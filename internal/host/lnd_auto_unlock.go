package host

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/lightningnetwork/lnd/lnrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/virtpriv/node/internal/autounlock"
	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/logger"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/system"
)

const (
	autoUnlockStartupTimeout       = 120 * time.Second
	autoUnlockPostconditionTimeout = 10 * time.Second
	autoUnlockPollInterval         = time.Second
	autoUnlockRPCTimeout           = 5 * time.Second
)

const lndVerificationDropIn = `[Service]
Restart=no
TimeoutStartSec=120
`

var errLNDInvocationChanged = errors.New(
	"LND invocation changed during process verification")

type autoUnlockUnit int

const (
	autoUnlockUnitUnknown autoUnlockUnit = iota
	autoUnlockUnitPlain
	autoUnlockUnitEnabled
)

type autoUnlockArtifacts struct {
	unit          autoUnlockUnit
	password      bool
	passwordStage bool
	verifyDrop    bool
}

type lndUnitStatus struct {
	invocationID     string
	mainPID          int
	controlPID       int
	serviceType      string
	activeState      string
	subState         string
	result           string
	execMainStatus   string
	restart          string
	fragmentPath     string
	dropInPaths      []string
	needDaemonReload string
	execStart        string
}

type invocationResult int

const (
	invocationUnknown invocationResult = iota
	invocationReady
	invocationExited
	invocationStartTimedOut
	invocationProofTimedOut
)

type autoUnlockOps struct {
	loadConfig       func() (*config.AppConfig, error)
	saveConfig       func(*config.AppConfig) error
	artifacts        func() (autoUnlockArtifacts, error)
	writeUnit        func(autoUnlockUnit) error
	writePassword    func(string) error
	removePassword   func() error
	writeVerifyDrop  func() error
	removeVerifyDrop func() error
	validateUnit     func() error
	daemonReload     func() error
	startLND         func() error
	stopLND          func() error
	unitStatus       func() (lndUnitStatus, error)
	processArgs      func(int) ([]string, error)
	walletState      func() (lnrpc.WalletState, error)
	now              func() time.Time
	sleep            func(time.Duration)
}

func repairRequired(stage string, cause ...error) autounlock.Result {
	if len(cause) > 0 && cause[0] != nil {
		logger.System("auto-unlock: %s: %v", stage, cause[0])
	} else {
		logger.System("auto-unlock: %s", stage)
	}
	return autounlock.Result{
		Outcome:    autounlock.RepairRequired,
		FailedStep: stage,
	}
}

func enableAutoUnlock(password string, ops autoUnlockOps) autounlock.Result {
	cfg, err := ops.loadConfig()
	if err != nil {
		return repairRequired("read node configuration", err)
	}
	if cfg.AutoUnlock {
		return repairRequired("require disabled starting state")
	}

	artifacts, err := ops.artifacts()
	if err != nil || artifacts.unit == autoUnlockUnitUnknown {
		return repairRequired("inspect disabled starting state", err)
	}

	// Restart=no is installed before touching either the candidate credential
	// or ExecStart. If this process or the host stops, the persistent drop-in
	// prevents an unverified password from becoming a restart loop.
	if err := ops.writeVerifyDrop(); err != nil {
		return repairRequired("install one-attempt restart policy", err)
	}
	if err := normalizeDisabledUnit(ops); err != nil {
		return repairRequired("restore disabled starting state", err)
	}
	if err := reloadAndVerifyUnit(ops, autoUnlockUnitPlain, "no", true); err != nil {
		return repairRequired("load disabled starting state", err)
	}

	before, err := ops.unitStatus()
	if err != nil {
		return repairRequired("observe current LND invocation", err)
	}
	if before.mainPID == 0 || verifyStableProcessArgs(ops, before, false) != nil {
		// An interrupted older enable can leave an auto-unlock process alive
		// while the still-false desired state has just been normalized on disk.
		// Converge that recognizable shape to a plain locked invocation before
		// testing the newly supplied password.
		if err := replaceWithLockedPlainInvocation(
			ops, before.invocationID,
		); err != nil {
			return repairRequired("restore disabled LND invocation", err)
		}
		before, err = ops.unitStatus()
		if err != nil {
			return repairRequired("observe restored LND invocation", err)
		}
	} else if err := removePasswordDurably(ops); err != nil {
		return repairRequired("remove stale wallet password", err)
	}
	if err := verifyDisabledDisk(ops); err != nil {
		return repairRequired("confirm disabled starting state", err)
	}

	runtimeTransitioned := false
	failWithRecoveryStage := func(
		outcome autounlock.Outcome, stage, recoveryStage, detail string,
		cause error,
	) autounlock.Result {
		if cause != nil {
			logger.System("auto-unlock: %s: %v", stage, cause)
		} else {
			logger.System("auto-unlock: %s", stage)
		}
		if err := restoreDisabled(
			ops, cfg, before, runtimeTransitioned,
		); err != nil {
			return repairRequired(recoveryStage,
				fmt.Errorf("recovery failed: %w", err))
		}
		return autounlock.Result{Outcome: outcome, Detail: detail}
	}
	fail := func(
		outcome autounlock.Outcome, stage, detail string, cause error,
	) autounlock.Result {
		return failWithRecoveryStage(
			outcome, stage, "automatic return to locked state", detail,
			cause,
		)
	}

	if err := ops.writePassword(password); err != nil {
		return fail(autounlock.VerificationFailed,
			"write candidate wallet password",
			"VPN could not store the password safely. The previous disabled setting was restored.", err)
	}
	if err := ops.writeUnit(autoUnlockUnitEnabled); err != nil {
		return fail(autounlock.VerificationFailed,
			"install auto-unlock unit",
			"VPN could not install auto-unlock safely. The previous disabled setting was restored.", err)
	}
	if err := ops.validateUnit(); err != nil {
		return fail(autounlock.VerificationFailed,
			"validate auto-unlock unit",
			"VPN could not validate the auto-unlock service. The previous disabled setting was restored.", err)
	}
	if err := reloadAndVerifyUnit(ops, autoUnlockUnitEnabled, "no", true); err != nil {
		return fail(autounlock.VerificationFailed,
			"load one-attempt auto-unlock unit",
			"VPN could not load auto-unlock safely. The previous disabled setting was restored.", err)
	}

	runtimeTransitioned = true
	transitionErr, invocation, verifyErr := stopStartAndVerifyInvocation(
		ops, before.invocationID, true,
		lnrpc.WalletState_RPC_ACTIVE,
	)
	if invocation == invocationExited {
		return failWithRecoveryStage(
			autounlock.VerificationFailed,
			"verify candidate wallet password",
			"automatic recovery after password rejection", "", verifyErr,
		)
	}
	if invocation == invocationStartTimedOut {
		return fail(autounlock.VerificationTimedOut,
			"wait for LND to become ready", "", verifyErr)
	}
	if transitionErr != nil || verifyErr != nil ||
		invocation != invocationReady {
		cause := verifyErr
		if transitionErr != nil {
			cause = transitionErr
		}
		return fail(autounlock.VerificationFailed,
			"verify candidate LND",
			"VPN could not verify auto-unlock because a system operation failed. LND has been returned to the locked state.", cause)
	}

	// The password has now been proved. Removing the override and reloading
	// changes only the policy PID 1 applies to a future failure; the verified
	// LND invocation stays online and is checked again below.
	if err := ops.removeVerifyDrop(); err != nil {
		return fail(autounlock.VerificationFailed,
			"remove one-attempt restart policy",
			"VPN verified the password but could not finish auto-unlock safely. LND has been returned to the locked state.", err)
	}
	if err := reloadAndVerifyUnit(ops, autoUnlockUnitEnabled, "on-failure", false); err != nil {
		return fail(autounlock.VerificationFailed,
			"restore normal restart policy",
			"VPN verified the password but could not finish auto-unlock safely. LND has been returned to the locked state.", err)
	}
	if err := verifySameReadyInvocation(ops, before.invocationID); err != nil {
		return fail(autounlock.VerificationFailed,
			"recheck verified LND invocation",
			"VPN could not prove that LND stayed ready. LND has been returned to the locked state.", err)
	}

	cfg.AutoUnlock = true
	if err := ops.saveConfig(cfg); err != nil {
		return fail(autounlock.VerificationFailed,
			"publish enabled setting",
			"VPN verified the password but could not publish auto-unlock safely. LND has been returned to the locked state.", err)
	}
	published, err := ops.loadConfig()
	if err != nil || !published.AutoUnlock {
		return fail(autounlock.VerificationFailed,
			"confirm enabled setting",
			"VPN could not confirm the auto-unlock setting. LND has been returned to the locked state.", err)
	}
	return autounlock.Result{Outcome: autounlock.Enabled}
}

func disableAutoUnlockTransition(ops autoUnlockOps) autounlock.Result {
	cfg, err := ops.loadConfig()
	if err != nil {
		return repairRequired("read node configuration", err)
	}
	if !cfg.AutoUnlock {
		return repairRequired("require enabled starting state")
	}
	artifacts, err := ops.artifacts()
	if err != nil || artifacts.unit == autoUnlockUnitUnknown {
		return repairRequired("inspect enabled starting state", err)
	}
	if !artifacts.password {
		// Password deletion is the irreversible commit point. This exact shape
		// is an interrupted disable after that point: finish publication rather
		// than attempting to recreate a credential VPN no longer has.
		if artifacts.unit != autoUnlockUnitPlain {
			return repairRequired("classify interrupted disable")
		}
		if err := finishDisabledAfterPasswordRemoval(ops, cfg); err != nil {
			return repairRequired("finish interrupted disable", err)
		}
		return autounlock.Result{Outcome: autounlock.Disabled}
	}

	if err := ops.writeVerifyDrop(); err != nil {
		return repairRequired("install one-attempt restart policy", err)
	}
	if err := ops.writeUnit(autoUnlockUnitPlain); err != nil {
		return restoreEnabledResult(ops, cfg, "install plain LND unit", err)
	}
	if err := ops.validateUnit(); err != nil {
		return restoreEnabledResult(ops, cfg, "validate plain LND unit", err)
	}
	if err := reloadAndVerifyUnit(ops, autoUnlockUnitPlain, "no", true); err != nil {
		return restoreEnabledResult(ops, cfg, "load plain LND unit", err)
	}

	before, err := ops.unitStatus()
	if err != nil {
		return restoreEnabledResult(ops, cfg, "observe current LND invocation", err)
	}
	transitionErr, invocation, verifyErr := stopStartAndVerifyInvocation(
		ops, before.invocationID, false,
		lnrpc.WalletState_LOCKED,
	)
	if transitionErr != nil || invocation != invocationReady || verifyErr != nil {
		cause := verifyErr
		if transitionErr != nil {
			cause = transitionErr
		}
		return restoreEnabledResult(ops, cfg, "prove locked LND invocation", cause)
	}

	if err := ops.removeVerifyDrop(); err != nil {
		return restoreEnabledResult(ops, cfg, "remove one-attempt restart policy", err)
	}
	if err := reloadAndVerifyUnit(ops, autoUnlockUnitPlain, "on-failure", false); err != nil {
		return restoreEnabledResult(ops, cfg, "restore normal restart policy", err)
	}
	if err := verifySameLockedInvocation(ops, before.invocationID); err != nil {
		return restoreEnabledResult(ops, cfg, "recheck locked LND invocation", err)
	}

	removeErr := ops.removePassword()
	afterRemoval, inspectErr := ops.artifacts()
	if inspectErr != nil {
		return repairRequired("confirm wallet password removal", inspectErr)
	}
	if afterRemoval.password {
		return restoreEnabledResult(ops, cfg, "remove wallet password", removeErr)
	}
	// An observed absence is past the irreversible commit point, but success
	// still requires durability. Calling remove again when the name is absent
	// retries the parent-directory sync without recreating credential data.
	durabilityErr := removeErr
	if removeErr != nil {
		durabilityErr = ops.removePassword()
	}
	if err := finishDisabledAfterPasswordRemoval(ops, cfg); err != nil {
		return repairRequired("publish disabled setting", err)
	}
	if durabilityErr != nil {
		return repairRequired("confirm durable wallet password removal", durabilityErr)
	}
	return autounlock.Result{Outcome: autounlock.Disabled}
}

func normalizeDisabledUnit(ops autoUnlockOps) error {
	if err := ops.writeUnit(autoUnlockUnitPlain); err != nil {
		return err
	}
	return ops.validateUnit()
}

func verifyDisabledDisk(ops autoUnlockOps) error {
	artifacts, err := ops.artifacts()
	if err != nil {
		return err
	}
	if artifacts.unit != autoUnlockUnitPlain || artifacts.password ||
		artifacts.passwordStage || !artifacts.verifyDrop {
		return errors.New("disabled disk state did not converge")
	}
	return nil
}

func removePasswordDurably(ops autoUnlockOps) error {
	removeErr := ops.removePassword()
	artifacts, inspectErr := ops.artifacts()
	if inspectErr != nil {
		return inspectErr
	}
	if artifacts.password || artifacts.passwordStage {
		if removeErr != nil {
			return removeErr
		}
		return errors.New("wallet password remains after removal")
	}
	if removeErr != nil {
		// Retry the parent-directory sync after an unlink that succeeded but
		// reported a durability error. This never recreates credential data.
		return ops.removePassword()
	}
	return nil
}

func replaceWithLockedPlainInvocation(
	ops autoUnlockOps, previousID string,
) error {
	if err := stopAndVerifyNoProcess(ops); err != nil {
		return err
	}
	removeErr := removePasswordDurably(ops)
	startErr, outcome, verifyErr := startAndVerifyInvocation(
		ops, previousID, false,
		lnrpc.WalletState_LOCKED,
	)
	if startErr != nil || verifyErr != nil || outcome != invocationReady {
		return errors.Join(removeErr, startErr, verifyErr,
			errors.New("could not restore locked LND invocation"))
	}
	return removeErr
}

func restoreDisabled(
	ops autoUnlockOps, cfg *config.AppConfig, before lndUnitStatus,
	runtimeTransitioned bool,
) error {
	var lockedPreviousID string
	if err := ops.writeVerifyDrop(); err != nil {
		return err
	}
	if err := normalizeDisabledUnit(ops); err != nil {
		return err
	}
	if err := reloadAndVerifyUnit(ops, autoUnlockUnitPlain, "no", true); err != nil {
		return err
	}
	if runtimeTransitioned {
		current, err := ops.unitStatus()
		if err != nil {
			return err
		}
		lockedPreviousID = current.invocationID
		if err := replaceWithLockedPlainInvocation(
			ops, current.invocationID,
		); err != nil {
			return err
		}
	} else if before.mainPID > 0 {
		current, err := ops.unitStatus()
		if err != nil || current.invocationID != before.invocationID {
			return errors.New("original LND invocation changed")
		}
		if err := verifyStableProcessArgs(ops, current, false); err != nil {
			return err
		}
		if err := removePasswordDurably(ops); err != nil {
			return err
		}
	}
	if err := ops.removeVerifyDrop(); err != nil {
		return err
	}
	if err := reloadAndVerifyUnit(ops, autoUnlockUnitPlain, "on-failure", false); err != nil {
		return err
	}
	if runtimeTransitioned {
		if err := verifySameLockedInvocation(ops, lockedPreviousID); err != nil {
			return err
		}
	}
	cfg.AutoUnlock = false
	if err := ops.saveConfig(cfg); err != nil {
		observed, loadErr := ops.loadConfig()
		if loadErr != nil || observed.AutoUnlock {
			return err
		}
	}
	final, err := ops.artifacts()
	if err != nil || final.unit != autoUnlockUnitPlain || final.password ||
		final.passwordStage || final.verifyDrop {
		return errors.New("disabled state did not pass final inspection")
	}
	return nil
}

func restoreEnabledResult(
	ops autoUnlockOps, cfg *config.AppConfig, failedStage string, cause error,
) autounlock.Result {
	if cause != nil {
		logger.System("auto-unlock: %s: %v", failedStage, cause)
	} else {
		logger.System("auto-unlock: %s", failedStage)
	}
	if err := restoreEnabled(ops, cfg); err != nil {
		return repairRequired(failedStage,
			fmt.Errorf("recovery failed: %w", err))
	}
	return autounlock.Result{Outcome: autounlock.StillEnabled}
}

func restoreEnabled(ops autoUnlockOps, cfg *config.AppConfig) error {
	artifacts, err := ops.artifacts()
	if err != nil || !artifacts.password {
		return errors.New("stored wallet password is unavailable")
	}
	if err := ops.writeVerifyDrop(); err != nil {
		return err
	}
	if err := ops.writeUnit(autoUnlockUnitEnabled); err != nil {
		return err
	}
	if err := ops.validateUnit(); err != nil {
		return err
	}
	if err := reloadAndVerifyUnit(ops, autoUnlockUnitEnabled, "no", true); err != nil {
		return err
	}
	before, err := ops.unitStatus()
	if err != nil {
		return err
	}
	transitionErr, outcome, verifyErr := stopStartAndVerifyInvocation(
		ops, before.invocationID, true,
		lnrpc.WalletState_RPC_ACTIVE,
	)
	if transitionErr != nil || verifyErr != nil || outcome != invocationReady {
		return errors.Join(transitionErr, verifyErr,
			errors.New("previous enabled state was not restored"))
	}
	if err := ops.removeVerifyDrop(); err != nil {
		return err
	}
	if err := reloadAndVerifyUnit(ops, autoUnlockUnitEnabled, "on-failure", false); err != nil {
		return err
	}
	if err := verifySameReadyInvocation(ops, before.invocationID); err != nil {
		return err
	}
	cfg.AutoUnlock = true
	if err := ops.saveConfig(cfg); err != nil {
		observed, loadErr := ops.loadConfig()
		if loadErr != nil || !observed.AutoUnlock {
			return err
		}
	}
	return nil
}

func finishDisabledAfterPasswordRemoval(
	ops autoUnlockOps, cfg *config.AppConfig,
) error {
	if err := ops.writeVerifyDrop(); err != nil {
		return err
	}
	if err := ops.writeUnit(autoUnlockUnitPlain); err != nil {
		return err
	}
	if err := ops.validateUnit(); err != nil {
		return err
	}
	if err := reloadAndVerifyUnit(ops, autoUnlockUnitPlain, "no", true); err != nil {
		return err
	}
	if err := ensureLockedRuntime(ops); err != nil {
		return err
	}
	if err := ops.removeVerifyDrop(); err != nil {
		return err
	}
	if err := reloadAndVerifyUnit(ops, autoUnlockUnitPlain, "on-failure", false); err != nil {
		return err
	}
	if err := verifyCurrentLockedInvocation(ops); err != nil {
		return err
	}
	cfg.AutoUnlock = false
	if err := ops.saveConfig(cfg); err != nil {
		observed, loadErr := ops.loadConfig()
		if loadErr != nil || observed.AutoUnlock {
			return err
		}
	}
	final, err := ops.artifacts()
	if err != nil {
		return err
	}
	if final.unit != autoUnlockUnitPlain || final.password ||
		final.passwordStage || final.verifyDrop {
		return errors.New("disabled state did not pass final inspection")
	}
	return nil
}

func ensureLockedRuntime(ops autoUnlockOps) error {
	status, err := ops.unitStatus()
	if err != nil {
		return err
	}
	if status.mainPID > 0 {
		if err := verifyStableProcessArgs(ops, status, false); err == nil {
			state, stateErr := ops.walletState()
			if stateErr == nil && state == lnrpc.WalletState_LOCKED {
				return nil
			}
		}
	}
	transitionErr, outcome, verifyErr := stopStartAndVerifyInvocation(
		ops, status.invocationID, false,
		lnrpc.WalletState_LOCKED,
	)
	if transitionErr != nil || verifyErr != nil || outcome != invocationReady {
		return errors.Join(transitionErr, verifyErr,
			errors.New("could not prove a locked LND invocation"))
	}
	return nil
}

func reloadAndVerifyUnit(
	ops autoUnlockOps, unit autoUnlockUnit, restart string, drop bool,
) error {
	if err := ops.daemonReload(); err != nil {
		return err
	}
	status, err := ops.unitStatus()
	if err != nil {
		return err
	}
	return verifyLoadedUnit(status, unit, restart, drop)
}

func verifyLoadedUnit(
	status lndUnitStatus, unit autoUnlockUnit, restart string, drop bool,
) error {
	if status.fragmentPath != paths.LNDService {
		return fmt.Errorf("loaded fragment is %q", status.fragmentPath)
	}
	if status.needDaemonReload != "no" {
		return fmt.Errorf("systemd still requires daemon-reload")
	}
	if status.serviceType != "notify" {
		return fmt.Errorf("loaded Type=%s, want notify", status.serviceType)
	}
	if status.restart != restart {
		return fmt.Errorf("loaded Restart=%s, want %s", status.restart, restart)
	}
	wantDrops := []string(nil)
	if drop {
		wantDrops = []string{paths.LNDVerificationDropIn}
	}
	if !equalStrings(status.dropInPaths, wantDrops) {
		return fmt.Errorf("loaded drop-ins are %q", status.dropInPaths)
	}
	if !loadedExecMatches(status.execStart, unit == autoUnlockUnitEnabled) {
		return errors.New("loaded ExecStart is not the exact VPN LND command")
	}
	return nil
}

// stopStartAndVerifyInvocation keeps graceful shutdown outside the candidate
// startup window. systemd independently bounds stop with TimeoutStopSec and
// start with the verification drop-in's TimeoutStartSec.
func stopStartAndVerifyInvocation(
	ops autoUnlockOps, previousID string,
	withUnlock bool, wantState lnrpc.WalletState,
) (error, invocationResult, error) {
	if err := stopAndVerifyNoProcess(ops); err != nil {
		return err, invocationUnknown, nil
	}
	return startAndVerifyInvocation(
		ops, previousID, withUnlock, wantState)
}

// stopAndVerifyNoProcess accepts systemd's two quiescent unit states. An
// inactive unit stopped normally; a failed unit is also inactive but retains
// the unsuccessful result for diagnosis. Restart=no is required so a failed
// candidate cannot enter an automatic restart transition between samples.
// Neither state is sufficient on its own: both the service's main process and
// any systemd control process must also be absent before credential removal.
func stopAndVerifyNoProcess(ops autoUnlockOps) error {
	if err := ops.stopLND(); err != nil {
		return err
	}
	status, err := ops.unitStatus()
	if err != nil {
		return err
	}
	if status.restart != "no" {
		return fmt.Errorf(
			"LND stop proof has Restart=%s, want no", status.restart)
	}
	if status.mainPID != 0 || status.controlPID != 0 {
		return fmt.Errorf(
			"LND still has a process after stop: MainPID=%d ControlPID=%d",
			status.mainPID, status.controlPID)
	}
	if status.activeState != "inactive" && status.activeState != "failed" {
		return fmt.Errorf(
			"LND is not process-free after stop: ActiveState=%s",
			status.activeState)
	}
	return nil
}

// startAndVerifyInvocation uses two deliberate, non-overlapping bounds. The
// loaded Type=notify unit and TimeoutStartSec=120 bound native LND startup
// until it reports RPC_ACTIVE or SERVER_ACTIVE. After systemctl start returns,
// VPN gets a short independent window to bind the process to the loaded unit
// and prove that native wallet-state postcondition. Network-profile validation
// is an installer concern; tying password verification to a chain-dependent
// RPC would make a valid RPC_ACTIVE state depend on blockchain synchronization.
func startAndVerifyInvocation(
	ops autoUnlockOps, previousID string, withUnlock bool,
	wantState lnrpc.WalletState,
) (error, invocationResult, error) {
	started := ops.now()
	startErr := ops.startLND()
	startupElapsed := ops.now().Sub(started)
	if startupElapsed > autoUnlockStartupTimeout {
		return startErr, invocationStartTimedOut, fmt.Errorf(
			"LND startup returned after %s, limit %s",
			startupElapsed, autoUnlockStartupTimeout)
	}
	outcome, verifyErr := waitForInvocation(
		ops, previousID, withUnlock, wantState,
		autoUnlockPostconditionTimeout)
	return startErr, outcome, verifyErr
}

func waitForInvocation(
	ops autoUnlockOps, previousID string, withUnlock bool,
	wantState lnrpc.WalletState, timeout time.Duration,
) (invocationResult, error) {
	deadline := ops.now().Add(timeout)
	var lastErr error
	for {
		status, statusErr := ops.unitStatus()
		if statusErr != nil {
			lastErr = statusErr
		}
		if statusErr == nil && status.invocationID != "" &&
			status.invocationID != previousID {
			if status.mainPID == 0 {
				if status.result == "timeout" {
					return invocationStartTimedOut, fmt.Errorf(
						"new LND invocation timed out: active=%s sub=%s result=%s status=%s",
						status.activeState, status.subState,
						status.result, status.execMainStatus)
				}
				if status.activeState == "failed" ||
					status.activeState == "inactive" {
					return invocationExited, fmt.Errorf(
						"new LND invocation exited: active=%s sub=%s result=%s status=%s",
						status.activeState, status.subState,
						status.result, status.execMainStatus)
				}
			} else {
				if err := verifyStableProcessArgs(ops, status, withUnlock); err != nil {
					if errors.Is(err, errLNDInvocationChanged) {
						return invocationExited, err
					} else {
						return invocationReady, err
					}
				} else {
					state, err := ops.walletState()
					if err != nil {
						lastErr = err
					}
					if err == nil && walletStateMatches(state, wantState) {
						return invocationReady, nil
					}
				}
			}
		}
		if !ops.now().Before(deadline) {
			return invocationProofTimedOut, lastErr
		}
		ops.sleep(autoUnlockPollInterval)
	}
}

func verifySameReadyInvocation(
	ops autoUnlockOps, previousID string,
) error {
	status, err := ops.unitStatus()
	if err != nil {
		return err
	}
	if status.invocationID == "" || status.invocationID == previousID || status.mainPID == 0 {
		return errors.New("verified LND invocation is not running")
	}
	if err := verifyStableProcessArgs(ops, status, true); err != nil {
		return err
	}
	state, err := ops.walletState()
	if err != nil || !walletStateMatches(state, lnrpc.WalletState_RPC_ACTIVE) {
		return errors.New(
			"verified LND invocation has not reached RPC_ACTIVE or SERVER_ACTIVE")
	}
	return nil
}

// walletStateMatches accepts LND's RPC_ACTIVE or SERVER_ACTIVE startup states.
// LND always enters RPC_ACTIVE first, but an already-synchronized backend can
// advance to SERVER_ACTIVE between observations. UNLOCKED is deliberately
// insufficient because LND has not started its RPC server yet.
func walletStateMatches(got, want lnrpc.WalletState) bool {
	if want == lnrpc.WalletState_RPC_ACTIVE {
		return got == lnrpc.WalletState_RPC_ACTIVE ||
			got == lnrpc.WalletState_SERVER_ACTIVE
	}
	return got == want
}

func verifySameLockedInvocation(ops autoUnlockOps, previousID string) error {
	status, err := ops.unitStatus()
	if err != nil {
		return err
	}
	if status.invocationID == "" || status.invocationID == previousID || status.mainPID == 0 {
		return errors.New("locked LND invocation is not running")
	}
	return verifyLockedStatus(ops, status)
}

func verifyCurrentLockedInvocation(ops autoUnlockOps) error {
	status, err := ops.unitStatus()
	if err != nil {
		return err
	}
	if status.invocationID == "" || status.mainPID == 0 {
		return errors.New("locked LND invocation is not running")
	}
	return verifyLockedStatus(ops, status)
}

func verifyLockedStatus(ops autoUnlockOps, status lndUnitStatus) error {
	if err := verifyStableProcessArgs(ops, status, false); err != nil {
		return err
	}
	state, err := ops.walletState()
	if err != nil || state != lnrpc.WalletState_LOCKED {
		return errors.New("LND did not remain LOCKED")
	}
	return nil
}

// verifyStableProcessArgs binds the inspected /proc argument vector to one
// systemd invocation. Type=notify guarantees that the service reached LND's
// native RPC-readiness notification before a successful start returns; the
// second status sample proves that the PID did not exit or change while its
// arguments were being inspected.
func verifyStableProcessArgs(
	ops autoUnlockOps, before lndUnitStatus, withUnlock bool,
) error {
	if before.invocationID == "" || before.mainPID <= 0 ||
		before.controlPID != 0 ||
		before.activeState != "active" || before.subState != "running" {
		return errors.New("LND invocation is not stably running")
	}
	args, processErr := ops.processArgs(before.mainPID)
	after, statusErr := ops.unitStatus()
	if statusErr != nil {
		return statusErr
	}
	if after.invocationID != before.invocationID ||
		after.mainPID != before.mainPID || after.controlPID != 0 ||
		after.activeState != "active" ||
		after.subState != "running" {
		return errLNDInvocationChanged
	}
	if processErr != nil {
		return processErr
	}
	if !equalStrings(args, expectedLNDArgs(withUnlock)) {
		return errors.New("LND process arguments do not match the loaded unit")
	}
	return nil
}

func expectedLNDArgs(withUnlock bool) []string {
	args := []string{"/usr/local/bin/lnd", "--configfile=/etc/lnd/lnd.conf"}
	if withUnlock {
		args = append(args, "--wallet-unlock-password-file="+paths.LNDWalletPassword)
	}
	return args
}

func loadedExecMatches(raw string, withUnlock bool) bool {
	const prefix = "argv[]="
	start := strings.Index(raw, prefix)
	if start < 0 {
		return false
	}
	argv := raw[start+len(prefix):]
	if end := strings.Index(argv, " ;"); end >= 0 {
		argv = argv[:end]
	} else if end := strings.IndexByte(argv, '}'); end >= 0 {
		argv = strings.TrimSpace(argv[:end])
	}
	return argv == strings.Join(expectedLNDArgs(withUnlock), " ")
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ── Production operations ────────────────────────────────────────────────

type autoUnlockFS struct {
	lndUID int
	lndGID int
}

func productionAutoUnlockOps() (autoUnlockOps, error) {
	identity, err := user.Lookup("lnd")
	if err != nil {
		return autoUnlockOps{}, fmt.Errorf("resolve lnd user: %w", err)
	}
	uid, err := strconv.Atoi(identity.Uid)
	if err != nil {
		return autoUnlockOps{}, fmt.Errorf("parse lnd uid: %w", err)
	}
	gid, err := strconv.Atoi(identity.Gid)
	if err != nil {
		return autoUnlockOps{}, fmt.Errorf("parse lnd gid: %w", err)
	}
	fs := &autoUnlockFS{lndUID: uid, lndGID: gid}
	return autoUnlockOps{
		loadConfig:       config.Load,
		saveConfig:       config.Save,
		artifacts:        fs.inspectArtifacts,
		writeUnit:        fs.writeUnit,
		writePassword:    fs.writePassword,
		removePassword:   fs.removePassword,
		writeVerifyDrop:  fs.writeVerifyDrop,
		removeVerifyDrop: fs.removeVerifyDrop,
		validateUnit:     validateInstalledLNDUnit,
		daemonReload: func() error {
			return system.RunRoot("systemctl", "daemon-reload")
		},
		startLND: func() error {
			return system.RunRoot("systemctl", "start", "lnd.service")
		},
		stopLND: func() error {
			return system.RunRoot("systemctl", "stop", "lnd.service")
		},
		unitStatus:  readLNDUnitStatus,
		processArgs: readProcessArgs,
		walletState: ReadLNDWalletState,
		now:         time.Now,
		sleep:       time.Sleep,
	}, nil
}

func (fs *autoUnlockFS) inspectArtifacts() (autoUnlockArtifacts, error) {
	var out autoUnlockArtifacts
	unit, exists, err := readExactFile(paths.LNDService, 0o644, 0, 0)
	if err != nil {
		return out, fmt.Errorf("inspect LND unit: %w", err)
	}
	if !exists {
		return out, errors.New("LND unit is missing")
	}
	switch string(unit) {
	case LNDServiceUnit("lnd", false):
		out.unit = autoUnlockUnitPlain
	case LNDServiceUnit("lnd", true):
		out.unit = autoUnlockUnitEnabled
	default:
		out.unit = autoUnlockUnitUnknown
	}
	out.password, err = exactFileExists(
		paths.LNDWalletPassword, 0o400, fs.lndUID, fs.lndGID)
	if err != nil {
		return out, fmt.Errorf("inspect wallet password: %w", err)
	}
	out.passwordStage, err = passwordStageExists(fs.lndUID, fs.lndGID)
	if err != nil {
		return out, fmt.Errorf("inspect staged wallet password: %w", err)
	}

	if err := validateOptionalDropInDir(); err != nil {
		return out, err
	}
	drop, exists, err := readExactFile(
		paths.LNDVerificationDropIn, 0o644, 0, 0)
	if err != nil {
		return out, fmt.Errorf("inspect verification drop-in: %w", err)
	}
	if exists {
		if string(drop) != lndVerificationDropIn {
			return out, errors.New("verification drop-in has unexpected content")
		}
		out.verifyDrop = true
	}
	return out, nil
}

func (fs *autoUnlockFS) writeUnit(unit autoUnlockUnit) error {
	var content string
	switch unit {
	case autoUnlockUnitPlain:
		content = LNDServiceUnit("lnd", false)
	case autoUnlockUnitEnabled:
		content = LNDServiceUnit("lnd", true)
	default:
		return errors.New("refusing to write unknown LND unit variant")
	}
	if err := validateExactDir(filepath.Dir(paths.LNDService), 0o755, 0, 0); err != nil {
		return err
	}
	return replaceExactFile(paths.LNDService, []byte(content), 0o644, 0, 0)
}

func (fs *autoUnlockFS) writePassword(password string) error {
	if password == "" || strings.ContainsAny(password, "\r\n") {
		return errors.New("wallet password has an invalid shape")
	}
	if err := validateExactDir(
		filepath.Dir(paths.LNDWalletPassword), 0o750,
		fs.lndUID, fs.lndGID,
	); err != nil {
		return err
	}
	return publishWalletPassword([]byte(password), fs.lndUID, fs.lndGID)
}

func (fs *autoUnlockFS) removePassword() error {
	canonicalErr := removeExactFile(
		paths.LNDWalletPassword, 0o400, fs.lndUID, fs.lndGID)
	stageErr := removeWalletPasswordStage(fs.lndUID, fs.lndGID)
	if canonicalErr != nil {
		return canonicalErr
	}
	return stageErr
}

func publishWalletPassword(data []byte, uid, gid int) error {
	return publishWalletPasswordAt(
		paths.LNDWalletPassword, paths.LNDWalletPasswordStage,
		data, uid, gid,
	)
}

func publishWalletPasswordAt(
	canonical, stage string, data []byte, uid, gid int,
) error {
	if exists, err := passwordStageExistsAt(stage, uid, gid); err != nil {
		return err
	} else if exists {
		return errors.New("staged wallet password already exists")
	}
	if info, err := os.Lstat(canonical); err == nil {
		if err := validateFileInfo(
			canonical, info, false, 0o400, uid, gid,
		); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	f, err := os.OpenFile(stage, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create staged wallet password: %w", err)
	}
	cleanup := func() {
		f.Close()
		if os.Remove(stage) == nil {
			_ = syncDir(filepath.Dir(stage))
		}
	}
	failed := true
	defer func() {
		if failed {
			cleanup()
		}
	}()
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write staged wallet password: %w", err)
	}
	if err := f.Chmod(0o400); err != nil {
		return fmt.Errorf("protect staged wallet password: %w", err)
	}
	if err := f.Chown(uid, gid); err != nil {
		return fmt.Errorf("own staged wallet password: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync staged wallet password: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close staged wallet password: %w", err)
	}
	if err := os.Rename(stage, canonical); err != nil {
		return fmt.Errorf("publish wallet password: %w", err)
	}
	if err := syncDir(filepath.Dir(canonical)); err != nil {
		return err
	}
	failed = false
	return nil
}

func passwordStageExists(uid, gid int) (bool, error) {
	return passwordStageExistsAt(paths.LNDWalletPasswordStage, uid, gid)
}

func passwordStageExistsAt(path string, uid, gid int) (bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, errors.New("wallet-password stage is not a regular file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false, errors.New("wallet-password stage ownership is unavailable")
	}
	mode := info.Mode().Perm()
	rootPhase := int(stat.Uid) == 0 && int(stat.Gid) == 0 &&
		(mode == 0o600 || mode == 0o400)
	lndPhase := int(stat.Uid) == uid && int(stat.Gid) == gid && mode == 0o400
	if !rootPhase && !lndPhase {
		return false, fmt.Errorf(
			"wallet-password stage metadata is %d:%d %04o",
			stat.Uid, stat.Gid, mode)
	}
	return true, nil
}

func removeWalletPasswordStage(uid, gid int) error {
	return removeWalletPasswordStageAt(paths.LNDWalletPasswordStage, uid, gid)
}

func removeWalletPasswordStageAt(path string, uid, gid int) error {
	exists, err := passwordStageExistsAt(path, uid, gid)
	if err != nil {
		return err
	}
	if !exists {
		return syncDir(filepath.Dir(path))
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func (fs *autoUnlockFS) writeVerifyDrop() error {
	parent := filepath.Dir(paths.LNDServiceDropInDir)
	if err := validateExactDir(parent, 0o755, 0, 0); err != nil {
		return err
	}
	if _, err := os.Lstat(paths.LNDServiceDropInDir); os.IsNotExist(err) {
		if err := os.Mkdir(paths.LNDServiceDropInDir, 0o755); err != nil {
			return fmt.Errorf("create LND drop-in directory: %w", err)
		}
		if err := os.Chmod(paths.LNDServiceDropInDir, 0o755); err != nil {
			return fmt.Errorf("set LND drop-in directory mode: %w", err)
		}
		if err := os.Chown(paths.LNDServiceDropInDir, 0, 0); err != nil {
			return fmt.Errorf("own LND drop-in directory: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("inspect LND drop-in directory: %w", err)
	}
	if err := validateExactDir(paths.LNDServiceDropInDir, 0o755, 0, 0); err != nil {
		return err
	}
	if err := syncDir(parent); err != nil {
		return err
	}
	return replaceExactFile(
		paths.LNDVerificationDropIn, []byte(lndVerificationDropIn),
		0o644, 0, 0,
	)
}

func (fs *autoUnlockFS) removeVerifyDrop() error {
	if err := validateOptionalDropInDir(); err != nil {
		return err
	}
	if err := removeExactFile(paths.LNDVerificationDropIn, 0o644, 0, 0); err != nil {
		return err
	}
	if _, err := os.Lstat(paths.LNDServiceDropInDir); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if err := os.Remove(paths.LNDServiceDropInDir); err != nil {
		return fmt.Errorf("remove LND drop-in directory: %w", err)
	}
	return syncDir(filepath.Dir(paths.LNDServiceDropInDir))
}

func validateOptionalDropInDir() error {
	info, err := os.Lstat(paths.LNDServiceDropInDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect LND drop-in directory: %w", err)
	}
	if err := validateFileInfo(paths.LNDServiceDropInDir, info, true, 0o755, 0, 0); err != nil {
		return err
	}
	entries, err := os.ReadDir(paths.LNDServiceDropInDir)
	if err != nil {
		return fmt.Errorf("read LND drop-in directory: %w", err)
	}
	for _, entry := range entries {
		if entry.Name() != filepath.Base(paths.LNDVerificationDropIn) {
			return fmt.Errorf("unexpected LND service drop-in %q", entry.Name())
		}
	}
	return nil
}

func validateInstalledLNDUnit() error {
	out, err := system.RunRootCombinedOutput(
		"systemd-analyze", "verify", paths.LNDService)
	if err != nil {
		return fmt.Errorf("systemd rejected LND unit: %s: %w",
			strings.TrimSpace(out), err)
	}
	return nil
}

func readLNDUnitStatus() (lndUnitStatus, error) {
	properties := []string{
		"InvocationID", "MainPID", "ControlPID", "Type", "ActiveState",
		"SubState", "Result", "ExecMainStatus", "Restart", "FragmentPath", "DropInPaths",
		"NeedDaemonReload", "ExecStart",
	}
	args := []string{"show", "lnd.service", "--no-pager"}
	for _, property := range properties {
		args = append(args, "--property="+property)
	}
	out, err := system.RunRootOutput("systemctl", args...)
	if err != nil {
		return lndUnitStatus{}, err
	}
	values := make(map[string]string, len(properties))
	for _, line := range strings.Split(out, "\n") {
		key, value, found := strings.Cut(line, "=")
		if found {
			values[key] = value
		}
	}
	pid, err := strconv.Atoi(values["MainPID"])
	if err != nil {
		return lndUnitStatus{}, fmt.Errorf("parse LND MainPID %q: %w", values["MainPID"], err)
	}
	controlPID, err := strconv.Atoi(values["ControlPID"])
	if err != nil {
		return lndUnitStatus{}, fmt.Errorf(
			"parse LND ControlPID %q: %w", values["ControlPID"], err)
	}
	drops := []string(nil)
	if values["DropInPaths"] != "" {
		drops = strings.Fields(values["DropInPaths"])
	}
	return lndUnitStatus{
		invocationID: values["InvocationID"], mainPID: pid,
		controlPID:  controlPID,
		serviceType: values["Type"],
		activeState: values["ActiveState"], subState: values["SubState"],
		result: values["Result"], execMainStatus: values["ExecMainStatus"],
		restart: values["Restart"], fragmentPath: values["FragmentPath"],
		dropInPaths: drops, needDaemonReload: values["NeedDaemonReload"],
		execStart: values["ExecStart"],
	}, nil
}

func readProcessArgs(pid int) ([]string, error) {
	if pid <= 0 {
		return nil, errors.New("LND has no main process")
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return nil, fmt.Errorf("read LND process arguments: %w", err)
	}
	parts := strings.Split(string(data), "\x00")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts, nil
}

// ReadLNDWalletState uses the root-owned TLS certificate for a bounded State RPC.
func ReadLNDWalletState() (lnrpc.WalletState, error) {
	conn, err := directLNDConn()
	if err != nil {
		return lnrpc.WalletState_WAITING_TO_START, err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), autoUnlockRPCTimeout)
	defer cancel()
	resp, err := lnrpc.NewStateClient(conn).GetState(ctx, &lnrpc.GetStateRequest{})
	if err != nil {
		return lnrpc.WalletState_WAITING_TO_START, err
	}
	return resp.GetState(), nil
}

func directLNDConn() (*grpc.ClientConn, error) {
	certData, err := os.ReadFile(paths.LNDTLSCert)
	if err != nil {
		return nil, fmt.Errorf("read LND TLS certificate: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certData) {
		return nil, errors.New("parse LND TLS certificate")
	}
	return grpc.NewClient(paths.LNDGRPCEndpoint,
		grpc.WithTransportCredentials(credentials.NewClientTLSFromCert(pool, "")))
}

func readExactFile(
	path string, mode os.FileMode, uid, gid int,
) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if err := validateFileInfo(path, info, false, mode, uid, gid); err != nil {
		return nil, false, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func exactFileExists(path string, mode os.FileMode, uid, gid int) (bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := validateFileInfo(path, info, false, mode, uid, gid); err != nil {
		return false, err
	}
	return true, nil
}

func validateExactDir(path string, mode os.FileMode, uid, gid int) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect directory %s: %w", path, err)
	}
	return validateFileInfo(path, info, true, mode, uid, gid)
}

func validateFileInfo(
	path string, info os.FileInfo, wantDir bool,
	mode os.FileMode, uid, gid int,
) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink", path)
	}
	if wantDir && !info.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	if !wantDir && !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	if info.Mode().Perm() != mode {
		return fmt.Errorf("%s mode is %04o, want %04o", path, info.Mode().Perm(), mode)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%s ownership is unavailable", path)
	}
	if int(stat.Uid) != uid || int(stat.Gid) != gid {
		return fmt.Errorf("%s owner is %d:%d, want %d:%d",
			path, stat.Uid, stat.Gid, uid, gid)
	}
	return nil
}

func replaceExactFile(
	path string, data []byte, mode os.FileMode, uid, gid int,
) error {
	if info, err := os.Lstat(path); err == nil {
		if err := validateFileInfo(path, info, false, mode, uid, gid); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary %s: %w", path, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	fail := func(stage string, stageErr error) error {
		tmp.Close()
		return fmt.Errorf("%s %s: %w", stage, path, stageErr)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail("write", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		return fail("chmod", err)
	}
	if err := tmp.Chown(uid, gid); err != nil {
		return fail("chown", err)
	}
	if err := tmp.Sync(); err != nil {
		return fail("sync", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary %s: %w", path, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("publish %s: %w", path, err)
	}
	return syncDir(dir)
}

func removeExactFile(path string, mode os.FileMode, uid, gid int) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		// Absence is only a durable fact after the containing directory has
		// been synchronized. This also lets a caller retry a sync that failed
		// after an earlier successful unlink without recreating the file.
		return syncDir(filepath.Dir(path))
	}
	if err != nil {
		return err
	}
	if err := validateFileInfo(path, info, false, mode, uid, gid); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	if err := syncDir(filepath.Dir(path)); err != nil {
		return err
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		if err == nil {
			return fmt.Errorf("%s still exists after removal", path)
		}
		return err
	}
	return nil
}

func syncDir(path string) error {
	dir, err := os.Open(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("open directory %s for sync: %w", path, err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync directory %s: %w", path, err)
	}
	return nil
}

// SetupAutoUnlock runs the verified transition independently of the observing TUI.
func SetupAutoUnlock(password autounlock.Password) (autounlock.Result, error) {
	if os.Geteuid() != 0 {
		return autounlock.Result{}, errors.New("auto-unlock changes require the root helper")
	}
	if password.Text() == "" {
		return autounlock.Result{}, errors.New("wallet password was not validated")
	}
	ops, err := productionAutoUnlockOps()
	if err != nil {
		return repairRequired("initialize auto-unlock operation", err), nil
	}
	return enableAutoUnlock(password.Text(), ops), nil
}

// DisableAutoUnlock proves a locked invocation before durably removing the secret.
func DisableAutoUnlock() (autounlock.Result, error) {
	if os.Geteuid() != 0 {
		return autounlock.Result{}, errors.New("auto-unlock changes require the root helper")
	}
	ops, err := productionAutoUnlockOps()
	if err != nil {
		return repairRequired("initialize auto-unlock operation", err), nil
	}
	return disableAutoUnlockTransition(ops), nil
}
