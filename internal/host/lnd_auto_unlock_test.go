package host

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lightningnetwork/lnd/lnrpc"

	"github.com/virtpriv/node/internal/autounlock"
	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/paths"
)

type autoUnlockFixture struct {
	cfg        config.AppConfig
	art        autoUnlockArtifacts
	loaded     autoUnlockUnit
	restart    string
	loadedDrop bool

	invocation string
	pid        int
	controlPID int
	args       []string
	wallet     lnrpc.WalletState
	password   string

	now                          time.Time
	startCount                   int
	stopCount                    int
	holdEnabled                  bool
	rpcOnly                      bool
	timeoutOnStart               bool
	enabledStartAdvance          time.Duration
	stopAdvance                  time.Duration
	stopped                      bool
	removedWhileUnlockRunning    bool
	changeDuringProcessRead      bool
	processChangesRemaining      int
	exitDuringProcessRead        bool
	walletStateFailuresRemaining int
	result                       string
	execMainStatus               string
	events                       []string

	calls  map[string]int
	failOn map[string]int
	after  map[string]bool
	always map[string]bool
}

func newAutoUnlockFixture(enabled bool) *autoUnlockFixture {
	f := &autoUnlockFixture{
		now:            time.Unix(1_700_000_000, 0),
		pid:            100,
		invocation:     "invocation-1",
		result:         "success",
		execMainStatus: "0",
		calls:          map[string]int{},
		failOn:         map[string]int{},
		after:          map[string]bool{},
		always:         map[string]bool{},
	}
	f.cfg = *config.Default()
	if enabled {
		f.cfg.AutoUnlock = true
		f.art = autoUnlockArtifacts{unit: autoUnlockUnitEnabled, password: true}
		f.loaded = autoUnlockUnitEnabled
		f.args = expectedLNDArgs(true)
		f.wallet = lnrpc.WalletState_SERVER_ACTIVE
		f.password = "correct"
	} else {
		f.art = autoUnlockArtifacts{unit: autoUnlockUnitPlain}
		f.loaded = autoUnlockUnitPlain
		f.args = expectedLNDArgs(false)
		f.wallet = lnrpc.WalletState_SERVER_ACTIVE
	}
	f.restart = "on-failure"
	return f
}

func (f *autoUnlockFixture) fail(name string) error {
	f.calls[name]++
	if f.always[name] {
		return fmt.Errorf("injected persistent %s failure", name)
	}
	if f.failOn[name] == f.calls[name] {
		return fmt.Errorf("injected %s failure", name)
	}
	return nil
}

func (f *autoUnlockFixture) ops() autoUnlockOps {
	startInvocation := func(failureName string) error {
		f.startCount++
		f.invocation = fmt.Sprintf("invocation-%d", f.startCount+1)
		f.stopped = false
		if err := f.fail(failureName); err != nil {
			return err
		}
		f.result = "success"
		f.execMainStatus = "0"
		if f.loaded == autoUnlockUnitEnabled {
			f.now = f.now.Add(f.enabledStartAdvance)
		}
		if f.loaded == autoUnlockUnitEnabled && f.timeoutOnStart {
			f.pid = 0
			f.args = nil
			f.wallet = lnrpc.WalletState_WAITING_TO_START
			f.result = "timeout"
			f.execMainStatus = "0"
			return errors.New("injected systemd start timeout")
		}
		if f.loaded == autoUnlockUnitEnabled && f.password != "correct" {
			f.pid = 0
			f.args = nil
			f.wallet = lnrpc.WalletState_WAITING_TO_START
			f.result = "exit-code"
			f.execMainStatus = "1"
			return errors.New("injected LND password rejection")
		}
		f.pid = 100 + f.startCount
		f.args = expectedLNDArgs(f.loaded == autoUnlockUnitEnabled)
		if f.loaded == autoUnlockUnitEnabled {
			if f.holdEnabled {
				f.wallet = lnrpc.WalletState_UNLOCKED
			} else if f.rpcOnly {
				f.wallet = lnrpc.WalletState_RPC_ACTIVE
			} else {
				f.wallet = lnrpc.WalletState_SERVER_ACTIVE
			}
		} else {
			f.wallet = lnrpc.WalletState_LOCKED
		}
		return nil
	}
	return autoUnlockOps{
		loadConfig: func() (*config.AppConfig, error) {
			if err := f.fail("load-config"); err != nil {
				return nil, err
			}
			cfg := f.cfg
			return &cfg, nil
		},
		saveConfig: func(cfg *config.AppConfig) error {
			name := "save-disabled"
			if cfg.AutoUnlock {
				name = "save-enabled"
			}
			if f.after[name] {
				f.cfg = *cfg
				return fmt.Errorf("injected %s post-publication failure", name)
			}
			if err := f.fail(name); err != nil {
				return err
			}
			f.cfg = *cfg
			return nil
		},
		artifacts: func() (autoUnlockArtifacts, error) {
			if err := f.fail("inspect"); err != nil {
				return autoUnlockArtifacts{}, err
			}
			return f.art, nil
		},
		writeUnit: func(unit autoUnlockUnit) error {
			name := "write-unit-plain"
			if unit == autoUnlockUnitEnabled {
				name = "write-unit-enabled"
			}
			if err := f.fail(name); err != nil {
				return err
			}
			f.art.unit = unit
			return nil
		},
		writePassword: func(password string) error {
			if err := f.fail("write-password"); err != nil {
				return err
			}
			f.password = password
			f.art.password = true
			f.art.passwordStage = false
			return nil
		},
		removePassword: func() error {
			if f.pid > 0 && equalStrings(f.args, expectedLNDArgs(true)) {
				f.removedWhileUnlockRunning = true
			}
			if f.after["remove-password"] {
				f.after["remove-password"] = false
				f.art.password = false
				f.art.passwordStage = false
				f.password = ""
				return errors.New("injected remove-password post-unlink failure")
			}
			if err := f.fail("remove-password"); err != nil {
				return err
			}
			f.art.password = false
			f.art.passwordStage = false
			f.password = ""
			return nil
		},
		writeVerifyDrop: func() error {
			if err := f.fail("write-drop"); err != nil {
				return err
			}
			f.art.verifyDrop = true
			return nil
		},
		removeVerifyDrop: func() error {
			if err := f.fail("remove-drop"); err != nil {
				return err
			}
			f.art.verifyDrop = false
			return nil
		},
		validateUnit: func() error { return f.fail("validate") },
		daemonReload: func() error {
			if err := f.fail("daemon-reload"); err != nil {
				return err
			}
			f.loaded = f.art.unit
			f.loadedDrop = f.art.verifyDrop
			if f.loadedDrop {
				f.restart = "no"
			} else {
				f.restart = "on-failure"
			}
			return nil
		},
		startLND: func() error {
			f.events = append(f.events, "start")
			return startInvocation("start-lnd")
		},
		stopLND: func() error {
			f.stopCount++
			f.events = append(f.events, "stop")
			if err := f.fail("stop-lnd"); err != nil {
				return err
			}
			alreadyFailed := f.pid == 0 && !f.stopped &&
				f.result != "success"
			f.now = f.now.Add(f.stopAdvance)
			f.pid = 0
			f.controlPID = 0
			f.args = nil
			f.wallet = lnrpc.WalletState_WAITING_TO_START
			if !alreadyFailed {
				f.stopped = true
				f.result = "success"
				f.execMainStatus = "0"
			}
			return nil
		},
		unitStatus: func() (lndUnitStatus, error) {
			if err := f.fail("unit-status"); err != nil {
				return lndUnitStatus{}, err
			}
			active := "active"
			sub := "running"
			if f.stopped {
				active = "inactive"
				sub = "dead"
			} else if f.pid == 0 {
				active = "failed"
				sub = "failed"
			}
			drops := []string(nil)
			if f.loadedDrop {
				drops = []string{paths.LNDVerificationDropIn}
			}
			return lndUnitStatus{
				invocationID:     f.invocation,
				mainPID:          f.pid,
				controlPID:       f.controlPID,
				serviceType:      "notify",
				activeState:      active,
				subState:         sub,
				result:           f.result,
				execMainStatus:   f.execMainStatus,
				restart:          f.restart,
				fragmentPath:     paths.LNDService,
				dropInPaths:      drops,
				needDaemonReload: "no",
				execStart: "{ path=/usr/local/bin/lnd ; argv[]=" +
					strings.Join(expectedLNDArgs(f.loaded == autoUnlockUnitEnabled), " ") +
					" ; ignore_errors=no ; }",
			}, nil
		},
		processArgs: func(pid int) ([]string, error) {
			if err := f.fail("process-args"); err != nil {
				return nil, err
			}
			if pid != f.pid || pid == 0 {
				return nil, errors.New("process does not exist")
			}
			args := append([]string(nil), f.args...)
			if f.exitDuringProcessRead {
				f.exitDuringProcessRead = false
				f.pid = 0
				f.args = nil
				f.result = "exit-code"
				f.execMainStatus = "1"
			} else if f.changeDuringProcessRead ||
				f.processChangesRemaining > 0 {
				f.changeDuringProcessRead = false
				if f.processChangesRemaining > 0 {
					f.processChangesRemaining--
				}
				f.invocation += "-changed"
				f.pid++
			}
			return args, nil
		},
		walletState: func() (lnrpc.WalletState, error) {
			if f.walletStateFailuresRemaining > 0 {
				f.walletStateFailuresRemaining--
				return lnrpc.WalletState_WAITING_TO_START,
					errors.New("injected transient wallet-state failure")
			}
			if err := f.fail("wallet-state"); err != nil {
				return lnrpc.WalletState_WAITING_TO_START, err
			}
			return f.wallet, nil
		},
		now: func() time.Time { return f.now },
		sleep: func(d time.Duration) {
			f.now = f.now.Add(d)
		},
	}
}

func assertDisabled(t *testing.T, f *autoUnlockFixture) {
	t.Helper()
	if f.cfg.AutoUnlock || f.art.unit != autoUnlockUnitPlain ||
		f.art.password || f.art.passwordStage || f.art.verifyDrop ||
		f.restart != "on-failure" ||
		f.wallet != lnrpc.WalletState_LOCKED {
		t.Fatalf("not safely disabled: cfg=%+v art=%+v restart=%s wallet=%s",
			f.cfg, f.art, f.restart, f.wallet)
	}
}

func assertDisabledDisk(t *testing.T, f *autoUnlockFixture) {
	t.Helper()
	if f.cfg.AutoUnlock || f.art.unit != autoUnlockUnitPlain ||
		f.art.password || f.art.passwordStage || f.art.verifyDrop ||
		f.restart != "on-failure" {
		t.Fatalf("disabled disk state did not converge: cfg=%+v art=%+v restart=%s",
			f.cfg, f.art, f.restart)
	}
}

func assertEnabled(t *testing.T, f *autoUnlockFixture) {
	t.Helper()
	if !f.cfg.AutoUnlock || f.art.unit != autoUnlockUnitEnabled ||
		!f.art.password || f.art.verifyDrop || f.restart != "on-failure" ||
		!walletStateMatches(f.wallet, lnrpc.WalletState_RPC_ACTIVE) {
		t.Fatalf("not safely enabled: cfg=%+v art=%+v restart=%s wallet=%s",
			f.cfg, f.art, f.restart, f.wallet)
	}
}

func TestEnableAutoUnlockProvesAndPublishes(t *testing.T) {
	f := newAutoUnlockFixture(false)
	result := enableAutoUnlock("correct", f.ops())
	if result.Outcome != autounlock.Enabled {
		t.Fatalf("outcome = %+v", result)
	}
	assertEnabled(t, f)
	if f.startCount != 1 || f.calls["wallet-state"] < 2 {
		t.Fatalf("starts=%d wallet-state checks=%d",
			f.startCount, f.calls["wallet-state"])
	}
}

func TestEnableAutoUnlockAcceptsRPCActiveDuringChainSync(t *testing.T) {
	f := newAutoUnlockFixture(false)
	f.rpcOnly = true
	result := enableAutoUnlock("correct", f.ops())
	if result.Outcome != autounlock.Enabled {
		t.Fatalf("outcome = %+v", result)
	}
	assertEnabled(t, f)
	if f.wallet != lnrpc.WalletState_RPC_ACTIVE {
		t.Fatalf("wallet state = %s, want RPC_ACTIVE", f.wallet)
	}
	if f.calls["wallet-state"] < 2 {
		t.Fatalf("wallet-state checks = %d, want at least 2",
			f.calls["wallet-state"])
	}
}

func TestEnableWrongPasswordReturnsToLockedRetryState(t *testing.T) {
	f := newAutoUnlockFixture(false)
	result := enableAutoUnlock("wrong", f.ops())
	if result.Outcome != autounlock.VerificationFailed || result.Detail != "" {
		t.Fatalf("outcome = %+v", result)
	}
	assertDisabled(t, f)
	if f.startCount != 2 {
		t.Fatalf("starts = %d, want candidate plus locked rollback", f.startCount)
	}
}

func TestEnableRecoversFailedPasswordRejectionResidue(t *testing.T) {
	f := newAutoUnlockFixture(false)
	f.art = autoUnlockArtifacts{
		unit:       autoUnlockUnitPlain,
		password:   true,
		verifyDrop: true,
	}
	f.loaded = autoUnlockUnitPlain
	f.loadedDrop = true
	f.restart = "no"
	f.invocation = "failed-password-rejection"
	f.pid = 0
	f.args = nil
	f.wallet = lnrpc.WalletState_WAITING_TO_START
	f.password = "wrong"
	f.result = "exit-code"
	f.execMainStatus = "1"

	result := enableAutoUnlock("correct", f.ops())
	if result.Outcome != autounlock.Enabled {
		t.Fatalf("outcome = %+v", result)
	}
	assertEnabled(t, f)
	if f.startCount != 2 {
		t.Fatalf("starts = %d, want locked recovery plus candidate", f.startCount)
	}
}

func TestEnablePostReadinessProofTimeoutIsInconclusiveAndLocked(t *testing.T) {
	f := newAutoUnlockFixture(false)
	f.holdEnabled = true
	result := enableAutoUnlock("correct", f.ops())
	if result.Outcome != autounlock.VerificationFailed {
		t.Fatalf("outcome = %+v", result)
	}
	assertDisabled(t, f)
	if elapsed := f.now.Sub(time.Unix(1_700_000_000, 0)); elapsed < autoUnlockPostconditionTimeout {
		t.Fatalf("post-readiness proof elapsed only %s", elapsed)
	}
	if f.removedWhileUnlockRunning {
		t.Fatal("rollback removed the password while auto-unlock LND was running")
	}
}

func TestEnableClassifiesSystemdReadinessTimeout(t *testing.T) {
	f := newAutoUnlockFixture(false)
	f.timeoutOnStart = true
	result := enableAutoUnlock("correct", f.ops())
	if result.Outcome != autounlock.VerificationTimedOut {
		t.Fatalf("outcome = %+v", result)
	}
	assertDisabled(t, f)
}

func TestEnableSlowGracefulStopDoesNotConsumeStartupWindow(t *testing.T) {
	f := newAutoUnlockFixture(false)
	f.stopAdvance = 299 * time.Second
	f.enabledStartAdvance = 119 * time.Second
	started := f.now
	result := enableAutoUnlock("correct", f.ops())
	if result.Outcome != autounlock.Enabled {
		t.Fatalf("outcome = %+v", result)
	}
	if elapsed := f.now.Sub(started); elapsed < 418*time.Second {
		t.Fatalf("elapsed %s, want independent stop and start windows", elapsed)
	}
	if f.stopCount != 1 || f.startCount != 1 ||
		!equalStrings(f.events, []string{"stop", "start"}) {
		t.Fatalf("service events = %q, stops=%d starts=%d",
			f.events, f.stopCount, f.startCount)
	}
	assertEnabled(t, f)
}

func TestEnableRejectsLateCandidateStartSuccess(t *testing.T) {
	f := newAutoUnlockFixture(false)
	f.enabledStartAdvance = 121 * time.Second
	result := enableAutoUnlock("correct", f.ops())
	if result.Outcome != autounlock.VerificationTimedOut {
		t.Fatalf("outcome = %+v", result)
	}
	assertDisabled(t, f)
}

func TestEnableHasSeparateBoundedPostReadinessProof(t *testing.T) {
	f := newAutoUnlockFixture(false)
	f.enabledStartAdvance = 119 * time.Second
	f.walletStateFailuresRemaining = 2
	result := enableAutoUnlock("correct", f.ops())
	if result.Outcome != autounlock.Enabled {
		t.Fatalf("outcome = %+v", result)
	}
	if elapsed := f.now.Sub(time.Unix(1_700_000_000, 0)); elapsed != 121*time.Second {
		t.Fatalf("elapsed = %s, want 119s start plus 2s proof", elapsed)
	}
	assertEnabled(t, f)
}

func TestEnableRollbackStopFailureRetainsCandidatePassword(t *testing.T) {
	f := newAutoUnlockFixture(false)
	f.holdEnabled = true
	f.failOn["stop-lnd"] = 2
	result := enableAutoUnlock("correct", f.ops())
	if result.Outcome != autounlock.RepairRequired {
		t.Fatalf("outcome = %+v", result)
	}
	if !f.art.password {
		t.Fatal("candidate password was removed before LND stopped")
	}
	if f.removedWhileUnlockRunning {
		t.Fatal("rollback removed the password while auto-unlock LND was running")
	}
}

func TestEnableRollbackRemovalFailureStillStartsLockedPlainLND(t *testing.T) {
	f := newAutoUnlockFixture(false)
	f.holdEnabled = true
	// The first removal confirms the disabled starting state. The second is
	// rollback after the candidate invocation has stopped.
	f.failOn["remove-password"] = 2
	result := enableAutoUnlock("correct", f.ops())
	if result.Outcome != autounlock.RepairRequired {
		t.Fatalf("outcome = %+v", result)
	}
	if f.pid == 0 || !equalStrings(f.args, expectedLNDArgs(false)) ||
		f.wallet != lnrpc.WalletState_LOCKED {
		t.Fatalf("plain locked recovery did not start: pid=%d args=%q state=%s",
			f.pid, f.args, f.wallet)
	}
	if f.removedWhileUnlockRunning {
		t.Fatal("rollback removed the password while auto-unlock LND was running")
	}
}

func TestEnablePublicationFailureRollsBackDisabled(t *testing.T) {
	f := newAutoUnlockFixture(false)
	f.failOn["save-enabled"] = 1
	result := enableAutoUnlock("correct", f.ops())
	if result.Outcome != autounlock.VerificationFailed || result.Detail == "" {
		t.Fatalf("outcome = %+v", result)
	}
	assertDisabled(t, f)
}

func TestEnableRecoversRecognizableInterruptedAttemptWithoutMarker(t *testing.T) {
	f := newAutoUnlockFixture(false)
	f.art = autoUnlockArtifacts{
		unit:          autoUnlockUnitEnabled,
		password:      true,
		passwordStage: true,
		verifyDrop:    true,
	}
	f.loaded = autoUnlockUnitEnabled
	f.loadedDrop = true
	f.restart = "no"
	f.args = expectedLNDArgs(true)
	f.password = "correct"
	result := enableAutoUnlock("correct", f.ops())
	if result.Outcome != autounlock.Enabled {
		t.Fatalf("outcome = %+v", result)
	}
	assertEnabled(t, f)
	if f.startCount != 2 {
		t.Fatalf("starts = %d, want recovery plus candidate", f.startCount)
	}
}

func TestEnableRollbackFailureRequiresRepair(t *testing.T) {
	f := newAutoUnlockFixture(false)
	f.failOn["write-unit-plain"] = 2
	result := enableAutoUnlock("wrong", f.ops())
	if result.Outcome != autounlock.RepairRequired ||
		result.FailedStep != "automatic recovery after password rejection" {
		t.Fatalf("outcome = %+v", result)
	}
}

func TestEnableFailureInjectionConvergesToDisabledState(t *testing.T) {
	tests := []struct {
		name        string
		inject      func(*autoUnlockFixture)
		wantOutcome autounlock.Outcome
	}{
		{"write password", func(f *autoUnlockFixture) { f.failOn["write-password"] = 1 }, autounlock.VerificationFailed},
		{"write unit", func(f *autoUnlockFixture) { f.failOn["write-unit-enabled"] = 1 }, autounlock.VerificationFailed},
		{"validate candidate", func(f *autoUnlockFixture) { f.failOn["validate"] = 2 }, autounlock.VerificationFailed},
		{"reload candidate", func(f *autoUnlockFixture) { f.failOn["daemon-reload"] = 2 }, autounlock.VerificationFailed},
		{"stop before candidate", func(f *autoUnlockFixture) { f.failOn["stop-lnd"] = 1 }, autounlock.VerificationFailed},
		{"start candidate", func(f *autoUnlockFixture) { f.failOn["start-lnd"] = 1 }, autounlock.VerificationFailed},
		{"read candidate process", func(f *autoUnlockFixture) { f.failOn["process-args"] = 2 }, autounlock.VerificationFailed},
		{"remove verification drop-in", func(f *autoUnlockFixture) { f.failOn["remove-drop"] = 1 }, autounlock.VerificationFailed},
		{"reload final policy", func(f *autoUnlockFixture) { f.failOn["daemon-reload"] = 3 }, autounlock.VerificationFailed},
		{"recheck process", func(f *autoUnlockFixture) { f.failOn["process-args"] = 3 }, autounlock.VerificationFailed},
		{"recheck state", func(f *autoUnlockFixture) { f.failOn["wallet-state"] = 2 }, autounlock.VerificationFailed},
		{"publish enabled config", func(f *autoUnlockFixture) { f.failOn["save-enabled"] = 1 }, autounlock.VerificationFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newAutoUnlockFixture(false)
			test.inject(f)
			result := enableAutoUnlock("correct", f.ops())
			if result.Outcome != test.wantOutcome {
				t.Fatalf("outcome = %+v, want %s", result, test.wantOutcome)
			}
			assertDisabledDisk(t, f)
		})
	}
}

func TestDisableAutoUnlockDeletesPasswordAndPublishes(t *testing.T) {
	f := newAutoUnlockFixture(true)
	result := disableAutoUnlockTransition(f.ops())
	if result.Outcome != autounlock.Disabled {
		t.Fatalf("outcome = %+v", result)
	}
	assertDisabled(t, f)
	if f.startCount != 1 {
		t.Fatalf("starts = %d, want one", f.startCount)
	}
}

func TestDisableFailureBeforeDeletionRestoresEnabledState(t *testing.T) {
	f := newAutoUnlockFixture(true)
	f.failOn["validate"] = 1
	result := disableAutoUnlockTransition(f.ops())
	if result.Outcome != autounlock.StillEnabled {
		t.Fatalf("outcome = %+v", result)
	}
	assertEnabled(t, f)
}

func TestDisableRemovalFailureWithPasswordPresentRestoresEnabled(t *testing.T) {
	f := newAutoUnlockFixture(true)
	f.failOn["remove-password"] = 1
	result := disableAutoUnlockTransition(f.ops())
	if result.Outcome != autounlock.StillEnabled {
		t.Fatalf("outcome = %+v", result)
	}
	assertEnabled(t, f)
}

func TestDisableRemovalErrorAfterUnlinkFinishesDisabled(t *testing.T) {
	f := newAutoUnlockFixture(true)
	f.after["remove-password"] = true
	result := disableAutoUnlockTransition(f.ops())
	if result.Outcome != autounlock.Disabled {
		t.Fatalf("outcome = %+v", result)
	}
	assertDisabled(t, f)
}

func TestDisableRollbackFailureRequiresRepair(t *testing.T) {
	f := newAutoUnlockFixture(true)
	f.password = "wrong"
	f.failOn["validate"] = 1
	result := disableAutoUnlockTransition(f.ops())
	if result.Outcome != autounlock.RepairRequired {
		t.Fatalf("outcome = %+v", result)
	}
}

func TestDisableResumesAfterPasswordCommitPoint(t *testing.T) {
	f := newAutoUnlockFixture(true)
	f.art = autoUnlockArtifacts{unit: autoUnlockUnitPlain}
	f.loaded = autoUnlockUnitPlain
	f.args = expectedLNDArgs(false)
	f.wallet = lnrpc.WalletState_LOCKED
	result := disableAutoUnlockTransition(f.ops())
	if result.Outcome != autounlock.Disabled {
		t.Fatalf("outcome = %+v", result)
	}
	assertDisabled(t, f)
}

func TestDisablePublicationFailureAfterDeletionRequiresRepair(t *testing.T) {
	f := newAutoUnlockFixture(true)
	f.failOn["save-disabled"] = 1
	result := disableAutoUnlockTransition(f.ops())
	if result.Outcome != autounlock.RepairRequired {
		t.Fatalf("outcome = %+v", result)
	}
	if f.art.password {
		t.Fatal("password was recreated after the irreversible deletion point")
	}
}

func TestDisableFailureInjectionBeforeDeletionRestoresEnabled(t *testing.T) {
	tests := []struct {
		name   string
		inject func(*autoUnlockFixture)
	}{
		{"write plain unit", func(f *autoUnlockFixture) { f.failOn["write-unit-plain"] = 1 }},
		{"validate plain unit", func(f *autoUnlockFixture) { f.failOn["validate"] = 1 }},
		{"reload plain unit", func(f *autoUnlockFixture) { f.failOn["daemon-reload"] = 1 }},
		{"inspect loaded plain unit", func(f *autoUnlockFixture) { f.failOn["unit-status"] = 1 }},
		{"stop before plain LND", func(f *autoUnlockFixture) { f.failOn["stop-lnd"] = 1 }},
		{"start plain LND", func(f *autoUnlockFixture) { f.failOn["start-lnd"] = 1 }},
		{"inspect plain process", func(f *autoUnlockFixture) { f.failOn["process-args"] = 1 }},
		{"remove verification drop-in", func(f *autoUnlockFixture) { f.failOn["remove-drop"] = 1 }},
		{"reload final policy", func(f *autoUnlockFixture) { f.failOn["daemon-reload"] = 2 }},
		{"recheck locked process", func(f *autoUnlockFixture) { f.failOn["process-args"] = 2 }},
		{"recheck locked state", func(f *autoUnlockFixture) { f.failOn["wallet-state"] = 2 }},
		{"remove password before unlink", func(f *autoUnlockFixture) { f.failOn["remove-password"] = 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newAutoUnlockFixture(true)
			test.inject(f)
			result := disableAutoUnlockTransition(f.ops())
			if result.Outcome != autounlock.StillEnabled {
				t.Fatalf("outcome = %+v", result)
			}
			assertEnabled(t, f)
		})
	}
}

func TestDisableFailureInjectionAfterDeletionNeverRecreatesPassword(t *testing.T) {
	tests := []struct {
		name   string
		inject func(*autoUnlockFixture)
	}{
		{"synchronize committed deletion", func(f *autoUnlockFixture) {
			f.after["remove-password"] = true
			f.failOn["remove-password"] = 1
		}},
		{"inspect committed deletion", func(f *autoUnlockFixture) { f.failOn["inspect"] = 2 }},
		{"validate final plain unit", func(f *autoUnlockFixture) { f.failOn["validate"] = 2 }},
		{"reload final plain unit", func(f *autoUnlockFixture) { f.failOn["daemon-reload"] = 3 }},
		{"publish disabled config", func(f *autoUnlockFixture) { f.failOn["save-disabled"] = 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newAutoUnlockFixture(true)
			test.inject(f)
			result := disableAutoUnlockTransition(f.ops())
			if result.Outcome != autounlock.RepairRequired {
				t.Fatalf("outcome = %+v", result)
			}
			if f.art.password {
				t.Fatal("password was recreated after the deletion commit point")
			}
		})
	}
}

func TestLoadedExecMatchesOnlyExactCommand(t *testing.T) {
	good := "{ path=/usr/local/bin/lnd ; argv[]=" +
		strings.Join(expectedLNDArgs(true), " ") + " ; ignore_errors=no ; }"
	if !loadedExecMatches(good, true) {
		t.Fatal("exact auto-unlock command was rejected")
	}
	for _, bad := range []string{
		strings.Replace(good, paths.LNDWalletPassword, "/tmp/password", 1),
		strings.Replace(good, " ; ignore_errors", " --extra ; ignore_errors", 1),
		strings.Replace(good, "argv[]=", "command[]=", 1),
	} {
		if loadedExecMatches(bad, true) {
			t.Fatalf("non-exact command accepted: %s", bad)
		}
	}
}

func TestRPCReadinessStateMatching(t *testing.T) {
	for _, test := range []struct {
		state lnrpc.WalletState
		want  bool
	}{
		{lnrpc.WalletState_UNLOCKED, false},
		{lnrpc.WalletState_RPC_ACTIVE, true},
		{lnrpc.WalletState_SERVER_ACTIVE, true},
		{lnrpc.WalletState_LOCKED, false},
	} {
		if got := walletStateMatches(
			test.state, lnrpc.WalletState_RPC_ACTIVE,
		); got != test.want {
			t.Errorf("state %s match = %v, want %v",
				test.state, got, test.want)
		}
	}
}

func TestStableProcessProofRejectsInvocationChange(t *testing.T) {
	f := newAutoUnlockFixture(false)
	ops := f.ops()
	status, err := ops.unitStatus()
	if err != nil {
		t.Fatal(err)
	}
	f.changeDuringProcessRead = true
	err = verifyStableProcessArgs(ops, status, false)
	if !errors.Is(err, errLNDInvocationChanged) {
		t.Fatalf("unstable process proof error = %v", err)
	}
}

func TestInvocationWaitRejectsProcessChangeDuringInspection(t *testing.T) {
	f := newAutoUnlockFixture(true)
	f.changeDuringProcessRead = true
	outcome, err := waitForInvocation(
		f.ops(), "older-invocation", true,
		lnrpc.WalletState_RPC_ACTIVE, 2*time.Second,
	)
	if outcome != invocationExited ||
		!errors.Is(err, errLNDInvocationChanged) {
		t.Fatalf("outcome=%v error=%v, want rejected transition", outcome, err)
	}
}

func TestInvocationWaitClassifiesProcessExitDuringInspection(t *testing.T) {
	f := newAutoUnlockFixture(true)
	f.exitDuringProcessRead = true
	outcome, err := waitForInvocation(
		f.ops(), "older-invocation", true,
		lnrpc.WalletState_RPC_ACTIVE, 2*time.Second,
	)
	if outcome != invocationExited || err == nil {
		t.Fatalf("outcome=%v error=%v, want exited", outcome, err)
	}
}

func TestStableProcessProofRejectsUnexpectedArguments(t *testing.T) {
	f := newAutoUnlockFixture(false)
	f.args = append(f.args, "--unexpected")
	ops := f.ops()
	status, err := ops.unitStatus()
	if err != nil {
		t.Fatal(err)
	}
	err = verifyStableProcessArgs(ops, status, false)
	if err == nil || !strings.Contains(err.Error(), "arguments") {
		t.Fatalf("unexpected process arguments error = %v", err)
	}
}

func TestStableProcessProofRejectsControlProcess(t *testing.T) {
	f := newAutoUnlockFixture(false)
	ops := f.ops()
	status, err := ops.unitStatus()
	if err != nil {
		t.Fatal(err)
	}
	status.controlPID = 42
	if err := verifyStableProcessArgs(ops, status, false); err == nil {
		t.Fatal("running invocation with a control process was accepted")
	}
}

func TestStopProofAcceptsOnlyProcessFreeQuiescentStates(t *testing.T) {
	for _, state := range []string{"inactive", "failed"} {
		t.Run("accept "+state, func(t *testing.T) {
			ops := autoUnlockOps{
				stopLND: func() error { return nil },
				unitStatus: func() (lndUnitStatus, error) {
					return lndUnitStatus{
						activeState: state,
						restart:     "no",
					}, nil
				},
			}
			if err := stopAndVerifyNoProcess(ops); err != nil {
				t.Fatalf("process-free %s state rejected: %v", state, err)
			}
		})
	}

	tests := []struct {
		name   string
		status lndUnitStatus
	}{
		{"main process", lndUnitStatus{
			activeState: "failed", mainPID: 41, restart: "no",
		}},
		{"control process", lndUnitStatus{
			activeState: "failed", controlPID: 42, restart: "no",
		}},
		{"restart policy", lndUnitStatus{
			activeState: "failed", restart: "on-failure",
		}},
		{"active state", lndUnitStatus{
			activeState: "active", restart: "no",
		}},
		{"activating state", lndUnitStatus{
			activeState: "activating", restart: "no",
		}},
		{"deactivating state", lndUnitStatus{
			activeState: "deactivating", restart: "no",
		}},
	}
	for _, test := range tests {
		t.Run("reject "+test.name, func(t *testing.T) {
			ops := autoUnlockOps{
				stopLND: func() error { return nil },
				unitStatus: func() (lndUnitStatus, error) {
					return test.status, nil
				},
			}
			if err := stopAndVerifyNoProcess(ops); err == nil {
				t.Fatalf("unsafe status accepted: %+v", test.status)
			}
		})
	}
}

func TestLoadedUnitRequiresNativeNotification(t *testing.T) {
	f := newAutoUnlockFixture(false)
	ops := f.ops()
	status, err := ops.unitStatus()
	if err != nil {
		t.Fatal(err)
	}
	status.serviceType = "simple"
	if err := verifyLoadedUnit(
		status, autoUnlockUnitPlain, "on-failure", false,
	); err == nil || !strings.Contains(err.Error(), "Type=simple") {
		t.Fatalf("non-notify service type error = %v", err)
	}
}
