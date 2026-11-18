package update

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/release"
	"github.com/virtpriv/node/internal/update/files"
	"github.com/virtpriv/node/internal/update/protocol"
)

const Root = paths.PrivateDir + "/updates"
const LockPath = paths.OperationsLock
const Service = protocol.WorkerUnit

// jobSchema is the number of the update's record, current.json. Number 1 was
// the record of an update of the Bitcoin and Lightning node.
const jobSchema = 2

// A record is the part of current.json that the installed side and the
// retained launcher depend on. Both stay in service for every later release
// that admits this one, and that release's worker may add fields, phases and
// steps. So they read only these fields, treat every phase other than failed,
// complete and canceled as work in progress, and keep the rest of the file
// when they write. Removing or redefining one of these fields breaks nodes
// that are already installed. Steps, StepNumber, StepStarts, StepEnds, Next
// and Advice are only for the screen, so they are kept undecoded here and a
// value of another shape is ignored instead of making the record unreadable.
type record struct {
	Schema int `json:"schema"`
	Review struct {
		Digest   string `json:"digest"`
		Manifest struct {
			Version string `json:"version"`
		} `json:"manifest"`
	} `json:"review"`
	// JobID is the Node Update's job ID in the vpn API, saved at Start.
	JobID       string          `json:"job_id"`
	WorkerHash  string          `json:"worker_hash"`
	ConfigHash  string          `json:"config_hash"`
	Phase       string          `json:"phase"`
	Step        string          `json:"step"`
	Steps       json.RawMessage `json:"steps"`
	StepNumber  json.RawMessage `json:"step_number"`
	StepStarts  json.RawMessage `json:"step_starts"`
	StepEnds    json.RawMessage `json:"step_ends"`
	Error       string          `json:"error"`
	Next        json.RawMessage `json:"next"`
	Advice      json.RawMessage `json:"advice"`
	HostChanges bool            `json:"host_changes_begun"`
}

func (r *record) active() bool           { return r.Phase != "complete" && r.Phase != "canceled" }
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
	if r.Schema != jobSchema || r.Phase == "" || !protocol.ValidDigest(r.Review.Digest) || !protocol.ValidDigest(r.WorkerHash) || !protocol.ValidDigest(r.ConfigHash) || !release.ValidVersion(r.Review.Manifest.Version) {
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

// A job is the worker's full view of the same record. It records intent
// before the program is replaced.
type job struct {
	// Previous is the failed update a repair release follows. The installed
	// side keeps that update's record as result.json in its folder.
	Previous string `json:"previous,omitempty"`
	Schema   int    `json:"schema"`
	// JobID, Accepted and Ended are the Node Update as a job of the vpn API:
	// its ID and when Start accepted it, written by the installed side, and
	// when it completed or was canceled.
	JobID      string          `json:"job_id,omitempty"`
	Accepted   time.Time       `json:"accepted,omitzero"`
	Ended      time.Time       `json:"ended,omitzero"`
	Review     protocol.Review `json:"review"`
	WorkerHash string          `json:"worker_hash"`
	PlanHash   string          `json:"plan_hash"`
	Phase      string          `json:"phase"`
	Step       string          `json:"step"`
	// Steps is the list Node Updates draws, written by the worker at the start
	// of each run. StepNumber counts from 1 and names the step in progress.
	// StepStarts and StepEnds hold when each step began and when it finished
	// or failed. Next and Advice are the screen's next step and words for a
	// failure, saved and cleared with Error.
	Steps      []string      `json:"steps,omitempty"`
	StepNumber int           `json:"step_number,omitempty"`
	StepStarts []time.Time   `json:"step_starts,omitempty"`
	StepEnds   []time.Time   `json:"step_ends,omitempty"`
	Error      string        `json:"error,omitempty"`
	Next       protocol.Next `json:"next,omitempty"`
	Advice     string        `json:"advice,omitempty"`
	Updated    time.Time     `json:"updated"`
	ConfigHash string        `json:"config_hash"`
	// HostChanges is saved before the program is replaced and never cleared.
	// The installed side refuses Cancel once it is set.
	HostChanges bool `json:"host_changes_begun"`
}

func (j *job) active() bool           { return j.Phase != "complete" && j.Phase != "canceled" }
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

// InstalledVersion prevents a vpnd left in memory from applying older rules
// after the worker has published the new executable.
func InstalledVersion(version string) error {
	j, err := loadRecord(Root)
	if err != nil {
		return err
	}
	if j != nil && j.Phase == "complete" && j.Review.Manifest.Version != version {
		return errors.New("VPN was updated. Quit and run vpn to open it again.")
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
	if j.Schema != jobSchema || !protocol.ValidDigest(j.Review.Digest) || !protocol.ValidDigest(j.WorkerHash) || !protocol.ValidDigest(j.PlanHash) || j.Review.Manifest.Validate() != nil {
		return nil, errors.New("unsupported or invalid update record")
	}
	if !protocol.ValidDigest(j.ConfigHash) || j.Review.Manifest.Admit(j.Review.Source, "") != nil {
		return nil, errors.New("invalid update source or configuration record")
	}
	switch j.Phase {
	case "accepted", "retry", "committing", "complete", "failed", "canceled":
	default:
		return nil, errors.New("unknown update phase")
	}
	return &j, nil
}

func saveJob(root string, j *job) error {
	j.Updated = time.Now().UTC()
	return writeJSON(filepath.Join(root, "current.json"), j)
}

// Unfinished reports an unfinished Node Update and its job ID, for the vpn
// API's busy rule. An interrupted update keeps other changes out even while
// no worker runs.
func Unfinished() (bool, string, error) {
	j, err := loadRecord(Root)
	if err != nil || j == nil {
		return false, "", err
	}
	return j.active(), j.JobID, nil
}
