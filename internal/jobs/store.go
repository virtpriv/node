package jobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/virtpriv/node/internal/api"
	"github.com/virtpriv/node/internal/update/files"
)

// Rule says what counts as busy for a kind.
type Rule int

const (
	// RuleAll refuses while any job record or a Node Update is unfinished.
	RuleAll Rule = iota
	// RuleJobs refuses only while a job record is unfinished. Prepare Update,
	// Start and Retry handle an unfinished Node Update themselves.
	RuleJobs
)

// BusyError refuses a change while another job is unfinished. Job names the
// job in the way.
type BusyError struct {
	Job        api.Job
	NodeUpdate bool
	// Unreadable is set when the record in the way cannot be read.
	Unreadable bool
}

func (e *BusyError) Error() string {
	id := e.Job.ID
	if id == "" {
		id = "unknown"
	}
	switch {
	case e.NodeUpdate:
		return fmt.Sprintf("A Node Update is unfinished (job %s). Nothing was changed. Open Node Updates.", id)
	case e.Unreadable:
		return fmt.Sprintf("The record of job %s cannot be read, so it counts as running. Nothing was changed.", id)
	}
	return fmt.Sprintf("%s is running (job %s). Nothing was changed.", e.Job.Title, id)
}

// Accept records a new job and starts it, one change at a time. It refuses
// while the rule finds an unfinished job. start runs systemd-run with the lock
// held, so a reader that finds an accepted record without its service while
// the lock is held knows the job is still starting.
func (s *Store) Accept(kind, title string, params json.RawMessage, uid uint32, rule Rule, start func(id string) error) (api.Job, error) {
	// A first look without the lock, so a refusal names the job in the way
	// even while the update worker holds the lock for its run.
	if err := s.busy(rule, false); err != nil {
		return api.Job{}, err
	}
	unlock, err := files.Lock(s.LockPath)
	if err != nil {
		// The update worker holds the lock for its run. Name its update.
		if active, id, uerr := s.nodeUpdate(); uerr == nil && active {
			return api.Job{}, &BusyError{Job: api.Job{ID: id, Kind: api.KindNodeUpdate}, NodeUpdate: true}
		}
		return api.Job{}, err
	}
	defer unlock()
	if err := s.busy(rule, true); err != nil {
		return api.Job{}, err
	}
	boot, err := s.bootID()
	if err != nil {
		return api.Job{}, err
	}
	if err := s.prune(); err != nil {
		return api.Job{}, err
	}
	r, err := s.create(Record{
		Schema: schema, Params: params, UID: uid, BootID: boot,
		Job: api.Job{Kind: kind, Title: title, State: api.JobAccepted, Accepted: s.now().UTC()},
	})
	if err != nil {
		if r.ID != "" {
			s.remove(r.ID)
		}
		return api.Job{}, err
	}
	if startErr := start(r.ID); startErr != nil {
		// systemd-run can fail after the service began. A running job owns
		// its record from here.
		if alive, err := s.unitAlive(r.ID); err == nil && alive {
			return r.Job, nil
		}
		r.State, r.NothingChanged, r.Ended = api.JobFailed, true, s.now().UTC()
		r.Error = "the job could not be started: " + startErr.Error()
		if err := s.write(r); err != nil {
			return api.Job{}, fmt.Errorf("%s, and its record was not saved: %w", r.Error, err)
		}
		return api.Job{}, errors.New(r.Error)
	}
	return r.Job, nil
}

// busy applies the busy rule. locked says the caller holds the lock, so
// what an unfinished record means is written down.
func (s *Store) busy(rule Rule, locked bool) error {
	ids, err := s.ids()
	if err != nil {
		return err
	}
	for _, id := range ids {
		r, err := s.current(id, locked)
		if os.IsNotExist(err) {
			// A folder whose record was never written: vpnd stopped between
			// making the folder and writing its accepted record, so the job
			// never started. With the lock held it is removed. Without the
			// lock it may be a job being accepted right now, which the check
			// under the lock sees.
			if locked {
				if err := s.remove(id); err != nil {
					return err
				}
			}
			continue
		}
		if err != nil {
			// An unreadable record counts as unfinished.
			return &BusyError{Job: api.Job{ID: id}, Unreadable: true}
		}
		if !r.Finished() {
			return &BusyError{Job: r.Job}
		}
	}
	if rule == RuleJobs {
		return nil
	}
	active, id, err := s.nodeUpdate()
	if err != nil {
		return fmt.Errorf("read the Node Update record: %w", err)
	}
	if active {
		return &BusyError{Job: api.Job{ID: id, Kind: api.KindNodeUpdate}, NodeUpdate: true}
	}
	return nil
}

// current reads a record and settles what an unfinished one means now.
// locked says the caller holds the lock. A reader without it takes the lock
// only if it is free and never waits.
func (s *Store) current(id string, locked bool) (Record, error) {
	r, err := s.read(id)
	if err != nil || (r.Finished() && (r.Kind != api.KindReboot || r.Restarted)) {
		return r, err
	}
	boot, err := s.bootID()
	if err != nil {
		return r, err
	}
	if r.Finished() && r.BootID == boot {
		return r, nil
	}
	held := false
	if !locked {
		unlock, other, err := files.TryLock(s.LockPath)
		if err != nil {
			return r, err
		}
		if other {
			held = true
		} else {
			defer unlock()
		}
	}
	// The service is looked at before the record is read again: a job writes
	// its end before its service ends, so an unfinished record read after its
	// service is gone was left by a job that died. With the lock held, an
	// accepted record cannot become started meanwhile, so what is seen here
	// stays true until the write below.
	alive, err := s.unitAlive(id)
	if err != nil {
		return r, err
	}
	if r, err = s.read(id); err != nil || (r.Finished() && (r.Kind != api.KindReboot || r.Restarted)) {
		return r, err
	}
	settled, changed := resolve(r, boot, alive, held, s.now().UTC())
	if changed && !held {
		// Written once, never retried. A later reader settles it the same way.
		if err := s.write(settled); err != nil {
			return settled, nil
		}
	}
	return settled, nil
}

// resolve says what an unfinished record means: still running, still
// starting, failed with nothing changed, unknown, or a Reboot that happened.
// lockHeld says another holder has the lock, which means vpnd is still
// starting an accepted job.
func resolve(r Record, boot string, alive, lockHeld bool, now time.Time) (Record, bool) {
	if r.Kind == api.KindReboot && r.Finished() && !r.Restarted && !r.NothingChanged && r.BootID != boot {
		// The node restarted after a Reboot that may have asked for it: one
		// that finished its request before the restart stopped it, or that
		// a reader settled as unknown or failed during the shutdown.
		r.State, r.Restarted, r.Error = api.JobDone, true, ""
		if r.Ended.IsZero() {
			r.Ended = now
		}
		return r, true
	}
	if r.Finished() {
		return r, false
	}
	if r.BootID == boot {
		if alive || (r.State == api.JobAccepted && lockHeld) {
			return r, false
		}
	}
	r.Ended = now
	switch {
	case r.Kind == api.KindReboot && r.BootID != boot:
		// Decision 2: the node restarted after a Reboot was asked for.
		r.State, r.Restarted = api.JobDone, true
	case r.State == api.JobAccepted:
		// A job writes "started" before it changes anything.
		r.State, r.NothingChanged = api.JobFailed, true
		r.Error = "the job did not start"
	default:
		r.State = api.JobUnknown
	}
	return r, true
}

// prune removes the oldest finished records beyond keepFinished. It runs
// with the lock held, before a new job is recorded.
func (s *Store) prune() error {
	ids, err := s.ids()
	if err != nil {
		return err
	}
	var records []Record
	for _, id := range ids {
		if r, err := s.read(id); err == nil {
			records = append(records, r)
		}
	}
	for _, id := range prune(records, keepFinished-1) {
		if err := s.remove(id); err != nil {
			return err
		}
	}
	return nil
}

// prune lists the finished records beyond the newest keep, oldest first.
// An unfinished record is never removed.
func prune(records []Record, keep int) []string {
	var finished []string
	for _, r := range records {
		if r.Finished() {
			finished = append(finished, r.ID)
		}
	}
	slices.Sort(finished) // IDs sort by time
	if len(finished) <= keep {
		return nil
	}
	return finished[:len(finished)-keep]
}

// Get reads one job, settled as of now.
func (s *Store) Get(id string) (api.Job, error) {
	if !ValidID(id) {
		return api.Job{}, fmt.Errorf("invalid job ID %q", id)
	}
	r, err := s.current(id, false)
	if os.IsNotExist(err) {
		return api.Job{}, fmt.Errorf("no job %s", id)
	}
	return r.Job, err
}

// Latest reads the newest job of a kind. found is false when there is none.
func (s *Store) Latest(kind string) (api.Job, bool, error) {
	ids, err := s.ids()
	if err != nil {
		return api.Job{}, false, err
	}
	// IDs follow the clock, which a restart can set back, so an unfinished
	// job of the kind comes first, then the newest by ID.
	newest := ""
	for _, id := range slices.Backward(ids) {
		r, err := s.read(id)
		if err != nil || r.Kind != kind {
			continue
		}
		if newest == "" {
			newest = id
		}
		if !r.Finished() {
			newest = id
			break
		}
	}
	if newest == "" {
		return api.Job{}, false, nil
	}
	r, err := s.current(newest, false)
	if err != nil {
		return api.Job{}, false, err
	}
	return r.Job, true, nil
}

// NewID gives a Node Update its job ID at Start.
func NewID() (string, error) { return newID(time.Now()) }

// CheckBusy applies the busy rule for a caller that holds the operations
// lock, such as Start and Retry of a Node Update.
func (s *Store) CheckBusy(rule Rule) error { return s.busy(rule, true) }
