// Package installer owns initial preflight, lifecycle, sequencing and completion.
package installer

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/host"
	"github.com/virtpriv/node/internal/logger"
	"github.com/virtpriv/node/internal/loginpassword"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/system"
)

var appVersion = "dev"

func SetVersion(v string) { appVersion = v }

// ── Main install flow ────────────────────────────────────
//
// `vpn install` runs only because the owner asked for it, never because the
// binary inspected the machine and decided by itself. The old implicit flow
// and its configuration and service probe are gone.
//
// The step model, resume planner and step runner live in engine.go, the
// ledger in ledger.go, the interactive session in interactive.go, and
// presentation in internal/tui/install. The TUI submits choices and
// observes the engine's progress.

// InstallOptions carries the `vpn install` command line.
type InstallOptions struct {
	// Unattended runs without a TUI or prompts and generates a login password.
	// SSH keys are configured by the owner after installation.
	Unattended bool
}

// installStartupDependencies keeps the lifecycle authorization boundary
// testable against isolated paths. The production constructor below supplies
// the same paths, lock, classifier, preflight, and initializer used before this
// seam was introduced.
type installStartupDependencies struct {
	runtimeDir          string
	installLock         string
	fs                  lifecycleFS
	lookup              identityLookup
	acquireRunLock      func(string, string) (*os.File, error)
	classifyLifecycle   func(lifecycleFS, identityLookup) (lifecycleState, error)
	runPreflight        func() (host.SSHObservation, error)
	initializeLifecycle func(lifecycleFS, identityLookup) (*installLedger, error)
	// inhibitShutdown holds off restarts until the returned release runs.
	inhibitShutdown func(why string) (release func(), err error)
	// systemBusRunning reports whether anything could hold off a restart.
	systemBusRunning func() bool
}

func productionInstallStartupDependencies() installStartupDependencies {
	return installStartupDependencies{
		runtimeDir:          paths.RuntimeDir,
		installLock:         paths.InstallLock,
		fs:                  productionLifecycleFS(),
		lookup:              productionIdentityLookup(),
		acquireRunLock:      acquireRunLock,
		classifyLifecycle:   classifyLifecycleState,
		runPreflight:        RunPreflight,
		initializeLifecycle: initializeLifecycle,
		inhibitShutdown: func(why string) (func(), error) {
			inhibitor, err := system.InhibitShutdown(why)
			if err != nil {
				return nil, err
			}
			return func() { inhibitor.Release() }, nil
		},
		systemBusRunning: system.SystemBusRunning,
	}
}

// vpnProgramStep installs the vpn command.
const vpnProgramStep = "binary.install"

// basePackagesStep installs sudo, and the system bus where a machine has
// none.
const basePackagesStep = "apt.base"

// restartHold holds off restarts from when it is taken to the run's end.
type restartHold struct {
	take func(why string) (release func(), err error)
	held func()
}

// ensure takes the hold unless it is already held.
func (h *restartHold) ensure() error {
	if h.held != nil {
		return nil
	}
	release, err := h.take("installing the node")
	if err != nil {
		return fmt.Errorf("cannot hold off a restart: %w", err)
	}
	h.held = release
	return nil
}

func (h *restartHold) release() {
	if h.held != nil {
		h.held()
	}
}

var newInstallStartupDependencies = productionInstallStartupDependencies

// RunInstall is the `sudo vpn install` entry point.
func RunInstall(opts InstallOptions, frontend InstallFrontend) (retErr error) {
	if os.Geteuid() != 0 {
		return &StopError{NotStarted: true, StartErr: errors.New("the installer must run as root"),
			Again: againCommand(opts.Unattended, nil)}
	}

	// Package operations run without prompts for the whole run, as the
	// retired bootstrap script did, so debconf asks nothing.
	os.Setenv("DEBIAN_FRONTEND", "noninteractive")
	// A server named in no_proxy would make apt skip Tor for it.
	os.Unsetenv("no_proxy")
	deps := newInstallStartupDependencies()

	// The transient stable lock is acquired before classification. It is not
	// durable lifecycle state and remains the same inode while the ledger is
	// atomically replaced.
	runLock, err := deps.acquireRunLock(deps.runtimeDir, deps.installLock)
	if err != nil {
		return err
	}
	defer runLock.Close()

	fs := deps.fs
	lookup := deps.lookup
	lifecycle, err := deps.classifyLifecycle(fs, lookup)
	if err != nil {
		return err
	}
	// From here the install record is known. Once it shows root's SSH login
	// turned off, every error also says how to get back in.
	ledger := lifecycle.Ledger
	defer func() { retErr = withRootLoginOff(retErr, ledger) }()
	// A pause, a refused start and a failed step name the same command to
	// run the install again.
	defer func() {
		if stop, ok := errors.AsType[*StopError](retErr); ok && stop.Again == "" {
			stop.Again = againCommand(opts.Unattended, ledger)
		}
	}()
	if lifecycle.Disposition == lifecycleCompleted {
		logger.Install("already installed, no changes were made")
		fmt.Println("\n  Virtual Private Node is already installed. No changes were made.")
		return nil
	}
	if lifecycle.Disposition != lifecyclePristine &&
		!opts.Unattended && passwordPending() {
		return &StopError{NotStarted: true, Again: againCommand(true, ledger), StartErr: errors.New(
			"an unattended install stopped before it showed its password, so it has to be continued unattended")}
	}

	if !opts.Unattended && frontend == nil {
		return errors.New("interactive installation requires a frontend")
	}

	// Restarts wait until the run ends. Debian's update program restarts
	// the machine when the restart flag is up, and step 11 runs it. Without
	// a system bus nothing can hold off a restart, so the hold is taken at
	// the end of step 2, which installs and starts the bus. A run that would
	// skip step 2 does not start without it.
	hold := &restartHold{take: deps.inhibitShutdown}
	defer hold.release()
	if err := hold.ensure(); err != nil {
		if deps.systemBusRunning() || (ledger != nil && ledger.done(basePackagesStep)) {
			return &StopError{NotStarted: true, StartErr: err}
		}
		logger.Install("the system bus is not running, so restarts are held off once step 2 has started it")
	}

	// Preflight is read-only and precedes every durable initialization.
	_, err = deps.runPreflight()
	if err != nil {
		return err
	}

	if lifecycle.Disposition == lifecyclePristine {
		ledger, err = deps.initializeLifecycle(fs, lookup)
		if err != nil {
			return err
		}
	} else if lifecycle.Disposition == lifecycleBootstrap {
		ledger, err = resumeLifecycleBootstrap(fs, lookup)
		if err != nil {
			return err
		}
	}

	dec := &InstallDecisions{}
	steps := buildInstallSteps(dec, hold.ensure)
	if err := validateBaseInstallSteps(steps); err != nil {
		return err
	}

	completeInteractive := func() error {
		if err := prepareInstallCompletion(ledger, len(steps)); err != nil {
			return err
		}
		return publishTerminalLedger(ledger)
	}

	var res RunResult
	var startErr error
	// An unattended run generates its password. A run that only finishes a
	// recorded install needs one when an earlier pass applied a password it
	// never showed.
	if opts.Unattended && (lifecycle.Disposition != lifecycleCompletionPending || passwordPending()) {
		if err := fillGeneratedPassword(dec); err != nil {
			return err
		}
	}

	if lifecycle.Disposition == lifecycleCompletionPending {
		res = RunResult{Outcome: RunComplete, Total: len(steps)}
	} else if opts.Unattended {
		fmt.Printf("\n  Virtual Private Node, unattended install\n\n")
		res, err = RunInstallUnattended(
			steps, appVersion, ledger, paths.InstallStateFile)
	} else {
		runner, runnerErr := newStepRunner(steps, appVersion, ledger, paths.InstallStateFile)
		if runnerErr != nil {
			return runnerErr
		}
		session := newInstallSession(runner, dec, completeInteractive)
		res, err = runInstallInteractive(session, installView(session), frontend)
		startErr = session.refusal()
	}
	if err != nil {
		return err
	}
	if lifecycle.Disposition == lifecycleCompletionPending && !opts.Unattended {
		if err := completeInteractive(); err != nil {
			return err
		}
	}
	switch res.Outcome {
	case RunFailed:
		// The runner already logged the failure line. main prints the
		// error to stderr.
		return &StopError{StepName: res.StepName, StepNum: res.StepNum, Total: res.Total, Err: res.Err,
			RootLoginMayBeOff: rootLoginMayBeOff(res.Err)}
	case RunInterrupted:
		// The log must not just stop. Record how far the install got and
		// what running it again does.
		if len(ledger.Steps) == 0 {
			logger.Install("install did not start, run the installer again to start it")
			return &StopError{NotStarted: true, StartErr: startErr}
		}
		stop := &StopError{Paused: true, Total: len(steps), StartErr: startErr}
		if i, ok := nextStep(steps, ledger); ok {
			stop.StepNum, stop.StepName = i+1, steps[i].Name
		}
		for i, s := range steps {
			if ledger.done(s.Key) {
				stop.DoneNum, stop.DoneName = i+1, s.Name
			}
		}
		stop.Again = againCommand(opts.Unattended, ledger)
		logger.Install(
			"install PAUSED after step %d of %d (%s), "+
				"run %s to continue with step %d",
			stop.DoneNum, stop.Total, stop.DoneName, stop.Again, stop.StepNum)
		return stop
	}

	if opts.Unattended {
		if err := prepareInstallCompletion(ledger, len(steps)); err != nil {
			return err
		}
		if needsPasswordReapply(dec.GeneratedPassword,
			dec.PasswordApplied, passwordPending()) {
			// Seen in a live run. An earlier unattended pass applied a
			// generated password and died before printing it, so nobody
			// has seen a working password, and this pass skipped the
			// identity step from the ledger. Apply this pass's generated
			// password now, so the line printed below is one that works.
			if err := host.SetLoginPassword(dec.Password); err != nil {
				return fmt.Errorf(
					"re-apply admin password: %w", err)
			}
			dec.PasswordApplied = true
			logger.Install("admin password re-applied at " +
				"completion, because an earlier pass applied one that " +
				"was never displayed")
		}
	}

	if dec.GeneratedPassword != "" && dec.PasswordApplied {
		// Unattended installs only, and only when this pass applied the
		// password. A ledger skip with no pending marker means an older
		// password, already shown, still stands. Printed once, never logged.
		if err := printGeneratedPassword(dec.GeneratedPassword); err != nil {
			return err
		}
		if err := clearPasswordPendingMarkerStrict(); err != nil {
			return err
		}
	}
	if opts.Unattended {
		if err := publishTerminalLedger(ledger); err != nil {
			return err
		}
	}

	// Both paths leave the connect command in the shell. The install terminal
	// never opens the node TUI. The owner reaches it by logging in over SSH.
	printConnectInstructions()
	return nil
}

// withRootLoginOff adds the way back in to an install error once the install
// record shows root's SSH login turned off.
func withRootLoginOff(err error, ledger *installLedger) error {
	if err == nil || ledger == nil || !ledger.done(rootLoginOffStep) {
		return err
	}
	var stop *StopError
	if errors.As(err, &stop) {
		stop.RootLoginOff = true
		return err
	}
	return &StopError{Err: err, RootLoginOff: true}
}

func validateBaseInstallSteps(steps []InstallStep) error {
	if len(steps) != len(baseInstallStepKeys) {
		return fmt.Errorf("base install has %d steps, ledger schema requires %d",
			len(steps), len(baseInstallStepKeys))
	}
	for i, step := range steps {
		if step.Key != baseInstallStepKeys[i] {
			return fmt.Errorf("base install step %d is %q, ledger schema requires %q",
				i+1, step.Key, baseInstallStepKeys[i])
		}
	}
	return nil
}

func prepareInstallCompletion(ledger *installLedger, stepCount int) error {
	if !ledger.allBaseStepsDone() {
		return errors.New("cannot finalize installation before every base step is recorded")
	}
	if err := host.VerifyOperatorSudo(); err != nil {
		return err
	}
	if err := host.VerifyInitialOwnerSSH(); err != nil {
		return err
	}
	logger.Install("all %d install steps complete", stepCount)
	// Written once every step is recorded. It is how the TUI and the update
	// tell a finished install.
	if err := config.Save(config.Default()); err != nil {
		return fmt.Errorf("write %s: %w", config.DefaultPath, err)
	}
	if err := host.SetOperatorLogOwnership(); err != nil {
		return err
	}
	return nil
}

func publishTerminalLedger(ledger *installLedger) error {
	if passwordPending() {
		return errors.New(
			"cannot mark installation complete while password delivery is pending")
	}
	if err := ledger.markComplete(); err != nil {
		return err
	}
	if err := ledger.save(paths.InstallStateFile); err != nil {
		return fmt.Errorf("publish terminal install ledger: %w", err)
	}
	return nil
}

func printGeneratedPassword(password string) error {
	_, err := fmt.Fprintf(os.Stdout,
		"\n  Login password for %q (SAVE IT, it will not be shown again):\n\n  %s\n",
		paths.AdminUser, password)
	if err != nil {
		return fmt.Errorf("display generated login password: %w", err)
	}
	return nil
}

// fillGeneratedPassword prepares the password of an unattended install. Its
// delivery follows the applied/pending-marker contract.
func fillGeneratedPassword(dec *InstallDecisions) error {
	gen := generateAdminPassword()
	pw, err := loginpassword.New(gen)
	if err != nil {
		return err
	}
	dec.Password = pw
	dec.GeneratedPassword = gen
	return nil
}

// The package upgrade step calls these through variables so that a test can
// stand in for apt and for Tor. After an upgrade only Tor's own readiness is
// asked again. The exit probe of the Tor gate is not repeated, because a
// restart of Tor does not change whether torsocks intercepts.
var (
	upgradeDebian         = host.UpgradeDebian
	verifyTorAfterUpgrade = func(ctx context.Context, say func(string)) error {
		say("Checking that Tor is still connected")
		return waitForTorBootstrap(ctx, say, torStallWindow, torBootstrapCeiling)
	}
)

// buildInstallSteps returns the initial install step list. Every step
// carries a stable Key, its identity in the ledger without a version, and a
// Kind (gates run again on every pass, see engine.go).
//
// The firewall step comes right after the single apt operation outside Tor,
// so denying incoming traffic is in place before anything else. Outgoing
// traffic stays allowed so Tor can start behind it, and `ufw enable` does not
// break established SSH sessions. The base upgrade, a large download on an
// old image, waits until apt goes through Tor.
func buildInstallSteps(dec *InstallDecisions, holdRestarts func() error) []InstallStep {
	return []InstallStep{
		{Key: vpnProgramStep,
			Name: "Installing the vpn program",
			Run:  plain(host.InstallInitialBinary)},
		{Key: basePackagesStep,
			Name: "Installing Tor, the firewall, and base tools",
			Run: func(ctx context.Context, say func(string)) error {
				if err := host.InstallBasePackages(ctx, say); err != nil {
					return err
				}
				if err := host.ConfigureLogRotation(); err != nil {
					return err
				}
				// Held already, unless the machine had no system bus.
				return holdRestarts()
			}},
		{Key: "firewall", Name: "Closing every port except SSH",
			Run: plain(host.ConfigureInitialFirewall)},
		{Key: "host.prep",
			Name: "Checking the host name and clock sync",
			Run:  plain(host.PrepareBaseHost)},
		{Key: "identity.access",
			Name: "Creating your " + paths.AdminUser + " user account",
			Run: plain(func() error {
				return applyIdentityAccess(dec)
			})},
		{Key: "ipv6.disable", Name: "Turning off IPv6",
			Run: plain(host.DisableIPv6)},
		{Key: "mptcp.disable", Name: "Turning off Multipath TCP",
			Run: plain(host.DisableMPTCP)},
		{Key: "tor.configure", Name: "Setting up Tor",
			Run: plain(host.EnableAndRestartTor)},
		// Hard gate. No step below that sends apt through Tor runs unless
		// Tor routing is verified. See torgate.go. As a StepGate it is
		// checked again on every pass, resumes included, so no package comes
		// through Tor in a pass whose Tor routing was not verified in that
		// same pass. The check that torsocks is present runs inside this
		// step, after Tor is installed.
		{Key: "tor.gate", Name: "Verifying the Tor connection",
			Kind: StepGate, Run: verifyTorRouting},
		{Key: "apt.tor", Name: "Sending apt package downloads through Tor",
			Run: plain(host.ConfigureAptTor)},
		// Through Tor, like every later apt operation. Debian's own
		// unattended-upgrade runs with vpn's rules, the program every later
		// update runs, and its nightly job is turned on. An upgrade can
		// restart Tor, for example with a library Tor uses, so the step
		// reports done only once Tor is ready again. The next step
		// installs a package through it.
		{Key: "base.upgrade",
			Name: "Updating Debian and turning on its security updates",
			Run: func(ctx context.Context, say func(string)) error {
				if err := upgradeDebian(ctx, say); err != nil {
					return err
				}
				return verifyTorAfterUpgrade(ctx, say)
			}},
		// SSH login protection as early as its package can come through
		// Tor, on the lists the upgrade just refreshed.
		{Key: "security", Name: "Turning on fail2ban login protection",
			Run: func(ctx context.Context, say func(string)) error {
				if err := host.InstallFail2ban(ctx, say); err != nil {
					return err
				}
				return host.ConfigureFail2ban()
			}},
		// Debian's zram package, so it comes after apt uses Tor.
		{Key: "swap.zram", Name: "Setting up compressed swap",
			Run: host.SetUpCompressedSwap},

		// Checks the owner's sudo first, then turns root's SSH login off and
		// the owner's password login on. The root session running the
		// install stays open, so the later steps run in it.
		{Key: rootLoginOffStep,
			Name: "Turning off root login over SSH",
			Run:  plain(installSSHHardening)},
		// The unprivileged TUI uses vpnd's fixed requests. The owner can
		// separately authenticate with sudo for general host maintenance.
		{Key: "journal.access",
			Name: "Keeping system logs on disk for your " + paths.AdminUser + " user to read",
			Run:  plain(host.SetupJournalAccess)},
		{Key: "helper.enable",
			Name: "Starting vpnd, the node's root service",
			Run:  plain(host.InstallVPNDUnits)},
		// Last. It makes a folder in the owner's home.
		{Key: "releases.folder", Name: "Creating the releases folder",
			Run: plain(host.CreateReleasesFolder)},
	}
}
