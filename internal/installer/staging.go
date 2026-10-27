package installer

import (
	"fmt"

	"github.com/virtpriv/node/internal/host"
	"github.com/virtpriv/node/internal/logger"
	"github.com/virtpriv/node/internal/paths"
)

// stageBoard gives the TUI its copy of LND's TLS certificate, the one
// credential that exists during the install. The wallet's macaroon is staged
// when the owner creates the wallet, and Syncthing's key when Syncthing is
// installed.
func stageBoard() error {
	if err := host.StageLNDTLSCert(); err != nil {
		return fmt.Errorf("stage lnd-tls-cert: %w", err)
	}
	logger.Install("staging board complete at %s", paths.StateDir)
	return nil
}
