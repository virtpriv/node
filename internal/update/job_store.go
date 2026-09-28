package update

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/virtualprivatenode/vpn/internal/paths"
	"github.com/virtualprivatenode/vpn/internal/update/files"
	"github.com/virtualprivatenode/vpn/internal/update/protocol"
)

const Root = paths.PrivateDir + "/updates"
const LockPath = paths.RuntimeDir + "/operations.lock"
const Service = "vpn-update.service"

// A job records intent before each side effect. Completed is advisory: live
// files and service invocations are verified again after an interruption.
type job struct {
	Previous      string                      `json:"previous,omitempty"`
	Schema        int                         `json:"schema"`
	Review        protocol.Review             `json:"review"`
	WorkerHash    string                      `json:"worker_hash"`
	Phase         string                      `json:"phase"`
	Step          string                      `json:"step"`
	Error         string                      `json:"error,omitempty"`
	Updated       time.Time                   `json:"updated"`
	WalletPresent bool                        `json:"wallet_present"`
	SyncthingID   string                      `json:"syncthing_id,omitempty"`
	ConfigHash    string                      `json:"config_hash"`
	Started       map[protocol.Component]bool `json:"started"`
	MayHaveRun    map[protocol.Component]bool `json:"may_have_run"`
	Affected      []protocol.Component        `json:"affected"`
	Completed     map[string]bool             `json:"completed"`
	BinaryHashes  map[string]string           `json:"binary_hashes"`
}

func (j *job) active() bool           { return j.Phase != "complete" && j.Phase != "cancelled" }
func (j *job) dir(root string) string { return filepath.Join(root, j.Review.Digest) }

func readJSON(path string, result any) error {
	if err := files.CheckAncestors(filepath.Dir(path)); err != nil {
		return err
	}
	if err := files.Check(path, false); err != nil {
		return err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(b) > 1<<20 {
		return errors.New("update record exceeds size limit")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(result); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("update record has trailing data")
	}
	return nil
}

// InstalledVersion prevents a helper left in memory from applying older rules
// after the worker has published the new executable.
func InstalledVersion(version string) error {
	j, err := loadJob(Root)
	if err != nil {
		return err
	}
	if j != nil && j.Phase == "complete" && j.Review.Manifest.Version != version {
		return errors.New("VPN was updated; reconnect to its helper")
	}
	return nil
}

func writeJSON(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return files.Write(path, append(b, '\n'), 0600)
}

func loadJob(root string) (*job, error) {
	var j job
	err := readJSON(filepath.Join(root, "current.json"), &j)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if j.Schema != 1 || !protocol.ValidDigest(j.Review.Digest) || !protocol.ValidDigest(j.WorkerHash) || j.Review.Manifest.Validate() != nil || j.Started == nil || j.MayHaveRun == nil || j.Completed == nil || j.BinaryHashes == nil {
		return nil, errors.New("unsupported or invalid update record")
	}
	if !protocol.ValidDigest(j.ConfigHash) || j.Review.Manifest.Admit(j.Review.Source, j.Review.Network, "") != nil {
		return nil, errors.New("invalid update source or configuration record")
	}
	seen := map[protocol.Component]bool{}
	for _, c := range j.Affected {
		if !slices.Contains(protocol.Components, c) || seen[c] || (c == protocol.Syncthing && j.Review.Source.Syncthing == "") {
			return nil, errors.New("invalid affected component record")
		}
		seen[c] = true
	}
	for _, c := range j.Review.Manifest.Affected(j.Review.Source) {
		if !seen[c] {
			return nil, errors.New("update record omits an affected component")
		}
	}
	switch j.Phase {
	case "accepted", "staging", "installing", "starting", "checking", "waiting-unlock", "committing", "complete", "failed", "cancelled":
	default:
		return nil, errors.New("unknown update phase")
	}
	return &j, nil
}

func saveJob(root string, j *job) error {
	j.Updated = time.Now().UTC()
	return writeJSON(filepath.Join(root, "current.json"), j)
}

// MutationGuard is called while holding LockPath. An interrupted job continues
// to exclude conflicting writes even while no worker is running.
func MutationGuard() error {
	j, err := loadJob(Root)
	if err != nil {
		return err
	}
	if j != nil && j.active() {
		return errors.New("a managed update needs completion; open Node Updates")
	}
	return nil
}
