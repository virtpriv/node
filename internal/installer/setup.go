// Package installer owns initial preflight, lifecycle, sequencing and completion.
package installer

import (
	"errors"
	"fmt"
	"os"

	"github.com/virtpriv/node/internal/component"
	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/host"
	"github.com/virtpriv/node/internal/logger"
	"github.com/virtpriv/node/internal/loginpassword"
	"github.com/virtpriv/node/internal/paths"
)

const (
	bitcoinUser   = "bitcoin"
	lndUser       = "lnd"
	syncthingUser = "syncthing"
	backupGroup   = "vpn-lnd-backup"
)

var appVersion = "dev"

func SetVersion(v string) { appVersion = v }

// ── Main install flow ────────────────────────────────────
//
// `vpn install` — explicit dispatch only (IA-1-8's fix): this
// runs because the operator asked, never because the binary
// sniffed box state and decided for itself. The old
// NeedsInstall() config/service-probe is deleted with the old
// implicit flow.
//
// The step model, resume planner, and step runner live in
// engine.go; the ledger in ledger.go; the interactive session
// in interactive.go; and presentation in internal/tui/install.
// The TUI submits choices and observes the engine's progress.

// InstallOptions carries the `vpn install` command line.
type InstallOptions struct {
	// Network from --testnet4 or --signet ("" = mainnet for a pristine host,
	// or keep the interrupted lifecycle's recorded answer).
	Network string
	// Unattended runs without a TUI or prompts and generates a login password.
	// SSH keys are configured by the owner after installation.
	Unattended bool
	// UntilBake runs only PhaseBake steps (image build
	// pipeline, ruling iv). Requires Unattended. The run ends
	// without terminal ledger status, without handoff, without the
	// verification banner — first-boot steps are still owed.
	UntilBake bool
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
	initializeLifecycle func(lifecycleFS, identityLookup, installContext) (*installLedger, error)
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
	}
}

var newInstallStartupDependencies = productionInstallStartupDependencies

// RunInstall is the `sudo vpn install` entry point.
func RunInstall(opts InstallOptions, frontend InstallFrontend) error {
	if os.Geteuid() != 0 {
		return errors.New(
			"the installer must run as root — run: sudo vpn install")
	}
	if opts.UntilBake && !opts.Unattended {
		return errors.New(
			"--until=bake requires --unattended (image build path)")
	}

	// Non-interactive package operations for the whole run
	// (absorbed from the retired bootstrap): debconf prompts
	// suppressed, needrestart auto-restarts services instead of
	// showing its dialog mid-upgrade.
	os.Setenv("DEBIAN_FRONTEND", "noninteractive")
	os.Setenv("NEEDRESTART_MODE", "a")
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
	if lifecycle.Disposition == lifecycleCompleted {
		fmt.Println("\n  Virtual Private Node is already installed; no changes were made.")
		return nil
	}
	if lifecycle.Disposition != lifecyclePristine &&
		!opts.Unattended && passwordPending() {
		return errors.New(
			"an interrupted unattended install still owes password delivery — resume with: sudo vpn install --unattended")
	}

	if !opts.Unattended && frontend == nil {
		return errors.New("interactive installation requires a frontend")
	}

	// Preflight is read-only and precedes every durable initialization.
	_, err = deps.runPreflight()
	if err != nil {
		return err
	}

	ledger := lifecycle.Ledger
	if lifecycle.Disposition == lifecyclePristine {
		network := opts.Network
		if network == "" {
			network = "mainnet"
		}
		ledger, err = deps.initializeLifecycle(fs, lookup, installContext{
			Network: network, InitialP2PMode: "tor",
		})
		if err != nil {
			return err
		}
	} else if lifecycle.Disposition == lifecycleBootstrap {
		ctx := *lifecycle.BootstrapContext
		if opts.Network != "" && opts.Network != ctx.Network {
			return fmt.Errorf(
				"--%s conflicts with the interrupted install network %q — drop the flag to resume the recorded lifecycle",
				opts.Network, ctx.Network)
		}
		ledger, err = resumeLifecycleBootstrap(fs, lookup, ctx)
		if err != nil {
			return err
		}
	} else if opts.Network != "" && opts.Network != ledger.Context.Network {
		return fmt.Errorf(
			"--%s conflicts with the interrupted install network %q — drop the flag to resume the recorded lifecycle",
			opts.Network, ledger.Context.Network)
	}
	if lifecycle.Disposition == lifecycleCompletionPending && opts.UntilBake {
		return errors.New(
			"base installation is awaiting finalization — resume without --until=bake")
	}

	// General configuration is reconstructed from immutable lifecycle context
	// and fresh observations. It is not installation-completion authority.
	cfg := config.Default()
	cfg.Network = ledger.Context.Network
	cfg.P2PMode = ledger.Context.InitialP2PMode
	dec := &InstallDecisions{}
	if ledger.Context.DbCacheMB != nil {
		dec.DbCacheMB = *ledger.Context.DbCacheMB
		cfg.DbCache = dec.DbCacheMB
	}

	allSteps := buildInstallSteps(cfg, dec)
	if err := validateBaseInstallSteps(allSteps); err != nil {
		return err
	}
	steps := allSteps
	if opts.UntilBake {
		steps = FilterPhase(steps, PhaseBake)
	}

	persistDbCache := func(v int) error {
		if err := ledger.setDbCache(v); err != nil {
			return err
		}
		if err := ledger.save(paths.InstallStateFile); err != nil {
			return fmt.Errorf("record db cache decision: %w", err)
		}
		dec.DbCacheMB = v
		cfg.DbCache = v
		return nil
	}
	completeInteractive := func() error {
		if err := prepareInstallCompletion(cfg, dec, ledger, len(allSteps)); err != nil {
			return err
		}
		return publishTerminalLedger(ledger)
	}

	var res RunResult
	openConsole := false
	if opts.Unattended && lifecycle.Disposition != lifecycleCompletionPending {
		if err := fillUnattendedDecisions(dec); err != nil {
			return err
		}
		if ledger.Context.DbCacheMB == nil {
			if err := persistDbCache(dec.DbCacheMB); err != nil {
				return err
			}
		} else {
			dec.DbCacheMB = *ledger.Context.DbCacheMB
			cfg.DbCache = dec.DbCacheMB
		}
	} else if opts.Unattended && passwordPending() {
		if err := fillGeneratedPassword(dec); err != nil {
			return err
		}
	}

	if lifecycle.Disposition == lifecycleCompletionPending {
		res = RunResult{Outcome: RunComplete, Total: len(allSteps)}
	} else if opts.Unattended {
		fmt.Printf("\n  Virtual Private Node — unattended install\n\n")
		res, err = RunInstallUnattended(
			steps, appVersion, ledger, paths.InstallStateFile)
	} else {
		runner, runnerErr := newStepRunner(steps, appVersion, ledger, paths.InstallStateFile)
		if runnerErr != nil {
			return runnerErr
		}
		session := newInstallSession(runner, dec, persistDbCache, completeInteractive)
		res, openConsole, err = runInstallInteractive(session, installView(session), frontend)
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
		// The runner already logged the failure line; the
		// error surfaces to stderr via main.
		return fmt.Errorf("%s: %w", res.StepName, res.Err)
	case RunInterrupted:
		// The log trail must not just stop (commit-5 addendum):
		// record how far the run got, and that a re-run resumes.
		logger.Install(
			"install INTERRUPTED at step %d/%d: %s — "+
				"run again to resume", res.StepNum, res.Total,
			res.StepName)
		return fmt.Errorf(
			"install interrupted at step %d/%d (%s) — "+
				"run again to resume", res.StepNum, res.Total,
			res.StepName)
	}

	if opts.UntilBake {
		// The bake slice completing is not the install
		// completing: first-boot steps (identity, hardware fit,
		// SSH hardening) are still owed on the deployed box.
		logger.Install(
			"bake phase complete (%d steps) — install NOT marked "+
				"complete; first-boot steps pending", res.Total)
		fmt.Printf("\n  Bake phase complete (%d steps).\n", res.Total)
		return nil
	}

	if opts.Unattended {
		if err := prepareInstallCompletion(
			cfg, dec, ledger, len(allSteps)); err != nil {
			return err
		}
		if needsPasswordReapply(dec.GeneratedPassword,
			dec.PasswordApplied, passwordPending()) {
			// Fail-then-resume (live-run finding): an earlier
			// unattended pass applied a generated password and
			// died before the end-of-run print, so nobody has
			// ever seen a working credential — and this pass
			// ledger-skipped the identity step. Re-apply THIS
			// pass's generated password now, so the line printed
			// below is one that works.
			if err := host.SetLoginPassword(dec.Password); err != nil {
				return fmt.Errorf(
					"re-apply admin password: %w", err)
			}
			dec.PasswordApplied = true
			logger.Install("admin password re-applied at " +
				"completion — an earlier pass applied one that " +
				"was never displayed")
		}
	}

	if dec.GeneratedPassword != "" && dec.PasswordApplied {
		// Unattended fallback only (ruling vii), and only when
		// this pass actually applied it (a ledger-skip with no
		// pending marker means an older, already-shown password
		// stands). Printed once, never logged.
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

	if opts.Unattended {
		printConnectInstructions()
		return nil
	}
	// The done screen offered a real choice (live-run fix):
	// Enter opens the node TUI here via the identity drop;
	// ctrl+c means exit, so exit — just leave the connect
	// command behind. The handoff degrades to printed
	// instructions, never to an error; the install is already
	// recorded either way.
	if openConsole {
		HandoffToAdminConsole()
	} else {
		printConnectInstructions()
	}
	return nil
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

func prepareInstallCompletion(
	cfg *config.AppConfig, dec *InstallDecisions,
	ledger *installLedger, stepCount int,
) error {
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
	if ledger.Context.DbCacheMB == nil {
		return errors.New("cannot finalize installation without recorded db cache")
	}
	cfg.DbCache = *ledger.Context.DbCacheMB
	dec.DbCacheMB = cfg.DbCache
	if ledger.done("identity.access") {
		observed, err := host.AdminLoginObserved()
		if err != nil {
			logger.Install("SSH login evidence unavailable; leaving verification pending: %v", err)
		}
		if err := host.SetKeyVerificationPending(!observed || err != nil); err != nil {
			return fmt.Errorf("prepare SSH login verification: %w", err)
		}
	}
	if err := config.Save(cfg); err != nil {
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
		"\n  Login password for %q (SAVE IT — it will not be shown again):\n\n    %s\n",
		paths.AdminUser, password)
	if err != nil {
		return fmt.Errorf("display generated login password: %w", err)
	}
	return nil
}

// fillUnattendedDecisions prepares a generated password and hardware defaults.
// Password delivery follows the applied/pending-marker contract.
func fillUnattendedDecisions(dec *InstallDecisions) error {
	if err := fillGeneratedPassword(dec); err != nil {
		return err
	}
	dec.DbCacheMB = RecommendDbCache(DetectHardware().RAMMB)
	return nil
}

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

// buildInstallSteps returns the initial-install step list. Every
// step carries a stable Key (the ledger identity — versionless),
// a Kind (gates re-run every pass), a Group where steps hand
// ephemeral material to each other (see engine.go), and a Phase
// (bake vs first-boot, ruling iv — assignments provisional until
// the image-track session ratifies the map; identity/SSH steps
// are first-boot per rulings vii/viii: they apply observed,
// per-box state that an image build box cannot know).
//
// Order (ruling xvi(b)): the firewall step sits immediately
// after the single clearnet apt op — default-deny lands before
// the base upgrade, the longest pre-Tor phase. Outbound stays
// default-allow so Tor can bootstrap behind it; established SSH
// sessions are unaffected by `ufw enable`.
func buildInstallSteps(
	cfg *config.AppConfig, dec *InstallDecisions,
) []InstallStep {
	// Pipeline working directories — created in each pipeline's
	// first step, captured by closures, cleaned up in the final
	// step. Random paths via os.MkdirTemp prevent symlink
	// attacks. NOTE these closures are exactly why the btc/lnd
	// triplets are resume-atomic Groups: the workdir path lives
	// only in this process, so a resumed process can never
	// re-enter a pipeline midway.
	var btcWork, lndWork string

	return []InstallStep{
		{Key: "binary.install",
			Name: "Installing the vpn binary",
			Fn:   host.InstallInitialBinary},
		{Key: "apt.base",
			Name: "Installing base packages",
			Fn:   host.InstallBasePackages},
		{Key: "firewall", Name: "Configuring firewall",
			Fn: func() error { return host.ConfigureInitialFirewall(cfg) }},
		{Key: "base.upgrade",
			Name: "Upgrading base packages",
			Fn:   host.UpgradePackages},
		{Key: "host.prep",
			Name: "Configuring hostname and clock sync",
			Fn:   host.PrepareBaseHost},
		{Key: "identity.access", Phase: PhaseFirstBoot,
			Name: "Creating the owner account (" +
				paths.AdminUser + ")",
			Fn: func() error {
				return applyIdentityAccess(dec)
			}},
		{Key: "service-identities.v1",
			Name: "Creating dedicated service identities",
			Fn: func() error {
				return createBaseServiceIdentities()
			}},
		{Key: "ipv6.disable", Name: "Disabling IPv6",
			Fn: host.DisableIPv6},
		{Key: "tor.configure", Name: "Configuring Tor",
			Fn: func() error {
				if err := host.WriteTorConfig(cfg); err != nil {
					return err
				}
				return host.EnableAndRestartTor()
			}},
		// HARD GATE (IA-2-K): no Tor-dependent network step below —
		// apt over the socks5h proxy, every DownloadRequireTor —
		// runs unless Tor routing is verified. See torgate.go.
		// StepGate: re-verified on EVERY pass including resumes —
		// no download step can execute in a pass whose Tor routing
		// was not verified in that same pass. The torsocks-present
		// assertion re-homed here from preflight (ruling xvi(c))
		// also lives inside this step: post-Tor-install,
		// pre-first-download.
		{Key: "tor.gate", Name: "Verifying Tor routing",
			Kind: StepGate, Fn: verifyTorRouting},
		{Key: "apt.torproxy", Name: "Configuring apt for Tor",
			Fn: func() error {
				if err := host.ConfigureAptTor(); err != nil {
					return err
				}
				return host.EnsureGPG()
			}},
		{Key: "btc.download", Group: "btc",
			Name: "Downloading Bitcoin Core " + component.BitcoinCoreVersion,
			Fn: func() error {
				var err error
				btcWork, err = os.MkdirTemp("", "vpn-btc-")
				if err != nil {
					return fmt.Errorf("create work dir: %w", err)
				}
				return host.DownloadBitcoinCore(component.BitcoinCoreVersion, btcWork)
			}},
		{Key: "btc.verify", Group: "btc",
			Name: "Verifying Bitcoin Core",
			Fn: func() error {
				return host.VerifyBitcoinCore(btcWork)
			}},
		{Key: "btc.install", Group: "btc",
			Name: "Installing Bitcoin Core",
			Fn: func() error {
				if err := host.InstallBitcoinCoreBinaries(
					component.BitcoinCoreVersion, btcWork); err != nil {
					return err
				}
				os.RemoveAll(btcWork)
				if err := host.WriteInitialNodeRPCConfig(cfg); err != nil {
					return err
				}
				return host.WriteBitcoindService()
			}},
		{Key: "btc.start", Name: "Starting Bitcoin Core",
			Fn: func() error { return host.EnableAndRestartBitcoind(cfg) }},
		{Key: "security", Name: "Configuring security",
			Fn: func() error {
				if err := host.InstallUnattendedUpgrades(); err != nil {
					return err
				}
				if err := host.ConfigureUnattendedUpgrades(); err != nil {
					return err
				}
				if err := host.InstallFail2ban(); err != nil {
					return err
				}
				return host.ConfigureFail2ban()
			}},

		// ── LND (Tor-only, non-interactive) ─────────
		{Key: "lnd.download", Group: "lnd",
			Name: "Downloading LND",
			Fn: func() error {
				var err error
				lndWork, err = os.MkdirTemp("", "vpn-lnd-")
				if err != nil {
					return fmt.Errorf("create work dir: %w", err)
				}
				return host.DownloadLND(component.LNDVersion, lndWork)
			}},
		{Key: "lnd.verify", Group: "lnd",
			Name: "Verifying LND",
			Fn: func() error {
				return host.VerifyLND(component.LNDVersion, lndWork)
			}},
		{Key: "lnd.install", Group: "lnd",
			Name: "Installing LND",
			Fn: func() error {
				if err := host.InstallLNDBinaries(
					component.LNDVersion, lndWork); err != nil {
					return err
				}
				os.RemoveAll(lndWork)
				return host.WriteLNDServiceFromConfig(cfg)
			}},
		{Key: "tor.lnd", Name: "Configuring Tor for LND",
			Fn: func() error {
				if err := host.WriteTorConfig(cfg); err != nil {
					return err
				}
				return host.EnableAndRestartTor()
			}},
		{Key: "lnd.configure",
			Name: "Finalizing LND onion configuration",
			Fn: func() error {
				// Re-read the onion after the dedicated LND Tor
				// restart and rewrite lnd.conf with the preserved,
				// LND-only bitcoind credential. Missing or invalid
				// onion state fails closed inside host.WriteLNDConfig.
				return host.WriteLNDConfig(cfg, "")
			}},
		{Key: "lnd.start", Name: "Starting LND", Fn: host.EnableAndRestartLND},
		{Key: "lnd.tls-san", Kind: StepGate,
			Name: "Verifying LND TLS onion certificate",
			Fn:   host.VerifyLNDTLSOnionSAN},
		// LND owns its TLS certificate lifecycle. At startup it
		// replaces an expired certificate, and tlsautorefresh
		// also replaces one whose configured SAN inputs changed.
		// No TUI operation necessarily requested that startup.
		// This watch re-stages the TUI's copy within seconds of any
		// rewrite. After lnd.start so an installation or resume arms it
		// on the certificate LND is actually serving.
		{Key: "lnd.certwatch",
			Name: "Watching the LND TLS certificate",
			Fn:   host.InstallLNDCertWatch},
		// Harden SSH after the access prerequisites are installed. Old
		// project drop-ins are lifecycle conflicts, not migration inputs.
		{Key: "ssh.harden", Phase: PhaseFirstBoot,
			Name: "Hardening SSH",
			Fn: func() error {
				return installSSHHardening()
			}},
		// The unprivileged TUI uses fixed helper operations. The owner can
		// separately authenticate with sudo for general host maintenance.
		{Key: "journal.access", Phase: PhaseFirstBoot,
			Name: "Granting journal read access",
			Fn:   host.SetupJournalAccess},
		{Key: "helper.enable", Phase: PhaseFirstBoot,
			Name: "Enabling the root helper socket",
			Fn:   host.InstallHelperUnits},
		{Key: "state.stage", Phase: PhaseFirstBoot,
			Name: "Staging node facts for the TUI",
			Fn:   StageBoardAll},
		// Formerly a post-TUI special case that warned but
		// completed anyway (IA-1-16). As a real step it
		// inherits the ledger, the completion gate, failure
		// logging, and resume — the special case is dead.
		{Key: "shellenv", Name: "Configuring shell environment",
			Fn: func() error {
				return host.SetupNodeCLI(cfg)
			}},
	}
}
