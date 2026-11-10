// Package jobs keeps the records of the changes vpnd starts, the busy rule
// that allows one change at a time, and the runner each job's own systemd
// service starts as `vpn run-job <id>`.
//
// A job's record is the truth about it. vpnd writes "accepted" before it
// starts the job's service, the job writes "started" before it changes
// anything, then each step and its end. Every write replaces the whole file
// in one synced step. A record left unfinished whose service is gone is
// reported as unknown, never as failed, and never retried.
//
// Records follow ADR-0011's field rule: a field keeps its meaning in every
// release, a new meaning gets a new field, readers ignore fields they do not
// know, and each release reads older releases' records.
package jobs

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/virtpriv/node/internal/api"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/update/files"
)

// schema rises only when an older reader must not read a record at all.
const schema = 1

// keepFinished is how many finished records stay.
const keepFinished = 100

// maxRecordBytes bounds one record read.
const maxRecordBytes = 1 << 20

// Record is a job's file. The job as the API shows it is embedded, so an
// answer is the record without what only root needs.
type Record struct {
	Schema int `json:"schema"`
	api.Job
	// Params are the kind's parameters as accepted. They never hold a
	// password.
	Params json.RawMessage `json:"params,omitempty"`
	// UID is the account that asked.
	UID uint32 `json:"uid"`
	// BootID is the boot the job was accepted in.
	BootID string `json:"boot_id"`
}

var validID = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}-[0-9a-f]{6}$`)

// ValidID reports whether id has the shape of a job ID.
func ValidID(id string) bool { return validID.MatchString(id) }

// newID is the job's start time and a random part, for example
// 20261109-183205-3f9a2c, so IDs sort by time.
func newID(now time.Time) (string, error) {
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return now.UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b[:]), nil
}

// UnitName is the systemd service a job runs in.
func UnitName(id string) string { return "vpn-job-" + id + ".service" }

// Store is the jobs folder and the lock that makes acceptance one step.
type Store struct {
	Dir      string
	LockPath string
	// unitAlive reports whether a job's service still runs.
	unitAlive func(id string) (bool, error)
	// bootID reads the current boot's ID.
	bootID func() (string, error)
	// nodeUpdate reports an unfinished Node Update and its job ID.
	nodeUpdate func() (bool, string, error)
	now        func() time.Time
}

// Default is the node's store.
func Default() *Store {
	return &Store{
		Dir: paths.JobsDir, LockPath: paths.OperationsLock,
		unitAlive: unitAlive, bootID: readBootID, nodeUpdate: unfinishedNodeUpdate,
		now: time.Now,
	}
}

func (s *Store) dir(id string) string    { return filepath.Join(s.Dir, id) }
func (s *Store) record(id string) string { return filepath.Join(s.dir(id), "record.json") }

// read reads one record. A record that cannot be read is an error, and the
// busy rule counts it as unfinished.
func (s *Store) read(id string) (Record, error) {
	var r Record
	path := s.record(id)
	if err := files.Check(path, false); err != nil {
		return r, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return r, err
	}
	if len(b) > maxRecordBytes {
		return r, errors.New("job record exceeds size limit")
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return r, err
	}
	if r.Schema != schema || r.ID != id || r.Kind == "" || r.State == "" {
		return r, errors.New("unsupported or invalid job record")
	}
	return r, nil
}

func (s *Store) write(r Record) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return files.Write(s.record(r.ID), append(b, '\n'), 0600)
}

// ids lists the job folders, oldest first.
func (s *Store) ids() ([]string, error) {
	entries, err := os.ReadDir(s.Dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() && ValidID(e.Name()) {
			ids = append(ids, e.Name())
		}
	}
	slices.Sort(ids)
	return ids, nil
}

// create makes a new job's folder exclusively and writes its accepted
// record. A clash of IDs gets a new ID.
func (s *Store) create(r Record) (Record, error) {
	if err := files.Directory(s.Dir, 0700); err != nil {
		return r, err
	}
	for range 8 {
		id, err := newID(r.Accepted)
		if err != nil {
			return r, err
		}
		err = os.Mkdir(s.dir(id), 0700)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return r, err
		}
		if err := files.Sync(s.Dir); err != nil {
			return r, err
		}
		r.ID = id
		return r, s.write(r)
	}
	return r, errors.New("could not choose a new job ID")
}

func (s *Store) remove(id string) error {
	if !ValidID(id) {
		return fmt.Errorf("invalid job ID %q", id)
	}
	if err := os.RemoveAll(s.dir(id)); err != nil {
		return err
	}
	return files.Sync(s.Dir)
}

func readBootID() (string, error) {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(b))
	if id == "" {
		return "", errors.New("empty boot ID")
	}
	return id, nil
}
