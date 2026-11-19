// internal/paths/paths.go

// Package paths centralizes all filesystem paths used by vpn.
// Every hardcoded path in the project should be defined here.
package paths

// ── Configuration ────────────────────────────────────────

const (
	ConfigDir  = "/etc/vpn"
	ConfigFile = "/etc/vpn/config.json"

	VarLibVPN = "/var/lib/vpn"

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
	// JobsDir holds one folder per job of the vpn API, root only.
	JobsDir = PrivateDir + "/jobs"

	// InstallBootstrap is a short-lived, root-owned indication that
	// lifecycle authority is being published. Its name carries the layout
	// number, so an interruption before the normal private tree exists
	// remains unambiguous. It lives beside VarLibVPN because it must exist
	// before that tree is created. Any other name with the prefix is
	// refused, such as the names the Bitcoin and Lightning node used.
	InstallBootstrapPrefix = "/var/lib/vpn-install-bootstrap-"
	InstallBootstrap       = InstallBootstrapPrefix + "service-layout-2"

	// RuntimeDir and InstallLock are transient serialization
	// state. The stable lock is deliberately separate from the
	// atomically replaced durable ledger.
	RuntimeDir  = "/run/vpn"
	InstallLock = RuntimeDir + "/install.lock"
	// OperationsLock makes a change's acceptance one step, and the update
	// worker holds it for its run.
	OperationsLock = RuntimeDir + "/operations.lock"
)

// ── Tor ──────────────────────────────────────────────────

const (
	// Torrc is Debian's own file. vpn reads it before an install and never
	// writes it.
	Torrc = "/etc/tor/torrc"

	// TorControlSocket and TorControlCookie are set by Debian's
	// tor-service-defaults-torrc. Only root and Tor's own account can use
	// them.
	TorControlSocket = "/run/tor/control"
	TorControlCookie = "/run/tor/control.authcookie"
)

// ── Systemd ──────────────────────────────────────────────

const (
	// TorRestartDropIn makes systemd wait 30 seconds between restarts of the
	// real Tor after a crash. With systemd's defaults it gives up after five
	// quick tries.
	TorRestartDropIn = "/etc/systemd/system/tor@default.service.d/90-vpn-restart.conf"

	// vpnd's socket-activated units. The socket node's ownership
	// and mode (root:vpn 0660, created by systemd before vpnd ever
	// runs) ARE the authentication for the vpn API. The service is
	// started by traffic and exits when idle.
	VPNDSocket         = "/run/vpnd.sock"
	VPNDSocketUnit     = "/etc/systemd/system/vpnd.socket"
	VPNDServiceUnit    = "/etc/systemd/system/vpnd.service"
	VPNDSocketUnitName = "vpnd.socket"
)

// ── Logs ─────────────────────────────────────────────────

const (
	LogFile = "/var/log/vpn.log"
	// LogRotate is vpn's file for Debian's logrotate, which keeps LogFile
	// to a fixed size.
	LogRotate = "/etc/logrotate.d/vpn"
)

// ── System ───────────────────────────────────────────────

const (
	OSRelease   = "/etc/os-release"
	SudoersFile = "/etc/sudoers"
	SudoersDir  = "/etc/sudoers.d"

	UFWDefault = "/etc/default/ufw"
	// SSHDDropIn precedes ordinary provider drop-ins. Its Match blocks scope
	// owner authentication. Effective policy is verified before SSH restarts.
	SSHDDropIn = "/etc/ssh/sshd_config.d/00-vpn-hardening.conf"

	// OldSSHDDropIn is the path v0.6.x used under its earlier name. Its
	// presence is lifecycle-conflict evidence. The installer refuses it and
	// never deletes, adopts, or rewrites it.
	OldSSHDDropIn = "/etc/ssh/sshd_config.d/00-rlvpn-hardening.conf"

	Fail2banJail       = "/etc/fail2ban/jail.local"
	AutoUpgrades       = "/etc/apt/apt.conf.d/20auto-upgrades"
	UnattendedUpgrades = "/etc/apt/apt.conf.d/50unattended-upgrades"
	AptTor             = "/etc/apt/apt.conf.d/99-vpn-apt-tor"
	AptSources         = "/etc/apt/sources.list.d/vpn-debian.sources"
	AptRebootCheck     = "/etc/apt/apt.conf.d/99-vpn-reboot-check"
	DisableIPv6Conf    = "/etc/sysctl.d/99-disable-ipv6.conf"
	DisableMPTCPConf   = "/etc/sysctl.d/99-disable-mptcp.conf"

	// JournaldDropIn keeps the system journal on disk. Its name sorts after
	// the machine's own journald files, so its setting wins.
	JournaldDropIn    = "/etc/systemd/journald.conf.d/zz-vpn-journal.conf"
	JournalDir        = "/var/log/journal"
	JournalRuntimeDir = "/run/log/journal"
	MachineID         = "/etc/machine-id"
)

// ── User ─────────────────────────────────────────────────

const (
	// AdminUser is the node's admin login, the same name as the
	// binary, so there is one name to know. It is a clean break
	// from the old ripsline user. Existing old identities are
	// refused by the fresh-install lifecycle classifier.
	AdminUser          = "vpn"
	AdminHome          = "/home/" + AdminUser
	AdminBashProfile   = AdminHome + "/.bash_profile"
	AuthorizedKeysFile = AdminHome + "/.ssh/authorized_keys"
	// ReleasesFolder is where the owner may put release files the node
	// would otherwise download. It belongs to the owner, so its contents
	// are untrusted. Every installed node reads this path, so it never
	// changes.
	ReleasesFolder = AdminHome + "/releases"

	// AdminSudoers is the owner grant installed after initial password creation.
	// Unmarked pre-existing policy remains a fresh-install conflict.
	AdminSudoers = "/etc/sudoers.d/" + AdminUser

	// BinaryPath is where the installer places the running
	// binary, and where a Node Update installs new ones.
	BinaryPath = "/usr/local/bin/vpn"
)

// ── Cache ────────────────────────────────────────────────

const (
	VersionCacheDir  = AdminHome + "/.cache/vpn"
	VersionCacheFile = VersionCacheDir + "/latest-version"
)
