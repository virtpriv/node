package jobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/virtpriv/node/internal/api"
	"github.com/virtpriv/node/internal/host"
	"github.com/virtpriv/node/internal/update/files"
)

// runCtx is what a running job works with.
type runCtx struct {
	s       *Store
	r       *Record
	version string
}

// Run is `vpn run-job <id>`, started by vpnd in the job's own systemd
// service. version is the running program's version.
func Run(id, version string) error {
	if os.Geteuid() != 0 {
		return errors.New("vpn run-job runs as root in a job service vpnd starts, it is not meant to be started by hand")
	}
	// Package jobs run apt without questions, as the install does.
	os.Setenv("DEBIAN_FRONTEND", "noninteractive")
	if !ValidID(id) {
		return fmt.Errorf("invalid job ID %q", id)
	}
	s := Default()
	r, k, err := s.markStarted(id)
	if err != nil {
		return err
	}
	logJob(r, "started", "")
	c := &runCtx{s: s, r: &r, version: version}
	result, err := k.run(c, r.Params)
	return c.finish(result, err)
}

// markStarted is a job's first act: it writes "started" before anything
// changes, so an accepted record whose service is gone means nothing changed.
func (s *Store) markStarted(id string) (Record, kind, error) {
	unlock, err := files.Lock(s.LockPath)
	if err != nil {
		return Record{}, kind{}, err
	}
	defer unlock()
	r, err := s.read(id)
	if err != nil {
		return r, kind{}, err
	}
	if r.State != api.JobAccepted {
		return r, kind{}, fmt.Errorf("job %s is %s, not waiting to start", id, r.State)
	}
	boot, err := s.bootID()
	if err != nil {
		return r, kind{}, err
	}
	k, ok := kinds[r.Kind]
	if !ok || boot != r.BootID {
		r.State, r.NothingChanged, r.Ended = api.JobFailed, true, s.now().UTC()
		r.Error = "this job cannot start here"
		if !ok {
			r.Error = fmt.Sprintf("unknown job kind %q", r.Kind)
		}
		if err := s.write(r); err != nil {
			return r, kind{}, err
		}
		return r, kind{}, errors.New(r.Error)
	}
	now := s.now().UTC()
	r.State, r.Started = api.JobStarted, now
	if k.steps != nil {
		r.Steps = k.steps(r.Params)
		r.StepNumber = 1
		r.StepStarts = []time.Time{now}
		r.StepEnds = []time.Time{{}}
	}
	return r, k, s.write(r)
}

// step records that step i ended and the next one began. The host's
// operations report a step when it completes.
func (c *runCtx) step(i int) {
	r := c.r
	if i < 0 || i >= len(r.Steps) {
		return
	}
	now := c.s.now().UTC()
	for len(r.StepEnds) <= i {
		r.StepEnds = append(r.StepEnds, time.Time{})
	}
	r.StepEnds[i] = now
	if i+1 < len(r.Steps) {
		r.StepNumber = i + 2
		for len(r.StepStarts) <= i+1 {
			r.StepStarts = append(r.StepStarts, time.Time{})
		}
		r.StepStarts[i+1] = now
	}
	// A lost progress write leaves the record behind, never ahead. The end
	// is written either way.
	if err := c.s.write(*r); err != nil {
		fmt.Fprintf(os.Stderr, "job %s: record step: %v\n", r.ID, err)
	}
}

// finish writes the job's end.
func (c *runCtx) finish(result any, runErr error) error {
	r := c.r
	now := c.s.now().UTC()
	r.Ended = now
	if runErr != nil {
		r.State, r.Error = api.JobFailed, runErr.Error()
		r.NothingChanged = errors.Is(runErr, host.ErrNothingChanged)
		// The step in progress ends where the job failed.
		if n := r.StepNumber; n >= 1 && n <= len(r.Steps) {
			for len(r.StepEnds) < n {
				r.StepEnds = append(r.StepEnds, time.Time{})
			}
			if r.StepEnds[n-1].IsZero() {
				r.StepEnds[n-1] = now
			}
		}
	} else {
		r.State = api.JobDone
	}
	if result != nil {
		raw, err := json.Marshal(result)
		if err != nil {
			return err
		}
		r.Result = raw
	}
	if err := c.s.write(*r); err != nil {
		return err
	}
	logJob(*r, string(r.State), r.Error)
	if runErr != nil {
		return runErr
	}
	return nil
}

// logJob writes one journal line. Job services log as vpnd, so
// journalctl -t vpnd shows vpnd and every job. Parameters are never logged.
func logJob(r Record, event, detail string) {
	line, _ := json.Marshal(map[string]string{
		"event": "job-" + event, "id": r.ID, "kind": r.Kind, "detail": detail,
	})
	prefix := ""
	if event == string(api.JobFailed) {
		prefix = "<3>"
	}
	fmt.Fprintf(os.Stdout, "%s%s\n", prefix, line)
}
