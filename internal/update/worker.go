package update

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/virtpriv/node/internal/component"
	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/host"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/update/files"
	"github.com/virtpriv/node/internal/update/protocol"
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
	plan, err := loadPlan(Root, j)
	if err != nil {
		return recordWorkerFailure(j, err)
	}
	j.Review.Manifest = plan
	if err := ValidateBuild(plan, version); err != nil {
		return recordWorkerFailure(j, err)
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if err := verifyHash(self, j.WorkerHash); err != nil {
		return recordWorkerFailure(j, err)
	}
	if err := inheritFailed(Root, j); err != nil {
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
		save:  func(j *job) error { return saveJob(Root, j) },
		stage: func(j *job) error { return stageJob(j) },
		capacity: func(j *job) error {
			needs, err := spaceNeeds(Root, paths.BinaryPath, j)
			if err != nil {
				return err
			}
			return checkSpaceNeeds(needs, observeSpace)
		},
		discard: func(j *job) error { return discardStaged(Root, j) },
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

// loadPlan reads the approved plan bytes in full. The installed helper that
// accepted the job may be older and may have understood only part of the plan.
func loadPlan(root string, j *job) (protocol.Manifest, error) {
	path := filepath.Join(j.dir(root), "update.json")
	if err := files.Check(path, false); err != nil {
		return protocol.Manifest{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return protocol.Manifest{}, err
	}
	if sum := sha256.Sum256(data); !protocol.ValidDigest(j.PlanHash) || hex.EncodeToString(sum[:]) != j.PlanHash {
		return protocol.Manifest{}, errors.New("retained update file failed integrity verification")
	}
	return protocol.Decode(data)
}

// inheritFailed gives a repair job the duties of the failed update it follows:
// the services that update affected, what may already have run, and the wallet
// and device identity recorded before any change. The helper retained each
// failed record as result.json. A failed repair whose worker never ran has not
// merged its own predecessor, so the chain is followed until one has.
func inheritFailed(root string, j *job) error {
	if j.Previous == "" || j.Completed["inherited"] {
		return nil
	}
	affected, mayHaveRun := slices.Clone(j.Affected), maps.Clone(j.MayHaveRun)
	wallet, device, hostChanges := j.WalletPresent, j.SyncthingID, j.HostChanges
	seen := map[string]bool{j.Review.Digest: true}
	for id := j.Previous; id != ""; {
		if !protocol.ValidDigest(id) || seen[id] {
			return errors.New("invalid failed update history")
		}
		seen[id] = true
		b, err := readRecord(filepath.Join(root, id, "result.json"))
		if err != nil {
			return fmt.Errorf("read failed update record: %w", err)
		}
		var failed struct {
			Previous      string                      `json:"previous"`
			Affected      []protocol.Component        `json:"affected"`
			MayHaveRun    map[protocol.Component]bool `json:"may_have_run"`
			Completed     map[string]bool             `json:"completed"`
			WalletPresent bool                        `json:"wallet_present"`
			SyncthingID   string                      `json:"syncthing_id"`
			HostChanges   bool                        `json:"host_changes_begun"`
		}
		if err := json.Unmarshal(b, &failed); err != nil {
			return fmt.Errorf("read failed update record: %w", err)
		}
		for _, c := range append(slices.Collect(maps.Keys(failed.MayHaveRun)), failed.Affected...) {
			if !slices.Contains(protocol.Components, c) {
				return fmt.Errorf("failed update involved %q, which this release cannot repair", c)
			}
		}
		for _, c := range failed.Affected {
			if !slices.Contains(affected, c) {
				affected = append(affected, c)
			}
		}
		for c, ran := range failed.MayHaveRun {
			if ran {
				mayHaveRun[c] = true
			}
		}
		wallet, hostChanges = wallet || failed.WalletPresent, hostChanges || failed.HostChanges
		if device == "" {
			device = failed.SyncthingID
		}
		if failed.Completed["inherited"] {
			break
		}
		id = failed.Previous
	}
	slices.SortFunc(affected, func(a, b protocol.Component) int {
		return slices.Index(protocol.Components, a) - slices.Index(protocol.Components, b)
	})
	j.Affected, j.MayHaveRun, j.WalletPresent, j.SyncthingID, j.HostChanges = affected, mayHaveRun, wallet, device, hostChanges
	j.Completed["inherited"] = true
	return nil
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

const installDir = "/usr/local/bin"

// A spaceNeed is what one path's filesystem must hold before services stop:
// the free space that has to remain, plus the bytes still to be written there.
type spaceNeed struct {
	path         string
	floor, write uint64
}

func dataDir(c protocol.Component) string {
	return map[protocol.Component]string{protocol.Bitcoin: paths.BitcoinDataDir, protocol.LND: paths.LNDDataDir, protocol.Syncthing: paths.SyncthingDataDir}[c]
}

func floorNeeds(root string, j *job) []spaceNeed {
	floor := uint64(j.Review.Manifest.MinimumFreeMiB) << 20
	needs := []spaceNeed{{path: root, floor: floor}, {path: installDir, floor: 1024 << 20}}
	for _, c := range j.Affected {
		needs = append(needs, spaceNeed{path: dataDir(c), floor: floor})
	}
	return needs
}

// spaceNeeds measures the staged files, so it covers every byte installation
// and VPN publication will write after the services stop.
func spaceNeeds(root, binary string, j *job) ([]spaceNeed, error) {
	var install uint64
	for _, c := range j.Affected {
		for _, name := range host.UpdateBinaryNames(c) {
			if _, ok := j.BinaryHashes[name]; !ok {
				continue
			}
			i, err := os.Stat(filepath.Join(j.dir(root), string(c), "bin", name))
			if err != nil {
				return nil, err
			}
			install += uint64(i.Size())
		}
	}
	worker, err := os.Stat(filepath.Join(j.dir(root), "vpn"))
	if err != nil {
		return nil, err
	}
	return append(floorNeeds(root, j), spaceNeed{path: installDir, write: install}, spaceNeed{path: filepath.Dir(binary), write: uint64(worker.Size())}), nil
}

// checkSpaceNeeds counts each filesystem once, even when several paths share
// it: the largest floor plus all remaining writes must fit.
func checkSpaceNeeds(needs []spaceNeed, observe func(path string) (dev, avail uint64, err error)) error {
	type total struct {
		spaceNeed
		avail uint64
	}
	var totals []*total
	byDev := map[uint64]*total{}
	for _, n := range needs {
		dev, avail, err := observe(n.path)
		if err != nil {
			return fmt.Errorf("check free space on %s: %w", n.path, err)
		}
		t := byDev[dev]
		if t == nil {
			t = &total{spaceNeed: spaceNeed{path: n.path}, avail: avail}
			byDev[dev] = t
			totals = append(totals, t)
		}
		t.floor, t.write, t.avail = max(t.floor, n.floor), t.write+n.write, min(t.avail, avail)
	}
	for _, t := range totals {
		if required := t.floor + t.write; t.avail < required {
			return fmt.Errorf("at least %d MiB free space is required on %s; %d MiB is available", (required+1<<20-1)>>20, t.path, t.avail>>20)
		}
	}
	return nil
}

func observeSpace(path string) (dev, avail uint64, err error) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return 0, 0, err
	}
	var fs unix.Statfs_t
	if err := unix.Statfs(path, &fs); err != nil {
		return 0, 0, err
	}
	return uint64(st.Dev), uint64(fs.Bavail) * uint64(fs.Bsize), nil
}

// discardStaged removes the downloaded component files. The worker, plan and
// job records stay for status and inspection.
func discardStaged(root string, j *job) error {
	if err := files.Check(j.dir(root), true); err != nil {
		return err
	}
	var err error
	for _, c := range protocol.Components {
		err = errors.Join(err, os.RemoveAll(filepath.Join(j.dir(root), string(c))))
	}
	return errors.Join(err, files.Sync(j.dir(root)))
}

func stageJob(j *job) error {
	// Refuse early, before downloading anything.
	if err := checkSpaceNeeds(floorNeeds(Root, j), observeSpace); err != nil {
		return err
	}
	// Every affected component is staged again, so earlier entries are stale.
	clear(j.BinaryHashes)
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
			if err := files.Copy(filepath.Join(j.dir(Root), string(c), "bin", name), filepath.Join(installDir, name), 0755); err != nil {
				return err
			}
			if err := verifyHash(filepath.Join(installDir, name), j.BinaryHashes[name]); err != nil {
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
