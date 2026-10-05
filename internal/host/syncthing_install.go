package host

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"

	"github.com/virtpriv/node/internal/component"
	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/paths"
)

type syncthingInstallStep struct {
	name string
	run  func() error
}

type syncthingInstallOps struct {
	load          func() (*config.AppConfig, error)
	residue       func() (bool, error)
	prerequisites func(*config.AppConfig) error
	steps         func(*config.AppConfig) ([]syncthingInstallStep, string)
	stage         func() error
	stagePassword func(string) error
	save          func(*config.AppConfig) error
}

// InstallSyncthing owns fresh add-on provisioning and final desired publication.
// The helper supplies its fixed freshness action. Accepted work continues if the
// terminal disconnects; neither a failed step nor disconnect implies rollback.
func InstallSyncthing(stage func() error, progress func(int)) error {
	if os.Geteuid() != 0 {
		return errors.New("syncthing installation requires the root helper")
	}
	return installSyncthing(syncthingInstallOps{
		load: config.Load, residue: syncthingResiduePresent,
		prerequisites: verifySyncthingInstallPrerequisites,
		steps:         syncthingInstallSteps, stage: stage,
		stagePassword: StageSyncthingWebPassword, save: config.Save,
	}, progress)
}

func installSyncthing(ops syncthingInstallOps, progress func(int)) error {
	cfg, err := ops.load()
	if err != nil {
		return fmt.Errorf("read node configuration: %w", err)
	}
	if cfg.SyncthingEnabled {
		return errors.New("Syncthing is already enabled")
	}
	residue, err := ops.residue()
	if err != nil {
		return err
	}
	if residue {
		return errors.New("Syncthing add-on residue exists while desired enablement is false; refusing to modify it; retained-state recovery is not supported")
	}
	if err := ops.prerequisites(cfg); err != nil {
		return err
	}
	proposed := *cfg
	proposed.SyncthingEnabled = true
	steps, password := ops.steps(&proposed)
	for i, step := range steps {
		if err := step.run(); err != nil {
			return fmt.Errorf("%s: %w", step.name, err)
		}
		progress(i)
	}
	// Both credentials must be available before desired enablement is published.
	// The password never leaves this root operation as a result or progress value.
	if err := ops.stage(); err != nil {
		return err
	}
	if err := ops.stagePassword(password); err != nil {
		return fmt.Errorf("stage Syncthing Web password: %w", err)
	}
	progress(len(steps))
	if err := ops.save(&proposed); err != nil {
		return fmt.Errorf("publish Syncthing setting: %w", err)
	}
	progress(len(steps) + 1)
	return nil
}

func syncthingInstallSteps(
	cfg *config.AppConfig,
) ([]syncthingInstallStep, string) {
	passBytes := make([]byte, 12)
	// The pinned Go toolchain fills the buffer or terminates on entropy failure.
	rand.Read(passBytes)
	syncPassword := hex.EncodeToString(passBytes)

	var syncWork string
	steps := []syncthingInstallStep{
		{name: "Downloading Syncthing " + component.SyncthingVersion,
			run: func() error {
				var err error
				syncWork, err = os.MkdirTemp("", "vpn-sync-")
				if err != nil {
					return fmt.Errorf("create work dir: %w", err)
				}
				return downloadSyncthing(
					component.SyncthingVersion, syncWork)
			}},
		{name: "Verifying Syncthing",
			run: func() error {
				if err := verifySyncthingSig(syncWork); err != nil {
					return err
				}
				return verifySyncthingChecksum(syncWork)
			}},
		{name: "Installing Syncthing",
			run: func() error {
				if err := extractAndInstallSyncthing(
					component.SyncthingVersion, syncWork); err != nil {
					return err
				}
				os.RemoveAll(syncWork)
				return nil
			}},
		{name: "Creating Syncthing directories",
			run: func() error {
				if err := createSystemGroup(backupGroup); err != nil {
					return err
				}
				if err := CreateSystemUser(syncthingUser,
					paths.SyncthingDataDir); err != nil {
					return err
				}
				return createSyncthingDirs()
			}},
		{name: "Creating Syncthing service",
			run: writeSyncthingService},
		{name: "Configuring Syncthing authentication",
			run: func() error {
				return initializeSyncthingConfig(syncPassword)
			}},
		{name: "Adding Syncthing firewall rule",
			run: AllowSyncthingFirewallRule},
		{name: "Reloading Tor configuration",
			run: func() error {
				return configureAndReloadTorForSyncthing(cfg)
			}},
		{name: "Starting Syncthing", run: startSyncthing},
		{name: "Registering backup folder",
			run: registerBackupFolder},
		{name: "Setting up channel backup watcher",
			run: func() error {
				return setupChannelBackupWatcher(cfg)
			}},
	}
	return steps, syncPassword
}
