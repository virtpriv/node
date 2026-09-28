package update

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/virtualprivatenode/vpn/internal/component"
	"github.com/virtualprivatenode/vpn/internal/config"
	"github.com/virtualprivatenode/vpn/internal/host"
	"github.com/virtualprivatenode/vpn/internal/paths"
	"github.com/virtualprivatenode/vpn/internal/update/files"
	"github.com/virtualprivatenode/vpn/internal/update/protocol"
	"golang.org/x/sys/unix"
)

func RunWorker(version string) error {
	if err := requireUpdateHost(); err != nil {
		return err
	}
	unlock, err := files.Lock(LockPath)
	if err != nil {
		return err
	}
	defer unlock()
	j, err := loadJob(Root)
	if err != nil || j == nil {
		return err
	}
	if !j.active() || j.Phase == "failed" {
		return nil
	}
	if err := ValidateBuild(j.Review.Manifest, version); err != nil {
		return recordWorkerFailure(j, err)
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if err := verifyHash(self, j.WorkerHash); err != nil {
		return recordWorkerFailure(j, err)
	}
	cfg, err := config.Load()
	if err != nil {
		return recordWorkerFailure(j, err)
	}
	if cfg.Network != j.Review.Network {
		return recordWorkerFailure(j, errors.New("node network changed during update"))
	}
	if err := verifyHash(paths.ConfigFile, j.ConfigHash); err != nil {
		return recordWorkerFailure(j, errors.New("node configuration changed during update"))
	}
	return runJob(j, workflowOps{
		save:    func(j *job) error { return saveJob(Root, j) },
		stage:   func(j *job) error { return stageJob(j) },
		guard:   host.GuardUpdateServices,
		stop:    host.StopUpdateService,
		install: func(j *job) error { return installJob(j, cfg) },
		start:   host.StartUpdateService,
		running: host.CheckUpdateProcess,
		health:  func(j *job, c protocol.Component) error { return waitHealth(j, c, cfg) },
		commit: func(j *job) error {
			return commitJob(Root, paths.BinaryPath, j, host.CheckUpdateProcess, host.ReleaseUpdateGuards)
		},
		quarantine: quarantine,
	})
}

// commitJob publishes VPN only after the affected processes and retained bytes
// pass their final checks. Service guards stay in place until publication.
func commitJob(root, binary string, j *job, running func(protocol.Component) error, releaseGuards func([]protocol.Component) error) error {
	for _, c := range j.Affected {
		if err := running(c); err != nil {
			return err
		}
	}
	if err := verifyHash(filepath.Join(j.dir(root), "vpn"), j.WorkerHash); err != nil {
		return err
	}
	if err := files.Copy(filepath.Join(j.dir(root), "vpn"), binary, 0755); err != nil {
		return err
	}
	if err := verifyHash(binary, j.WorkerHash); err != nil {
		return err
	}
	return releaseGuards(j.Affected)
}

func recordWorkerFailure(j *job, cause error) error {
	j.Phase = "failed"
	j.Error = cause.Error()
	return errors.Join(cause, saveJob(Root, j))
}

func quarantine(components []protocol.Component) error {
	// A known failure closes the gates and stops unverified processes. No
	// database, wallet, configuration or old executable is restored.
	err := host.GuardUpdateServices(components)
	order := slices.Clone(components)
	slices.Reverse(order)
	for _, c := range order {
		err = errors.Join(err, host.StopUpdateService(c))
	}
	return err
}

func checkSpace(path string, mib int64) error {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return err
	}
	available := uint64(stat.Bavail) * uint64(stat.Bsize) / (1 << 20)
	if available < uint64(mib) {
		return fmt.Errorf("at least %d MiB free space is required on %s", mib, path)
	}
	return nil
}

func stageJob(j *job) error {
	if err := checkSpace(Root, j.Review.Manifest.MinimumFreeMiB); err != nil {
		return err
	}
	if err := checkSpace("/usr/local/bin", 1024); err != nil {
		return err
	}
	for _, c := range j.Affected {
		p := map[protocol.Component]string{protocol.Bitcoin: paths.BitcoinDataDir, protocol.LND: paths.LNDDataDir, protocol.Syncthing: paths.SyncthingDataDir}[c]
		if err := checkSpace(p, j.Review.Manifest.MinimumFreeMiB); err != nil {
			return err
		}
	}
	for _, c := range j.Affected {
		j.Step = "Download and verify " + string(c)
		if err := saveJob(Root, j); err != nil {
			return err
		}
		dir := filepath.Join(j.dir(Root), string(c))
		hashes, err := host.StageUpdateComponent(c, j.Review.Manifest.Artifact(c), dir)
		if err != nil {
			return fmt.Errorf("stage %s: %w", c, err)
		}
		for name, hash := range hashes {
			j.BinaryHashes[name] = hash
		}
	}
	return nil
}

func installJob(j *job, cfg *config.AppConfig) error {
	if err := checkSpace(Root, j.Review.Manifest.MinimumFreeMiB); err != nil {
		return err
	}
	// Verify every staged member before replacing the first installed member.
	for _, c := range j.Affected {
		_, required, err := host.UpdateArchive(c, j.Review.Manifest.Artifact(c).Version)
		if err != nil {
			return err
		}
		for _, name := range required {
			if j.BinaryHashes[name] == "" {
				return errors.New("staged component is incomplete")
			}
		}
		for _, name := range host.UpdateBinaryNames(c) {
			if hash, ok := j.BinaryHashes[name]; ok {
				if err := verifyHash(filepath.Join(j.dir(Root), string(c), "bin", name), hash); err != nil {
					return err
				}
			}
		}
	}
	for _, step := range j.Review.Manifest.HostSteps {
		j.Step = "Apply " + step
		if err := saveJob(Root, j); err != nil {
			return err
		}
		if err := host.ApplyUpdateHostStep(step, cfg); err != nil {
			return err
		}
	}
	for _, c := range j.Affected {
		for _, name := range host.UpdateBinaryNames(c) {
			if _, ok := j.BinaryHashes[name]; !ok {
				continue
			}
			j.Step = "Install " + name
			if err := saveJob(Root, j); err != nil {
				return err
			}
			if err := files.Copy(filepath.Join(j.dir(Root), string(c), "bin", name), filepath.Join("/usr/local/bin", name), 0755); err != nil {
				return err
			}
			if err := verifyHash(filepath.Join("/usr/local/bin", name), j.BinaryHashes[name]); err != nil {
				return err
			}
		}
	}
	return nil
}

func waitHealth(j *job, c protocol.Component, cfg *config.AppConfig) error {
	deadline := time.Now().Add(20 * time.Minute)
	for {
		state, err := host.CheckUpdateHealth(c, j.Review.Manifest.Artifact(c).Version, cfg, j.WalletPresent, j.SyncthingID)
		if err != nil {
			return fmt.Errorf("check %s: %w", c, err)
		}
		if state.Ready {
			if c == protocol.Syncthing {
				if err := host.StageSyncthingAPIKey(); err != nil {
					return err
				}
			}
			if c == protocol.LND {
				if err := host.StageLNDTLSCert(); err != nil {
					return err
				}
				if j.WalletPresent {
					if err := host.StageLNDMacaroon(); err != nil {
						return err
					}
				}
			}
			return nil
		}
		if state.Unlock {
			deadline = time.Now().Add(20 * time.Minute)
			if j.Phase != "waiting-unlock" {
				if err := host.StageLNDTLSCert(); err != nil {
					return err
				}
				j.Phase = "waiting-unlock"
				j.Step = "Unlock the LND wallet to finish"
				if err := saveJob(Root, j); err != nil {
					return err
				}
			}
		} else {
			if j.Phase == "waiting-unlock" {
				j.Phase = "checking"
				j.Step = "Check lnd"
				if err := saveJob(Root, j); err != nil {
					return err
				}
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("%s did not become ready; inspect its service journal before retrying", c)
			}
		}
		time.Sleep(2 * time.Second)
	}
}

// ValidateBuild checks that the plan can be executed by this worker.
func ValidateBuild(m protocol.Manifest, version string) error {
	if err := m.Validate(); err != nil {
		return err
	}
	if m.Version != version || m.Bitcoin.Version != component.BitcoinCoreVersion || m.LND.Version != component.LNDVersion || m.Syncthing.Version != component.SyncthingVersion {
		return errors.New("release plan differs from this VPN build's pinned versions")
	}
	for _, step := range m.HostSteps {
		if step != "lnd-service-v1" {
			return fmt.Errorf("this worker does not implement host step %q", step)
		}
	}
	return nil
}
