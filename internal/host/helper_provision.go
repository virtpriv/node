package host

import (
	"fmt"
	"os"

	"github.com/virtpriv/node/internal/logger"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/system"
)

// helperSocketUnit is the .socket unit. The socket node's
// ownership and mode are the authentication for the privilege
// boundary: systemd creates the node root:vpn 0660 BEFORE any
// helper process exists, so there is no bind-then-chmod window
// where it is world-connectable.
const helperSocketUnit = `[Unit]
Description=vpn root helper control socket

[Socket]
ListenStream=` + paths.HelperSocket + `
# root:vpn 0660. The mode is applied when systemd creates the
# node, so the socket is never briefly world-connectable. These
# permissions ARE the authentication, and the helper also
# verifies each connecting process's uid (SO_PEERCRED).
SocketUser=root
SocketGroup=` + paths.AdminUser + `
SocketMode=0660
# Default, stated for the record: one helper process receives
# the listening socket and serves connections one at a time.
Accept=no
# Remove the node on a deliberate stop of this socket unit.
# The helper's own idle exit does not stop the socket unit and
# keeps the node in place.
RemoveOnStop=yes

[Install]
WantedBy=sockets.target
`

// helperServiceUnit is the .service unit. It has no [Install]
// section on purpose: the service is only ever started by
// socket traffic, never enabled directly.
const helperServiceUnit = `[Unit]
Description=vpn root helper (socket-activated)
# systemd's defaults, restated so they are visible: at most 5
# starts per 10 seconds, after which activation is refused and
# the SOCKET unit enters a failed state until
# ` + "`systemctl reset-failed vpn-helperd.socket`" + `. The helper's
# 120-second idle exit keeps normal use far below this budget
# (steady state is at most one start per idle period).
StartLimitIntervalSec=10s
StartLimitBurst=5

[Service]
# exec, not simple: the start job fails if the binary is
# missing or broken (matters right after a self-update), and
# that failure is visible in systemctl status immediately.
Type=exec
ExecStart=` + paths.BinaryPath + ` helperd
# No supervisor restarts: socket activation IS the restart
# path. Restart=always would loop on every clean idle exit and
# drive the start-rate limit into refusing real work.
Restart=no
# Audit trail: stdout/stderr go to the journal under this
# identifier. The admin user can read it (systemd-journal
# group) but cannot alter it.
SyslogIdentifier=vpn-helperd
# Confinement for a root helper that must write /etc and /var
# and run apt: private /tmp, and home mounted read-only. Note:
# ProtectHome=read-only, NOT =yes, is load-bearing. The
# password-auth lockout guard (the rebuild-ssh-config verb)
# reads the admin user's authorized_keys under /home to refuse
# stranding the box. Hiding home (=yes) makes it read an empty
# /home and wrongly refuse every disable. Read-only still stops
# the helper from writing home, and the node's secrets live in
# /var/lib, not home. Broader sandboxing (ProtectSystem=) would
# break the helper's actual job.
PrivateTmp=yes
ProtectHome=read-only
`

// InstallHelperUnits writes both units, reloads systemd, and enables the socket.
// It requires an active socket unit and a node at the configured path.
func InstallHelperUnits() error {
	if err := system.WriteFileRoot(paths.HelperSocketUnit,
		[]byte(helperSocketUnit), 0644); err != nil {
		return err
	}
	if err := system.WriteFileRoot(paths.HelperServiceUnit,
		[]byte(helperServiceUnit), 0644); err != nil {
		return err
	}
	if err := system.RunRoot("systemctl", "daemon-reload"); err != nil {
		return err
	}
	if err := system.RunRoot("systemctl", "enable", "--now",
		paths.HelperSocketUnitName); err != nil {
		return err
	}
	// Confirm activation and node existence. Native validation separately
	// checks the socket type, ownership and mode configured by the unit.
	if !system.IsServiceActive(paths.HelperSocketUnitName) {
		return fmt.Errorf("%s is not active after enable",
			paths.HelperSocketUnitName)
	}
	if _, err := os.Stat(paths.HelperSocket); err != nil {
		return fmt.Errorf(
			"helper socket node missing after enable: %w", err)
	}
	logger.Install("helper socket enabled at %s (root:%s 0660)",
		paths.HelperSocket, paths.AdminUser)
	return nil
}
