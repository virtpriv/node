package host

import (
	"fmt"
	"os"

	"github.com/virtpriv/node/internal/logger"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/system"
)

// vpndSocketUnit is the .socket unit. The socket node's ownership and mode
// are the authentication for the vpn API: systemd creates the node root:vpn
// 0660 before any vpnd process exists, so there is no bind then chmod window
// where it is world-connectable.
const vpndSocketUnit = `[Unit]
Description=vpnd, the vpn API socket

[Socket]
ListenStream=` + paths.VPNDSocket + `
# root:vpn 0660. The mode is applied when systemd creates the
# node, so the socket is never briefly world-connectable. These
# permissions ARE the authentication, and vpnd also verifies
# each connecting process's uid (SO_PEERCRED).
SocketUser=root
SocketGroup=` + paths.AdminUser + `
SocketMode=0660
# One vpnd process receives the listening socket and answers
# its connections in parallel.
Accept=no
# Remove the node on a deliberate stop of this socket unit.
# vpnd's own idle exit does not stop the socket unit and keeps
# the node in place.
RemoveOnStop=yes

[Install]
WantedBy=sockets.target
`

// vpndServiceUnit is the .service unit. It has no [Install] section on
// purpose: the service is only ever started by socket traffic, never
// enabled directly.
const vpndServiceUnit = `[Unit]
Description=vpnd, the node's root service (socket-activated)
# systemd's defaults, restated so they are visible: at most 5
# starts per 10 seconds, after which activation is refused and
# the SOCKET unit enters a failed state until
# ` + "`systemctl reset-failed vpnd.socket`" + `. vpnd's 120 second
# idle exit keeps normal use far below this budget (steady state
# is at most one start per idle period).
StartLimitIntervalSec=10s
StartLimitBurst=5

[Service]
# exec, not simple: the start job fails if the program is
# missing or broken, and that failure is visible in systemctl
# status at once.
Type=exec
ExecStart=` + paths.BinaryPath + ` vpnd
# No supervisor restarts: socket activation IS the restart path.
# Restart=always would loop on every clean idle exit and drive
# the start rate limit into refusing real work.
Restart=no
# Audit trail: output goes to the journal under this name, as
# does every job's, so journalctl -t vpnd shows them all. The
# admin user can read it (systemd-journal group) but cannot
# alter it.
SyslogIdentifier=vpnd
# Confinement: private /tmp, and home mounted read-only. Each
# job runs in its own service with its own sandbox, set by vpnd
# when it starts the job. ProtectHome=read-only, NOT =yes, is
# load-bearing for vpnd's reads: the SSH password reading asks
# sshd for the admin user's effective setting.
PrivateTmp=yes
ProtectHome=read-only
`

// InstallVPNDUnits writes both units, reloads systemd and enables the
// socket. It requires an active socket unit and a node at the configured
// path.
func InstallVPNDUnits() error {
	if err := system.WriteFileRoot(paths.VPNDSocketUnit,
		[]byte(vpndSocketUnit), 0644); err != nil {
		return err
	}
	if err := system.WriteFileRoot(paths.VPNDServiceUnit,
		[]byte(vpndServiceUnit), 0644); err != nil {
		return err
	}
	if err := system.RunRoot("systemctl", "daemon-reload"); err != nil {
		return err
	}
	if err := system.RunRoot("systemctl", "enable", "--now",
		paths.VPNDSocketUnitName); err != nil {
		return err
	}
	// Confirm activation and node existence. Native validation separately
	// checks the socket type, ownership and mode configured by the unit.
	if !system.IsServiceActive(paths.VPNDSocketUnitName) {
		return fmt.Errorf("%s is not active after enable",
			paths.VPNDSocketUnitName)
	}
	if _, err := os.Stat(paths.VPNDSocket); err != nil {
		return fmt.Errorf(
			"vpnd's socket node missing after enable: %w", err)
	}
	logger.Install("vpnd socket enabled at %s (root:%s 0660)",
		paths.VPNDSocket, paths.AdminUser)
	return nil
}
