// internal/paths/paths.go

// Package paths centralizes all filesystem paths used by vpn.
// Every hardcoded path in the project should be defined here.
package paths

import "fmt"

// ── Configuration ────────────────────────────────────────

const (
	ConfigDir  = "/etc/vpn"
	ConfigFile = "/etc/vpn/config.json"

	BitcoinConf = "/etc/bitcoin/bitcoin.conf"
	BitcoinDir  = "/etc/bitcoin"

	// StateDir is the staging board: root-written files that
	// carry privileged facts (staged credentials) to the
	// unprivileged admin user. The directory is root:vpn
	// 0750, each file root:vpn 0640. Root (the installer and
	// the helper) writes, the admin user reads. Every file is
	// re-written by whatever operation changes the fact it
	// carries. A reader that finds a file missing or
	// unreadable reports the feature unavailable and logs
	// why, never guesses.
	//
	// Mostly facts consumed at MACHINE cadence (per RPC call,
	// per dial) live here: credentials, which fail closed at
	// the moment of use when stale, so a stale copy cannot
	// hide. Display facts consumed at HUMAN cadence (onion
	// addresses, the Syncthing device ID, the SSH
	// password-auth answer) have no copy at all: a stale copy
	// of those would render confidently wrong with no failure
	// to catch it, so the TUI reads them live through the
	// helper's read-only verbs instead. The Syncthing web password is the
	// one human-cadence exception: Syncthing retains only its hash, so the
	// install operation stages the generated plaintext once for later display.
	// Every board file
	// declares how it stays fresh in the helper's freshness
	// table (internal/helperd/matrix.go), and a unit test
	// fails on any file without a declaration.
	//
	// The board lives under /var/lib/vpn rather than /etc/vpn so desired
	// configuration and staged credentials have separate, auditable
	// authorities. Both trees have root-controlled ancestors. The TUI can read
	// the specific group-readable files but cannot replace either directory.
	VarLibVPN = "/var/lib/vpn"
	StateDir  = VarLibVPN + "/state"

	// PrivateDir contains root-authoritative lifecycle and
	// security-workflow state. Every ancestor remains root
	// controlled. The vpn operator cannot replace these files.
	PrivateDir = VarLibVPN + "/private"

	// LayoutVersion records the exact supported base-install
	// generation. InstallStateFile is the bounded base-install
	// progress ledger. PasswordPendingMarker closes the gap
	// between applying and displaying an unattended password.
	LayoutVersion         = PrivateDir + "/layout-version"
	InstallStateFile      = PrivateDir + "/install-state.json"
	PasswordPendingMarker = PrivateDir + "/password-pending"
	KeyVerificationMarker = PrivateDir + "/key-verification-pending"

	// InstallBootstrap* are short-lived, root-owned indications that
	// lifecycle authority is being published. The immutable initial install
	// context is encoded in the exact path so even an interruption before the
	// normal private tree exists remains unambiguous. They live beside
	// VarLibVPN because they must exist before that tree is created.
	InstallBootstrapPrefix  = "/var/lib/vpn-install-bootstrap-"
	InstallBootstrapMainnet = InstallBootstrapPrefix +
		"service-layout-1-mainnet-tor"
	InstallBootstrapTestnet4 = InstallBootstrapPrefix +
		"service-layout-1-testnet4-tor"
	InstallBootstrapPublicSignet = InstallBootstrapPrefix +
		"service-layout-1-public-signet-tor"

	// RuntimeDir and InstallLock are transient serialization
	// state. The stable lock is deliberately separate from the
	// atomically replaced durable ledger.
	RuntimeDir  = "/run/vpn"
	InstallLock = RuntimeDir + "/install.lock"

	// Previous lifecycle paths are recognized only so v0.7.0
	// can refuse unsupported or conflicting state. They are
	// never adopted, normalized, or migrated.
	OldInstallStateFile  = "/etc/vpn/install-state.json"
	OldPasswordPending   = "/etc/vpn/password-pending"
	OldServiceLayoutMark = VarLibVPN + "/service-layout-v1"

	// Staging board files. One fact per file.
	StateBitcoindRPCPass      = StateDir + "/bitcoind-rpc.pass"
	StateLNDTLSCert           = StateDir + "/lnd-tls.cert"
	StateLNDMacaroon          = StateDir + "/lnd-admin.macaroon"
	StateSyncthingAPIKey      = StateDir + "/syncthing-api-key"
	StateSyncthingWebPassword = StateDir +
		"/syncthing-web-password"

	LNDConf = "/etc/lnd/lnd.conf"
	LNDDir  = "/etc/lnd"

	SyncthingDir = "/etc/syncthing"
)

// ── Loopback endpoints ───────────────────────────────────

// Each loopback endpoint is defined exactly once, and BOTH
// ends of every connection use the same constant: lnd.conf
// binds LND to these values, and every client dials them,
// so the two ends cannot silently disagree.
//
// The values are literal IPv4 addresses, never the name
// localhost. The installer disables IPv6 at the kernel, but
// Debian's /etc/hosts still maps localhost to ::1, and that
// file is not ours to correct (cloud provider tooling may
// regenerate it). Reaching loopback by name can therefore
// resolve to an IPv6 address the box cannot use. On a node
// that disables IPv6, loopback is always dialed by address.
const (
	// LNDGRPCEndpoint is LND's gRPC server. Dialed by the
	// TUI's gRPC client, the wallet-creation lncli
	// invocation, and the shell's lncli wrapper.
	LNDGRPCEndpoint = "127.0.0.1:10009"

	// LNDRESTEndpoint is LND's REST server. Dialed by the
	// installer's readiness probe. The Tor hidden service
	// forwards the REST onion here.
	LNDRESTEndpoint = "127.0.0.1:8080"

	// LNDP2PBind is LND's peer listener in tor-only mode.
	// Hybrid mode binds all interfaces instead, computed
	// where the config is written.
	LNDP2PBind = "127.0.0.1:9735"
)

// ── Data ─────────────────────────────────────────────────

const (
	BitcoinDataDir   = "/var/lib/bitcoin"
	LNDDataDir       = "/var/lib/lnd"
	SyncthingDataDir = "/var/lib/syncthing"

	// ExportDir is the root-controlled boundary for artifacts that
	// one service deliberately publishes to another. It is separate
	// from every daemon's private state.
	ExportDir = VarLibVPN + "/exports"

	// ExportReadyMarkerName is the installer-owned marker convention
	// for project exports served from read-only Syncthing folders.
	// Each export places its own marker inside its folder root.
	ExportReadyMarkerName = ".vpn-export-ready"

	// LNDBackupStage is private publisher staging. Syncthing cannot
	// enter it. LNDBackupExport is the send-only folder registered
	// with Syncthing. Both live beneath ExportDir so publication can
	// use a same-filesystem atomic rename without traversing
	// Syncthing's private state.
	LNDBackupStage        = ExportDir + "/lnd-backup-stage"
	LNDBackupExport       = ExportDir + "/lnd-backup"
	LNDBackupExportMarker = LNDBackupExport + "/" + ExportReadyMarkerName
)

// ── LND files ────────────────────────────────────────────

const (
	LNDTLSCert        = "/var/lib/lnd/tls.cert"
	LNDTLSKey         = "/var/lib/lnd/tls.key"
	LNDWalletPassword = "/var/lib/lnd/wallet_password"
	// LNDWalletPasswordStage is the one recognizable same-filesystem staging
	// name used while publishing the canonical password. An interrupted write
	// can be cleaned deterministically without scanning or guessing at files in
	// LND's private directory.
	LNDWalletPasswordStage = "/var/lib/lnd/.vpn-wallet-password.stage"
)

// LNDMacaroon returns the path to the admin macaroon for a given network.
func LNDMacaroon(network string) string {
	return fmt.Sprintf("/var/lib/lnd/data/chain/bitcoin/%s/admin.macaroon", network)
}

// ChannelBackup returns the path to the channel backup for a given network.
func ChannelBackup(network string) string {
	return fmt.Sprintf("/var/lib/lnd/data/chain/bitcoin/%s/channel.backup", network)
}

// ── Tor ──────────────────────────────────────────────────

const (
	Torrc                = "/etc/tor/torrc"
	TorBitcoinP2P        = "/var/lib/tor/bitcoin-p2p"
	TorLNDGRPC           = "/var/lib/tor/lnd-grpc"
	TorLNDREST           = "/var/lib/tor/lnd-rest"
	TorLNDRESTHostname   = "/var/lib/tor/lnd-rest/hostname"
	TorSyncthing         = "/var/lib/tor/syncthing"
	TorSyncthingHostname = "/var/lib/tor/syncthing/hostname"
	TorSyncthingSync     = "/var/lib/tor/syncthing-sync"
)

// ── Systemd ──────────────────────────────────────────────

const (
	BitcoindService     = "/etc/systemd/system/bitcoind.service"
	LNDService          = "/etc/systemd/system/lnd.service"
	LNDServiceDropInDir = "/etc/systemd/system/lnd.service.d"
	// LNDVerificationDropIn disables automatic restart only while VPN is
	// proving one candidate wallet password. It is persistent (rather than
	// under /run) so a host reboot cannot reintroduce a password-failure loop
	// halfway through the operation, and is removed before success is
	// published.
	LNDVerificationDropIn = LNDServiceDropInDir +
		"/90-vpn-auto-unlock-verification.conf"
	SyncthingService    = "/etc/systemd/system/syncthing.service"
	BackupWatchPath     = "/etc/systemd/system/lnd-backup-watch.path"
	BackupExportService = "/etc/systemd/system/lnd-backup-export.service"
	BackupCheckTimer    = "/etc/systemd/system/lnd-backup-check.timer"

	// The LND TLS certificate watch. At startup LND replaces an
	// expired tls.cert, and tlsautorefresh also replaces one whose
	// configured SAN inputs changed. No TUI-requested operation is
	// involved, so no operation can re-stage the TUI's
	// copy. This path unit closes that gap at the source: a
	// rewrite of the certificate triggers the stage service,
	// which refreshes the staged copy within seconds.
	LNDCertWatchPath        = "/etc/systemd/system/vpn-lnd-cert-watch.path"
	LNDCertStageService     = "/etc/systemd/system/vpn-lnd-cert-stage.service"
	LNDCertWatchPathName    = "vpn-lnd-cert-watch.path"
	LNDCertStageServiceName = "vpn-lnd-cert-stage.service"

	// The root helper's socket-activated units. The socket
	// node's ownership and mode (root:vpn 0660, created by
	// systemd before the helper ever runs) ARE the
	// authentication for privileged operations. The service
	// is started by traffic and exits when idle.
	HelperSocket         = "/run/vpn-helperd.sock"
	HelperSocketUnit     = "/etc/systemd/system/vpn-helperd.socket"
	HelperServiceUnit    = "/etc/systemd/system/vpn-helperd.service"
	HelperSocketUnitName = "vpn-helperd.socket"
)

// ── Logs ─────────────────────────────────────────────────

const (
	LogFile = "/var/log/vpn.log"
)

// ── System ───────────────────────────────────────────────

const (
	OSRelease   = "/etc/os-release"
	SudoersFile = "/etc/sudoers"
	SudoersDir  = "/etc/sudoers.d"

	SyncthingConfigXML = "/etc/syncthing/config.xml"
	UFWDefault         = "/etc/default/ufw"
	SSHDConfig         = "/etc/ssh/sshd_config"
	// SSHDDropIn precedes ordinary provider drop-ins. Its Match blocks scope
	// owner authentication. Effective policy is verified before SSH restarts.
	SSHDDropIn = "/etc/ssh/sshd_config.d/00-vpn-hardening.conf"

	// OldSSHDDropIn is the pre-rename path. Its presence is
	// lifecycle-conflict evidence. v0.7.0 refuses it and never
	// deletes, adopts, or rewrites it.
	OldSSHDDropIn = "/etc/ssh/sshd_config.d/00-rlvpn-hardening.conf"

	Fail2banJail       = "/etc/fail2ban/jail.local"
	AutoUpgrades       = "/etc/apt/apt.conf.d/20auto-upgrades"
	UnattendedUpgrades = "/etc/apt/apt.conf.d/50unattended-upgrades"
	AptTorProxy        = "/etc/apt/apt.conf.d/99-tor-proxy"
	DisableIPv6Conf    = "/etc/sysctl.d/99-disable-ipv6.conf"
)

// ── User ─────────────────────────────────────────────────

const (
	// AdminUser is the node's admin login, the same name as the
	// binary, so there is one name to know. It is a clean break
	// from the old ripsline user. Existing old identities are
	// refused by the fresh-install lifecycle classifier.
	AdminUser          = "vpn"
	AdminHome          = "/home/" + AdminUser
	AdminBashrc        = AdminHome + "/.bashrc"
	AdminBashProfile   = AdminHome + "/.bash_profile"
	AuthorizedKeysFile = AdminHome + "/.ssh/authorized_keys"

	// AdminSudoers is the owner grant installed after initial password creation.
	// Unmarked pre-existing policy remains a fresh-install conflict.
	AdminSudoers = "/etc/sudoers.d/" + AdminUser

	// BinaryPath is where the installer places the running
	// binary (and where self-update installs new ones).
	BinaryPath      = "/usr/local/bin/vpn"
	SyncthingBinary = "/usr/local/bin/syncthing"
)

// ── Cache ────────────────────────────────────────────────

const (
	VersionCacheDir  = AdminHome + "/.cache/vpn"
	VersionCacheFile = VersionCacheDir + "/latest-version"
)
