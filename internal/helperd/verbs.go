// internal/helperd/verbs.go

package helperd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/virtualprivatenode/vpn/internal/accountaccess"
	"github.com/virtualprivatenode/vpn/internal/autounlock"
	"github.com/virtualprivatenode/vpn/internal/config"
	"github.com/virtualprivatenode/vpn/internal/helper"
	"github.com/virtualprivatenode/vpn/internal/host"
	"github.com/virtualprivatenode/vpn/internal/p2p"
	"github.com/virtualprivatenode/vpn/internal/paths"
	"github.com/virtualprivatenode/vpn/internal/release"
	"github.com/virtualprivatenode/vpn/internal/servicecontrol"
	"github.com/virtualprivatenode/vpn/internal/system"
	"github.com/virtualprivatenode/vpn/internal/update"
)

// ── The verb menu ────────────────────────────────────────
//
// Each verb: a typed parameter struct (decoded strictly),
// validation against closed sets, the operation itself, a
// postcondition where the outcome is checkable, and re-staging
// of every board fact the operation invalidated (the freshness
// matrix in matrix.go drives that last part — handlers call
// restage(verb) rather than remembering individual files).
//
// Deadlines bound the connection's socket I/O for one verb.
// They are sized to the operation's legitimate worst case —
// bitcoind alone is allowed 10 minutes to stop, a package
// upgrade can take most of half an hour over Tor. A deadline
// alone cannot cut short a wedged SUBPROCESS (nothing does
// socket I/O while one runs); the bounds there live with the
// subprocesses themselves — download timeouts and retry caps
// (system.doDownload), apt's Acquire timeout configuration,
// and systemd's per-unit stop timeouts.

type verbDef struct {
	deadline time.Duration
	handler  func(ctx *verbCtx, params json.RawMessage) (any, error)
}

var verbs = map[string]verbDef{
	helper.VerbReadAccounts: {time.Minute, verbReadAccounts},
	helper.VerbReadAccount:  {time.Minute, verbReadAccount},
	// LND uses its upstream readiness notification and permits up to 20
	// minutes for an ordinary start. Keep the helper connection above the
	// longest supported service start plus its graceful-stop allowance.
	helper.VerbServiceAction: {30 * time.Minute, verbServiceAction},
	helper.VerbReboot:        {1 * time.Minute, verbReboot},
	helper.VerbDirSize:       {2 * time.Minute, verbDirSize},
	// LND permits a graceful stop to take up to five minutes. A failed
	// transition can require a second stop/start recovery, so the helper
	// connection must outlive both bounded systemd transactions.
	helper.VerbStageWalletPassword:  {20 * time.Minute, verbStageWalletPassword},
	helper.VerbRemoveWalletPassword: {20 * time.Minute, verbRemoveWalletPassword},
	helper.VerbStageLNDCredentials:  {3 * time.Minute, verbStageLNDCredentials},
	helper.VerbStageLNDMacaroon:     {1 * time.Minute, verbStageLNDMacaroon},
	helper.VerbRebuildSSHConfig:     {3 * time.Minute, verbRebuildSSHConfig},
	helper.VerbPackageUpdate:        {30 * time.Minute, verbPackageUpdate},
	helper.VerbSelfUpdate:           {15 * time.Minute, verbSelfUpdate},
	helper.VerbUpgradeP2PToHybrid:   {30 * time.Minute, verbUpgradeP2PToHybrid},
	helper.VerbSyncthingInstall:     {30 * time.Minute, verbSyncthingInstall},
	helper.VerbReadNodeAddresses:    {1 * time.Minute, verbReadNodeAddresses},
	helper.VerbReadSSHAuth:          {1 * time.Minute, verbReadSSHAuth},
	helper.VerbReadWalletState:      {1 * time.Minute, verbReadWalletState},
	helper.VerbReadKeyVerificationState: {
		1 * time.Minute, verbReadKeyVerificationState,
	},
	helper.VerbVerifyAdminLogin: {1 * time.Minute, verbVerifyAdminLogin},
}

var (
	readLocalAccounts      = host.ReadLocalAccounts
	readLocalAccount       = host.ReadLocalAccount
	loadSystemConfig       = config.Load
	setupAutoUnlock        = host.SetupAutoUnlock
	disableAutoUnlock      = host.DisableAutoUnlock
	upgradeP2P             = host.UpgradeP2PToHybrid
	installSyncthing       = host.InstallSyncthing
	walletExists           = host.WalletExists
	keyVerificationPending = host.KeyVerificationPending
	verifyAdminLogin       = host.VerifyAdminLogin
	restageFacts           = restage
	controlNodeService     = host.ControlService
	updatePackages         = host.UpdatePackages
	updateSelf             = update.Self
	requestReboot          = host.RequestReboot
)

// decode unmarshals params strictly: unknown fields are an
// error, so a client/server drift surfaces as a loud refusal
// instead of a silently ignored option.
func decode(params json.RawMessage, into any) error {
	dec := json.NewDecoder(strings.NewReader(string(params)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return fmt.Errorf("invalid parameters: %v", err)
	}
	return nil
}

func rejectParams(params json.RawMessage) error {
	trimmed := bytes.TrimSpace(params)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	return errors.New("this operation accepts no parameters")
}

// loadConfig reads the node's config root-side. Verbs use it
// for facts the client must not be able to misstate. Typed mutating verbs also
// publish their own desired-state change before returning success.
func loadConfig() (*config.AppConfig, error) {
	cfg, err := loadSystemConfig()
	if err != nil {
		return nil, fmt.Errorf(
			"read node config: %v — the node may not be "+
				"installed", err)
	}
	return cfg, nil
}

// ── Service control ──────────────────────────────────────

func verbServiceAction(_ *verbCtx, params json.RawMessage) (any, error) {
	var p helper.ServiceActionParams
	if err := decode(params, &p); err != nil {
		return nil, err
	}
	// Revalidate the untrusted IPC input before privileged execution.
	request, err := servicecontrol.New(p.Unit, p.Action)
	if err != nil {
		return nil, err
	}
	completion, err := controlNodeService(request)
	if err != nil {
		return nil, err
	}
	// LND may replace its TLS certificate at startup. Publish the matching
	// staged copy before reporting completion to the terminal.
	if request.Service() == "lnd" && request.Action() != "stop" {
		if err := restageFacts(helper.VerbServiceAction); err != nil {
			return nil, fmt.Errorf("lnd is active; credential refresh failed: %w", err)
		}
	}
	return completion, nil
}

func verbReboot(_ *verbCtx, params json.RawMessage) (any, error) {
	if err := rejectParams(params); err != nil {
		return nil, err
	}
	if err := requestReboot(); err != nil {
		return nil, err
	}
	return helper.RebootResult{Accepted: true}, nil
}

// ── Sizes ────────────────────────────────────────────────

func verbDirSize(_ *verbCtx, params json.RawMessage) (any, error) {
	var p helper.DirSizeParams
	if err := decode(params, &p); err != nil {
		return nil, err
	}
	// Closed enum, not a path. "lnd" is the only entry: the
	// bitcoin data dir's size comes from bitcoind's own RPC
	// (size_on_disk) with no privileged call at all.
	if p.Which != "lnd" {
		return nil, fmt.Errorf("unknown directory %q", p.Which)
	}
	size := system.DirSize(paths.LNDDataDir)
	if size == "N/A" {
		return nil, errors.New("could not measure the LND data dir")
	}
	return helper.DirSizeResult{Size: size}, nil
}

// ── Passwords ────────────────────────────────────────────

func verbStageWalletPassword(_ *verbCtx, params json.RawMessage) (any, error) {
	var p helper.StageWalletPasswordParams
	if err := decode(params, &p); err != nil {
		return nil, err
	}
	password, err := autounlock.NewPassword(p.Password)
	if err != nil {
		return nil, err
	}
	return setupAutoUnlock(password)
}

func verbRemoveWalletPassword(_ *verbCtx, _ json.RawMessage) (any, error) {
	return disableAutoUnlock()
}

// ── Credential staging ───────────────────────────────────

func verbStageLNDCredentials(_ *verbCtx, _ json.RawMessage) (any, error) {
	return nil, restageFacts(helper.VerbStageLNDCredentials)
}

// Wallet creation mints only the admin macaroon. Keep that transition narrow;
// the broader credentials verb remains the repair path for an LND client that
// cannot know whether its certificate, macaroon, or both became stale.
func verbStageLNDMacaroon(_ *verbCtx, _ json.RawMessage) (any, error) {
	return nil, restageFacts(helper.VerbStageLNDMacaroon)
}

// ── Read-only verbs ──────────────────────────────────────
//
// Live reads for display facts the TUI needs at human
// cadence (screen entry). These facts can change with no
// operation of ours involved — a root login can destroy and
// recreate hidden-service directories, an sshd drop-in can be
// edited by hand or by provider tooling — and a fact with no
// failure moment cannot be repaired at one. So no copy is
// kept anywhere: the answer is read from its source at the
// moment of the question, and what the screen shows is what
// is true right now. Cost per read: one socket round-trip, a
// handful of times per session. Both verbs take no parameters
// and change nothing; they get the same peer-credential check
// and journal record as every other verb.

func verbReadNodeAddresses(_ *verbCtx, _ json.RawMessage) (any, error) {
	onions := host.ReadOnionAddresses()
	return helper.NodeAddressesResult{
		BitcoinP2POnion: onions.BitcoinP2P,
		LNDGRPCOnion:    onions.LNDGRPC,
		LNDRESTOnion:    onions.LNDREST,
		SyncthingOnion:  onions.Syncthing,
		// Read the certificate identity even when the daemon is stopped.
		SyncthingDeviceID: host.SyncthingDeviceID(),
	}, nil
}

func verbReadSSHAuth(_ *verbCtx, _ json.RawMessage) (any, error) {
	// Sample the operator configuration at localhost. Errors remain unknown;
	// source-specific Match rules may differ on a real connection.
	enabled, err := host.EffectiveSSHPasswordAuth()
	if err != nil {
		return nil, err
	}
	return helper.SSHAuthResult{PasswordAuthEnabled: enabled}, nil
}

func verbReadWalletState(_ *verbCtx, _ json.RawMessage) (any, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	wallet, err := walletExists(cfg.Network)
	if err != nil {
		return nil, err
	}
	return helper.WalletStateResult{WalletExists: wallet}, nil
}

func verbReadKeyVerificationState(
	_ *verbCtx, params json.RawMessage,
) (any, error) {
	if err := rejectParams(params); err != nil {
		return nil, err
	}
	pending, err := keyVerificationPending()
	if err != nil {
		return nil, err
	}
	return helper.KeyVerificationStateResult{Pending: pending}, nil
}

// This is a closed security-workflow mutation, not a caller-directed config
// write: current sshd journal evidence is the only authority allowed to clear
// the root-private marker.
func verbVerifyAdminLogin(_ *verbCtx, params json.RawMessage) (any, error) {
	if err := rejectParams(params); err != nil {
		return nil, err
	}
	pending, verified, err := verifyAdminLogin()
	if err != nil {
		return nil, err
	}
	return helper.VerifyAdminLoginResult{Pending: pending, Verified: verified}, nil
}

// ── Config writers (templates live on this side) ─────────

func verbRebuildSSHConfig(_ *verbCtx, params json.RawMessage) (any, error) {
	var p helper.RebuildSSHConfigParams
	if err := decode(params, &p); err != nil {
		return nil, err
	}
	// The fresh key guard, validate/restore sequence, and effective-state
	// verification live at the privileged write boundary.
	if err := host.RebuildSSHHardeningConfig(
		p.PasswordAuthDisabled); err != nil {
		return nil, err
	}
	// Nothing to re-stage: the effective password-auth answer
	// is not copied anywhere — readers ask the read-ssh-auth
	// verb, which queries sshd live.
	return nil, nil
}

// ── Streaming verbs ──────────────────────────────────────

func verbPackageUpdate(ctx *verbCtx, params json.RawMessage) (any, error) {
	if err := rejectParams(params); err != nil {
		return nil, err
	}
	return nil, updatePackages(ctx.emitStep)
}

func verbSelfUpdate(ctx *verbCtx, params json.RawMessage) (any, error) {
	var p helper.SelfUpdateParams
	if err := decode(params, &p); err != nil {
		return nil, err
	}
	// Enforce the existing version policy independently of TUI admission.
	// SameMajor validates both strings before the target reaches a release URL.
	same, err := release.SameMajor(ctx.version, p.Version)
	if err != nil {
		return nil, err
	}
	if !same {
		return nil, fmt.Errorf(
			"v%s is a major release — it is not installed "+
				"through self-update; see its release notes",
			p.Version)
	}
	// The root operation owns downloads and verification. It never consumes
	// an artifact staged by the unprivileged caller.
	if err := updateSelf(p.Version, ctx.emitStep); err != nil {
		return nil, err
	}
	// Exit after answering so the next helper activation runs the new binary.
	// Existing TUI processes continue running their original executable.
	ctx.exitAfterEnd = true
	return nil, nil
}

func verbUpgradeP2PToHybrid(ctx *verbCtx, params json.RawMessage) (any, error) {
	var p helper.UpgradeP2PParams
	if err := decode(params, &p); err != nil {
		return nil, err
	}
	request, err := p2p.NewUpgradeRequest(p.ExpectedIPv4)
	if err != nil {
		return nil, err
	}
	return nil, upgradeP2P(request, func() error {
		return restageFacts(helper.VerbUpgradeP2PToHybrid)
	}, ctx.emitStep)
}

func verbSyncthingInstall(ctx *verbCtx, params json.RawMessage) (any, error) {
	if err := rejectParams(params); err != nil {
		return nil, err
	}
	return nil, installSyncthing(func() error {
		return restageFacts(helper.VerbSyncthingInstall)
	}, ctx.emitStep)
}

func verbReadAccounts(_ *verbCtx, params json.RawMessage) (any, error) {
	if err := rejectParams(params); err != nil {
		return nil, err
	}
	return readLocalAccounts()
}

func verbReadAccount(_ *verbCtx, params json.RawMessage) (any, error) {
	var ref accountaccess.Ref
	if err := decode(params, &ref); err != nil {
		return nil, err
	}
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	return readLocalAccount(ref)
}
