package host

// LND owns certificate renewal. The native path unit refreshes the operator's
// staged certificate when LND replaces its source, independently of TUI requests.

import (
	"fmt"

	"github.com/virtpriv/node/internal/logger"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/system"
)

// lndCertWatchUnits renders the path unit and its oneshot
// service. Pure; unit-tested.
func lndCertWatchUnits() (pathUnit, serviceUnit string) {
	pathUnit = fmt.Sprintf(`[Unit]
Description=Watch the LND TLS certificate for the node TUI

[Path]
PathChanged=%s
Unit=%s

[Install]
WantedBy=multi-user.target
`, paths.LNDTLSCert, paths.LNDCertStageServiceName)

	serviceUnit = fmt.Sprintf(`[Unit]
Description=Refresh the staged copy of the LND TLS certificate

[Service]
Type=oneshot
ExecStart=%s stage-lnd-cert
SyslogIdentifier=vpn-cert-stage
`, paths.BinaryPath)
	return pathUnit, serviceUnit
}

// InstallLNDCertWatch writes both units, reloads systemd, and
// enables and starts the path unit, verifying it is active.
// Idempotent for a recognized interrupted base install.
func InstallLNDCertWatch() error {
	pathUnit, serviceUnit := lndCertWatchUnits()
	if err := system.WriteFileRoot(paths.LNDCertWatchPath,
		[]byte(pathUnit), 0644); err != nil {
		return err
	}
	if err := system.WriteFileRoot(paths.LNDCertStageService,
		[]byte(serviceUnit), 0644); err != nil {
		return err
	}
	if err := system.RunRoot("systemctl", "daemon-reload"); err != nil {
		return err
	}
	if err := system.RunRoot("systemctl", "enable", "--now",
		paths.LNDCertWatchPathName); err != nil {
		return err
	}
	// Postcondition: the watch is really armed.
	if !system.IsServiceActive(paths.LNDCertWatchPathName) {
		return fmt.Errorf("%s is not active after enable",
			paths.LNDCertWatchPathName)
	}
	logger.Install("LND TLS certificate watch enabled (%s)",
		paths.LNDCertWatchPathName)
	return nil
}
