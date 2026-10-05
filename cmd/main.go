//cmd/main.go

package main

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"strconv"

	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/helperd"
	"github.com/virtpriv/node/internal/host"
	"github.com/virtpriv/node/internal/installer"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/tui"
	installui "github.com/virtpriv/node/internal/tui/install"
	"github.com/virtpriv/node/internal/update"
)

var version = "dev"

// Explicit dispatch (IA-1-8's fix): what the binary does is
// decided by what the operator TYPED, never by sniffing box
// state. `vpn` is the node TUI; `sudo vpn install` is the
// installer; nothing infers one from the other.
func main() {
	installer.SetVersion(version)

	cmd, opts, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n\n%s", err, usage())
		os.Exit(2)
	}

	switch cmd {
	case cmdUpdateLaunch, cmdUpdateWorker:
		var err error
		if cmd == cmdUpdateLaunch {
			err = update.Launch()
		} else {
			err = update.RunWorker(version)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "vpn update: %v\n", err)
			os.Exit(1)
		}
	case cmdInstall:
		if err := installer.RunInstall(opts, installui.Run); err != nil {
			fmt.Fprintf(os.Stderr, "\n  Failed: %v\n", err)
			os.Exit(1)
		}
	case cmdHelperd:
		// The root helper. Started by systemd when traffic
		// arrives on its socket — never by hand; it verifies
		// both conditions itself and explains if they don't
		// hold.
		if err := helperd.Serve(version); err != nil {
			fmt.Fprintf(os.Stderr, "vpn helperd: %v\n", err)
			os.Exit(1)
		}
	case cmdStageLNDCert:
		// Run by systemd's certificate watch (a path unit on
		// LND's tls.cert) — never by hand. LND rewrites its
		// certificate on its own (tlsautorefresh); this
		// refreshes the TUI's staged copy to match. Its
		// output lands in this oneshot unit's own journal
		// (journalctl -u vpn-lnd-cert-stage.service), which is
		// where watcher-driven restages are audited — the
		// helper's journal only records operations that went
		// through the helper's socket.
		if os.Geteuid() != 0 {
			fmt.Fprintln(os.Stderr,
				"vpn stage-lnd-cert runs as root via its "+
					"systemd unit — it is not meant to be "+
					"started by hand")
			os.Exit(1)
		}
		// Explicit dispatch, nothing inferred — but refuse
		// fast and clearly on a box this command cannot apply
		// to, instead of waiting out the stager's stability
		// window against a file that will never appear.
		if _, err := os.Stat(config.DefaultPath); err != nil {
			fmt.Fprintf(os.Stderr,
				"vpn stage-lnd-cert: no configuration at %s "+
					"— this node is not installed\n",
				config.DefaultPath)
			os.Exit(1)
		}
		if err := host.StageLNDTLSCert(); err != nil {
			fmt.Fprintf(os.Stderr, "vpn stage-lnd-cert: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("staged LND TLS certificate copy refreshed")
	case cmdPublishLNDBackup:
		// Intended for lnd-backup-export.service. The publisher
		// validates the exact lnd identity and unit-local backup
		// group itself; this dispatch grants no privileges.
		if err := host.PublishLNDBackup(opts.Network); err != nil {
			fmt.Fprintf(os.Stderr,
				"vpn publish-lnd-backup: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("published LND channel backup")
	case cmdVersion:
		fmt.Println(version)
	case cmdHelp:
		fmt.Print(usage())
	case cmdConsole:
		runConsole()
	}
}

var errWrongTUIIdentity = errors.New("wrong effective identity for node TUI")

func loadConsoleConfig(
	euid int,
	lookup func(string) (*user.User, error),
	load func() (*config.AppConfig, error),
) (*config.AppConfig, error) {
	if euid == 0 {
		return nil, errWrongTUIIdentity
	}
	u, err := lookup(paths.AdminUser)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve %q identity: %v",
			errWrongTUIIdentity, paths.AdminUser, err)
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil || uid == 0 || euid != uid {
		return nil, errWrongTUIIdentity
	}
	return load()
}

// runConsole is the bare `vpn` path: the node TUI for the
// admin user. Fail-stop on an unloadable config (IA-1-C1): the
// error names the file and the reason, and Default() is NEVER
// substituted — a TUI running on defaults would render a
// mainnet node's screens over a testnet4 node's services and
// write the wrong answers back on its first save.
func runConsole() {
	cfg, err := loadConsoleConfig(os.Geteuid(), user.Lookup, config.Load)
	if errors.Is(err, errWrongTUIIdentity) {
		fmt.Fprintf(os.Stderr,
			"  The node TUI runs as the %q user.\n"+
				"  Connect with: ssh %s@<server-address>\n"+
				"  (To install or resume an interrupted install: sudo vpn install)\n",
			paths.AdminUser, paths.AdminUser)
		os.Exit(1)
	}
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr,
				"  No configuration found at %s — this node is "+
					"not installed.\n  To install: sudo vpn install\n",
				config.DefaultPath)
		} else {
			fmt.Fprintf(os.Stderr,
				"  Cannot start: configuration at %s is "+
					"unreadable:\n    %v\n"+
					"  Refusing to run with default settings in its "+
					"place — fix or restore the file.\n",
				config.DefaultPath, err)
		}
		os.Exit(1)
	}
	prefs, err := config.LoadPreferences()
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"  Warning: TUI preferences are unreadable (%v); using dark theme for this session.\n",
			err)
		prefs = config.DefaultPreferences()
	}
	tui.Show(cfg, prefs, version)
}

type command int

const (
	cmdConsole command = iota
	cmdInstall
	cmdHelperd
	cmdStageLNDCert
	cmdPublishLNDBackup
	cmdVersion
	cmdHelp
	cmdUpdateLaunch
	cmdUpdateWorker
)

// parseArgs maps the command line to a command. Pure —
// unit-tested.
func parseArgs(
	args []string,
) (command, installer.InstallOptions, error) {
	var opts installer.InstallOptions
	if len(args) == 0 {
		return cmdConsole, opts, nil
	}
	switch args[0] {
	case "update-launch", "update-worker":
		if len(args) != 1 {
			return 0, opts, fmt.Errorf("%s takes no arguments", args[0])
		}
		if args[0] == "update-launch" {
			return cmdUpdateLaunch, opts, nil
		}
		return cmdUpdateWorker, opts, nil
	case "install":
		var networkFlag string
		for _, a := range args[1:] {
			switch a {
			case "--testnet4":
				opts.Network = config.NetworkTestnet4
				if networkFlag != "" && networkFlag != a {
					return 0, opts, fmt.Errorf(
						"install network flags %s and %s conflict", networkFlag, a)
				}
				networkFlag = a
			case "--signet":
				opts.Network = config.NetworkPublicSignet
				if networkFlag != "" && networkFlag != a {
					return 0, opts, fmt.Errorf(
						"install network flags %s and %s conflict", networkFlag, a)
				}
				networkFlag = a
			case "--unattended":
				opts.Unattended = true
			case "--until=bake":
				opts.UntilBake = true
			case "--allow-console-only":
				return 0, opts, errors.New("--allow-console-only is no longer supported: installation enables password SSH for vpn")
			default:
				return 0, opts, fmt.Errorf(
					"unknown install flag %q", a)
			}
		}
		return cmdInstall, opts, nil
	case "helperd":
		if len(args) > 1 {
			return 0, opts, fmt.Errorf(
				"helperd takes no arguments")
		}
		return cmdHelperd, opts, nil
	case "stage-lnd-cert":
		if len(args) > 1 {
			return 0, opts, fmt.Errorf(
				"stage-lnd-cert takes no arguments")
		}
		return cmdStageLNDCert, opts, nil
	case "publish-lnd-backup":
		if len(args) != 2 {
			return 0, opts, fmt.Errorf(
				"publish-lnd-backup requires exactly one network")
		}
		if err := config.ValidateNetwork(args[1]); err != nil {
			return 0, opts, fmt.Errorf(
				"publish-lnd-backup: %w", err)
		}
		opts.Network = args[1]
		return cmdPublishLNDBackup, opts, nil
	case "version", "--version", "-v":
		return cmdVersion, opts, nil
	case "help", "--help", "-h":
		return cmdHelp, opts, nil
	default:
		return 0, opts, fmt.Errorf("unknown command %q", args[0])
	}
}

func usage() string {
	return `Virtual Private Node

Usage:
  vpn                open the node TUI (run as the ` +
		paths.AdminUser + ` user)
  sudo vpn install   start a fresh install or resume a recognized interruption
      --testnet4     use testnet4 instead of mainnet
      --signet       use default public signet (testing only)
      --unattended   no prompts (keys auto-copied from the box,
                     login password generated and printed once)
  vpn helperd        the node's root helper (started by systemd
                     through its socket, not by hand)
  vpn stage-lnd-cert refresh the node's staged copy of the
                     LND TLS certificate (run by systemd's
                     certificate watch, not by hand)
  vpn version        print the version
`
}
