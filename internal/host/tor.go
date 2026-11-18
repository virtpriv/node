package host

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/system"
	"github.com/virtpriv/node/internal/update/files"
)

// BuildTorConfig gives the content of vpn's torrc. Tor serves outgoing
// traffic only, so the file sets up no onion service.
func BuildTorConfig() string {
	var b strings.Builder
	b.WriteString("# Virtual Private Node, Tor configuration\n")
	b.WriteString("# vpn writes this file. Debian's own copy is kept as " +
		paths.TorrcDistrib + ".\n")
	b.WriteString("SOCKSPort 9050\n")

	// The installer's Tor routing check reads bootstrap progress on the
	// control port. It listens on loopback only and needs the cookie.
	b.WriteString("\n# Control port, for the installer's Tor check\n")
	b.WriteString("ControlPort 9051\n")
	b.WriteString("CookieAuthentication 1\n")
	return b.String()
}

// WriteTorConfig writes the torrc at install. Debian's own copy is set aside
// first, so vpn's file never overwrites it.
func WriteTorConfig() error {
	if err := divertDebianTorConfig(); err != nil {
		return fmt.Errorf("set aside Debian's %s: %w", paths.Torrc, err)
	}
	return writeTorConfigForInstall([]byte(BuildTorConfig()))
}

var (
	// A local dpkg diversion moves Debian's torrc to TorrcDistrib, and every
	// later Tor package puts its copy there, so a Tor update never replaces
	// vpn's file or asks about it. Adding the same diversion again changes
	// nothing, so a resumed install runs it again safely.
	divertDebianTorConfig = func() error {
		return system.RunRoot("dpkg-divert", "--local", "--rename",
			"--divert", paths.TorrcDistrib, "--add", paths.Torrc)
	}
	writeTorConfigForInstall = writeTorConfig
)

func writeTorConfig(content []byte) error {
	return system.WriteFileRootOwned(paths.Torrc, content, 0640, "root", "debian-tor")
}

var (
	runTorServiceAction = func(action string) error {
		return system.RunRoot("systemctl", action, "tor")
	}
	writeTorRestartRule = writeTorRestartDropIn
	reloadSystemdForTor = func() error {
		return system.RunRoot("systemctl", "daemon-reload")
	}
)

// torRestartRule replaces systemd's default restart wait of 100 ms for the
// real Tor. With that wait systemd's default start limit, five starts in ten
// seconds, stops the retries and Tor stays off until a reboot or a manual
// start. Thirty seconds apart, systemd
// never reaches that limit and keeps trying after a crash. A clean stop is
// not restarted.
const torRestartRule = "# Restart Tor 30 seconds after a crash, without giving up\n" +
	"[Service]\nRestartSec=30\n"

// writeTorRestartDropIn writes the rule for fresh installs only. Nodes
// installed earlier keep Debian's rule.
func writeTorRestartDropIn() error {
	dir := filepath.Dir(paths.TorRestartDropIn)
	if err := os.Mkdir(dir, 0o755); err == nil {
		if err := os.Chmod(dir, 0o755); err != nil {
			return fmt.Errorf("set Tor drop-in directory mode: %w", err)
		}
	} else if !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("create Tor drop-in directory: %w", err)
	}
	if info, err := os.Lstat(dir); err != nil {
		return err
	} else if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	// Saved every time, so a resume after a failed save still saves it.
	if err := files.Sync(filepath.Dir(dir)); err != nil {
		return err
	}
	return system.WriteFileRoot(paths.TorRestartDropIn,
		[]byte(torRestartRule), 0o644)
}

// EnableAndRestartTor belongs to initial installation. That operation owns
// establishing Tor's boot persistence as part of the base node. systemd
// loads the restart rule before Tor is restarted, so the rule applies from
// the first crash.
func EnableAndRestartTor() error {
	if err := writeTorRestartRule(); err != nil {
		return err
	}
	if err := reloadSystemdForTor(); err != nil {
		return err
	}
	if err := runTorServiceAction("enable"); err != nil {
		return err
	}
	return runTorServiceAction("restart")
}
