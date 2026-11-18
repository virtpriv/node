package update

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/virtpriv/node/internal/config"
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
	return runJob(j, workflowOps{
		save: func(j *job) error { return saveJob(Root, j) },
		check: func(j *job) error {
			return checkNode(j, func() error { _, err := config.Load(); return err }, paths.ConfigFile)
		},
		capacity: func(j *job) error {
			needs, err := spaceNeeds(Root, paths.BinaryPath, j)
			if err != nil {
				return err
			}
			return checkSpaceNeeds(needs, observeSpace)
		},
		commit: func(j *job) error { return commitJob(Root, paths.BinaryPath, j) },
	})
}

// checkNode is the update's first step. The node's settings must load, and
// must be the ones the owner reviewed the update for.
func checkNode(j *job, load func() error, configFile string) error {
	if err := load(); err != nil {
		return err
	}
	if err := verifyHash(configFile, j.ConfigHash); err != nil {
		return errors.New("node configuration changed during update")
	}
	return nil
}

// loadPlan reads the approved plan bytes in full. The installed side that
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

// commitJob replaces the installed program with the approved bytes. The copy
// leaves the old program or the whole new one, and the same bytes can be
// copied again.
func commitJob(root, binary string, j *job) error {
	if err := verifyHash(filepath.Join(j.dir(root), "vpn"), j.WorkerHash); err != nil {
		return err
	}
	if err := files.Copy(filepath.Join(j.dir(root), "vpn"), binary, 0755); err != nil {
		return err
	}
	return verifyHash(binary, j.WorkerHash)
}

func recordWorkerFailure(j *job, cause error) error {
	j.Phase = "failed"
	j.Error = cause.Error()
	j.Next, j.Advice = protocol.Advise(!j.HostChanges)
	// These checks run before this worker writes its list of steps, so the
	// list from an earlier run would mark the wrong step. Without a list the
	// screen shows the error under the saved step text.
	j.Steps, j.StepNumber, j.StepStarts, j.StepEnds = nil, 0, nil, nil
	return errors.Join(cause, saveJob(Root, j))
}

// A spaceNeed is what one path's filesystem must hold before the program is
// replaced, the free space that has to remain, plus the bytes still to be
// written there.
type spaceNeed struct {
	path         string
	floor, write uint64
}

// spaceNeeds asks for the plan's free space where the update's files are
// kept, and for room for the new program beside 1024 MiB where it is
// installed.
func spaceNeeds(root, binary string, j *job) ([]spaceNeed, error) {
	worker, err := os.Stat(filepath.Join(j.dir(root), "vpn"))
	if err != nil {
		return nil, err
	}
	return []spaceNeed{
		{path: root, floor: uint64(j.Review.Manifest.MinimumFreeMiB) << 20},
		{path: filepath.Dir(binary), floor: 1024 << 20, write: uint64(worker.Size())},
	}, nil
}

// checkSpaceNeeds counts each filesystem once, even when several paths share
// it. The largest floor plus all remaining writes must fit.
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
			return fmt.Errorf("at least %d MiB free space is required on %s, %d MiB is available", (required+1<<20-1)>>20, t.path, t.avail>>20)
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

// ValidateBuild checks that the plan can be executed by this worker: it is
// the plan of this release, and it names no change to the machine, since this
// release implements none.
func ValidateBuild(m protocol.Manifest, version string) error {
	if err := m.Validate(); err != nil {
		return err
	}
	if m.Version != version {
		return errors.New("release plan differs from this VPN build's version")
	}
	if len(m.HostSteps) > 0 {
		return fmt.Errorf("this worker does not implement host step %q", m.HostSteps[0])
	}
	return nil
}
