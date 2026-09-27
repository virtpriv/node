// internal/helper/wire.go

// Package helper is the unprivileged side of the node's
// privilege boundary. It holds three things:
//
//   - the wire protocol shared by the root helper (vpn helperd)
//     and its clients: one JSON request line per connection,
//     answered by zero or more progress events and exactly one
//     ok/error terminator (wire.go);
//   - the client the TUI uses to request privileged operations
//     over the helper's unix socket (client.go);
//   - the staging-board reader: the root-written
//     files under /var/lib/vpn/state that carry deliberately staged
//     credentials and fixed facts to the
//     admin user without any privileged code running on the
//     read path (board.go).
//
// Privileged operations live behind helperd; this package owns their IPC contract.
package helper

import "encoding/json"

// RebootResult confirms systemd accepted the request, not that the host rebooted.
type RebootResult struct {
	Accepted bool `json:"accepted"`
}

// Verb names. The helper serves this fixed menu and nothing
// else — there is no verb that runs a caller-supplied command,
// and no verb that returns a caller-chosen file. Every verb
// validates its parameters against a closed set of accepted
// values before touching the system.
const (
	// Simple verbs: one request, one terminator.
	VerbServiceAction        = "service-action"
	VerbReboot               = "reboot"
	VerbDirSize              = "dir-size"
	VerbStageWalletPassword  = "stage-wallet-password"
	VerbRemoveWalletPassword = "remove-wallet-password"
	VerbStageLNDCredentials  = "stage-lnd-credentials"
	VerbStageLNDMacaroon     = "stage-lnd-macaroon"
	VerbRebuildSSHConfig     = "rebuild-ssh-config"
	// VerifyAdminLogin checks live journal evidence and may clear the
	// root-private pending marker. It accepts no caller-chosen state.
	VerbVerifyAdminLogin = "verify-admin-login"

	// Read-only verbs: no mutation, a typed
	// result. read-account accepts only a local account identity.
	// They exist for display facts the TUI needs
	// at human cadence (screen entry) whose truth lives in
	// root-readable places and can change outside any
	// operation of ours. Keeping a copy of such a fact
	// would risk rendering it confidently wrong, so no copy
	// exists and the screens ask here instead.
	VerbReadAccounts             = "read-accounts"
	VerbReadAccount              = "read-account"
	VerbReadNodeAddresses        = "read-node-addresses"
	VerbReadSSHAuth              = "read-ssh-auth"
	VerbReadWalletState          = "read-wallet-state"
	VerbReadKeyVerificationState = "read-key-verification-state"

	// Streaming verbs: step progress events precede the
	// terminator, and feed the TUI's step renderer.
	VerbPackageUpdate      = "package-update"
	VerbSelfUpdate         = "self-update"
	VerbUpgradeP2PToHybrid = "upgrade-p2p-to-hybrid"
	VerbSyncthingInstall   = "syncthing-install"
)

// Request is the single line a client writes after connecting.
type Request struct {
	Verb   string          `json:"verb"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Event is one response line from the helper. Two kinds:
//
//   - "step": progress from a streaming verb. Index is the
//     0-based position in the verb's fixed step list. The
//     helper emits a step event on COMPLETION only — a step
//     that fails produces no step event; the failure arrives
//     as the error terminator. Err is reserved wire surface
//     for a per-step error report: the helper never sets it
//     today, and clients treat a non-empty Err as fatal if
//     one ever appears.
//   - "end": the terminator. Exactly one per connection. OK
//     with an optional Result payload, or an Error message.
type Event struct {
	Event string `json:"event"` // "step" or "end"

	// step fields
	Index int    `json:"index,omitempty"`
	Err   string `json:"err,omitempty"`

	// end fields
	OK     bool            `json:"ok,omitempty"`
	Error  string          `json:"error,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
}

// ── Typed parameters ─────────────────────────────────────
//
// Every value that crosses the privilege boundary is a field in
// one of these structs, and the helper validates each against a
// closed set (see internal/helperd). The structs live here so
// client and server marshal/unmarshal the same shape.

// ServiceActionParams: systemctl <action> <unit>. Both fields
// are validated against closed sets on the root side.
type ServiceActionParams struct {
	Unit   string `json:"unit"`
	Action string `json:"action"`
}

// DirSizeParams: which data directory to measure. "lnd" is the
// only accepted value — the bitcoin data dir's size comes from
// bitcoind's own getblockchaininfo (size_on_disk) over RPC,
// with no privileged operation at all.
type DirSizeParams struct {
	Which string `json:"which"`
}

// DirSizeResult carries the measured size back, already
// human-formatted ("12G").
type DirSizeResult struct {
	Size string `json:"size"`
}

// StageWalletPasswordParams: enable LND auto-unlock. The
// password the operator typed is written root-side to LND's
// wallet_password file (which stays owned by the service user,
// never readable by the admin user) and the LND service is
// rewritten to use it.
type StageWalletPasswordParams struct {
	Password string `json:"password"`
}

// RebuildSSHConfigParams: rewrite the SSH hardening drop-in.
// The template lives on the root side; the only caller-chosen
// value is the password-auth flag.
type RebuildSSHConfigParams struct {
	PasswordAuthDisabled bool `json:"password_auth_disabled"`
}

// SelfUpdateParams: update the vpn binary to a release version.
// Validated root-side: strict version shape, and same-major
// only (a cross-major release requires reading its release
// notes; the helper refuses it no matter what a client asks).
type SelfUpdateParams struct {
	Version string `json:"version"`
}

// NodeAddressesResult is the read-node-addresses answer: the
// node's Tor hidden-service hostnames and its Syncthing device
// ID, read from their sources at the moment of the request. An
// empty field means the observation is unavailable, including when a
// hostname file is missing or unreadable. It does not prove the service
// is unconfigured or that an observed address is reachable.
type NodeAddressesResult struct {
	BitcoinP2POnion   string `json:"bitcoin_p2p_onion"`
	LNDGRPCOnion      string `json:"lnd_grpc_onion"`
	LNDRESTOnion      string `json:"lnd_rest_onion"`
	SyncthingOnion    string `json:"syncthing_onion"`
	SyncthingDeviceID string `json:"syncthing_device_id"`
}

// SSHAuthResult is the read-ssh-auth answer: whether sshd's
// EFFECTIVE configuration permits password authentication for
// the admin user, asked of sshd itself at request time. This
// answer gates removing the last SSH key, so callers treat a
// verb error as "unavailable" and refuse the risky action.
type SSHAuthResult struct {
	PasswordAuthEnabled bool `json:"password_auth_enabled"`
}

// WalletStateResult is the independent live wallet observation. It is not
// coupled to any private workflow marker, so a marker read failure cannot make
// an otherwise readable wallet appear unknown.
type WalletStateResult struct {
	WalletExists bool `json:"wallet_exists"`
}

// KeyVerificationStateResult is the independent root-private workflow-marker
// observation. Wallet availability never depends on this read succeeding.
type KeyVerificationStateResult struct {
	Pending bool `json:"pending"`
}

// VerifyAdminLoginResult reports the private marker state after the helper
// checked current sshd journal evidence and, when verified, cleared it.
type VerifyAdminLoginResult struct {
	Pending  bool `json:"pending"`
	Verified bool `json:"verified"`
}

// ── Streaming step lists ─────────────────────────────────
//
// A streaming verb's step names are fixed per verb and known to
// both sides: the client renders the full list up front, the
// server reports completion by index. Operation tests check the stages
// against these names.

// SelfUpdateStepNames mirrors the server's self-update steps.
func SelfUpdateStepNames(version string) []string {
	return []string{
		"Downloading v" + version,
		"Verifying signature",
		"Verifying checksum",
		"Installing new binary",
	}
}

// PackageUpdateStepNames mirrors the server's package-update
// steps.
func PackageUpdateStepNames() []string {
	return []string{
		"Refreshing package lists",
		"Upgrading packages",
	}
}

// UpgradeP2PParams carries the reviewed address only as a precondition.
// The root operation independently derives the address it will advertise.
type UpgradeP2PParams struct {
	ExpectedIPv4 string `json:"expected_ipv4"`
}

// UpgradeP2PToHybridStepNames mirrors the server's one-way P2P transition.
func UpgradeP2PToHybridStepNames() []string {
	return []string{
		"Checking active firewall",
		"Updating LND config",
		"Adding hybrid P2P firewall rules",
		"Restarting LND",
		"Verifying LND TLS IP certificate",
		"Restaging LND TLS certificate",
		"Publishing node configuration",
	}
}

// SyncthingInstallStepNames mirrors the server's Syncthing
// install steps.
func SyncthingInstallStepNames(version string) []string {
	return []string{
		"Downloading Syncthing " + version,
		"Verifying Syncthing",
		"Installing Syncthing",
		"Creating Syncthing directories",
		"Creating Syncthing service",
		"Configuring Syncthing authentication",
		"Adding Syncthing firewall rule",
		"Reloading Tor configuration",
		"Starting Syncthing",
		"Registering backup folder",
		"Setting up channel backup watcher",
		"Staging Syncthing facts",
		"Publishing node configuration",
	}
}
