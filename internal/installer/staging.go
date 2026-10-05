package installer

import (
	"fmt"
	"os"

	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/host"
	"github.com/virtpriv/node/internal/logger"
	"github.com/virtpriv/node/internal/paths"
)

// StageBoardAll builds the complete board at install time:
// every fact that exists on this box right now. Facts whose
// source does not exist yet are SKIPPED, not failed; a fresh
// box has no wallet macaroon until the operator creates the
// wallet (that moment stages it), and no Syncthing facts until
// Syncthing is installed.
func StageBoardAll() error {
	if err := host.FixBoardOwnership(); err != nil {
		return err
	}

	type fact struct {
		name string
		fn   func() error
		skip func() bool // true = source legitimately absent
	}
	cfg, cfgErr := config.Load()
	haveSyncthing := cfgErr == nil && cfg.SyncthingEnabled
	haveWallet := false
	if cfgErr == nil {
		if profile, err := cfg.NetworkConfig(); err == nil {
			_, statErr := os.Stat(paths.LNDMacaroon(profile.LNDNetwork))
			haveWallet = statErr == nil
		}
	}

	facts := []fact{
		{"lnd-tls-cert", host.StageLNDTLSCert, nil},
		{"lnd-admin-macaroon", host.StageLNDMacaroon,
			func() bool { return !haveWallet }},
		{"syncthing-api-key", host.StageSyncthingAPIKey,
			func() bool { return !haveSyncthing }},
	}
	for _, f := range facts {
		if f.skip != nil && f.skip() {
			logger.Install(
				"staging: %s skipped (source not present yet)",
				f.name)
			continue
		}
		if err := f.fn(); err != nil {
			return fmt.Errorf("stage %s: %w", f.name, err)
		}
	}
	logger.Install("staging board complete at %s", paths.StateDir)
	return nil
}
