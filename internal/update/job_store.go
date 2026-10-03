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
	"github.com/virtualprivatenode/vpn/internal/release"
	"github.com/virtualprivatenode/vpn/internal/update/files"
	"github.com/virtualprivatenode/vpn/internal/update/protocol"
)

const Root = paths.PrivateDir + "/updates"
const LockPath = paths.RuntimeDir + "/operations.lock"
const Service = "vpn-update.service"

// A record is the part of current.json that the installed helper and the
// retained launcher depend on. Both stay in service for every later release
// that admits this one, and that release's worker may add fields, phases and
// components. So they read only these fields, treat every phase other than
// failed, complete, cancelled and waiting-unlock as work in progress, and keep
// the rest of the file when they write. Removing or redefining one of these
// fields breaks nodes that are already installed.
type record struct {
	Schema int `json:"schema"`
	Review struct {
		Digest   string `json:"digest"`
		Manifest struct {
			Version string `json:"version"`
		} `json:"manifest"`
		Network string `json:"network"`
	} `json:"review"`
	WorkerHash  string `json:"worker_hash"`
	ConfigHash  string `json:"config_hash"`
	Phase       string `json:"phase"`
	Step        string `json:"step"`
	Error       string `json:"error"`
	HostChanges bool   `json:"host_changes_begun"`
}

func (r *record) active() bool           { return r.Phase != "complete" && r.Phase != "cancelled" }
func (r *record) dir(root string) string { return filepath.Join(root, r.Review.Digest) }

// relaunch reports whether the launcher should hand the job to its worker.
// A failed job waits for an explicit Retry.
func (r *record) relaunch() bool { return r.active() && r.Phase != "failed" }

func loadRecord(root string) (*record, error) {
	b, err := readRecord(filepath.Join(root, "current.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r record
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	if r.Schema != 1 || r.Phase == "" || !protocol.ValidDigest(r.Review.Digest) || !protocol.ValidDigest(r.WorkerHash) || !protocol.ValidDigest(r.ConfigHash) || !release.ValidVersion(r.Review.Manifest.Version) {
		return nil, errors.New("unsupported or invalid update record")
	}
	return &r, nil
}

// amendRecord changes only the named fields and keeps every other field as
// the worker wrote it. A nil value removes the field.
func amendRecord(root string, set map[string]any) error {
	path := filepath.Join(root, "current.json")
	b, err := readRecord(path)
	if err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	for name, value := range set {
		if value == nil {
			delete(raw, name)
			continue
		}
		if raw[name], err = json.Marshal(value); err != nil {
			return err
		}
	}
	if raw["updated"], err = json.Marshal(time.Now().UTC()); err != nil {
		return err
	}
	return writeJSON(path, raw)
}

// A job is the worker's full view of the same record. It records intent before
// each side effect. Completed is advisory: live files and service invocations
// are verified again after an interruption.
type job struct {
	Previous      string                      `json:"previous,omitempty"`
	Schema        int                         `json:"schema"`
	Review        protocol.Review             `json:"review"`
	WorkerHash    string                      `json:"worker_hash"`
	PlanHash      string                      `json:"plan_hash"`
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
	// HostChanges is saved before the first service guard and never cleared.
	// The installed helper refuses Cancel once it is set.
	HostChanges bool `json:"host_changes_begun"`
}

func (j *job) active() bool           { return j.Phase != "complete" && j.Phase != "cancelled" }
func (j *job) dir(root string) string { return filepath.Join(root, j.Review.Digest) }

func readRecord(path string) ([]byte, error) {
	if err := files.CheckAncestors(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if err := files.Check(path, false); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) > 1<<20 {
		return nil, errors.New("update record exceeds size limit")
	}
	return b, nil
}

// readJSON is the strict reader for records this build wrote or fully owns.
func readJSON(path string, result any) error {
	b, err := readRecord(path)
	if err != nil {
		return err
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
	j, err := loadRecord(Root)
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

// loadJob is the worker's strict reader. The worker is the newest code in a
// job and must understand all of its record before it changes the host.
func loadJob(root string) (*job, error) {
	var j job
	err := readJSON(filepath.Join(root, "current.json"), &j)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if j.Schema != 1 || !protocol.ValidDigest(j.Review.Digest) || !protocol.ValidDigest(j.WorkerHash) || !protocol.ValidDigest(j.PlanHash) || j.Review.Manifest.Validate() != nil || j.Started == nil || j.MayHaveRun == nil || j.Completed == nil || j.BinaryHashes == nil {
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
	case "accepted", "retry", "staging", "installing", "starting", "checking", "waiting-unlock", "committing", "complete", "failed", "cancelled":
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
	j, err := loadRecord(Root)
	if err != nil {
		return err
	}
	if j != nil && j.active() {
		return errors.New("a managed update needs completion; open Node Updates")
	}
	return nil
}
