package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/virtpriv/node/internal/api"
	"github.com/virtpriv/node/internal/host"
	"github.com/virtpriv/node/internal/steps"
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
	r.State, r.Started = api.JobStarted, s.now().UTC()
	return r, k, s.write(r)
}

// steps runs a kind's steps through the shared runner. The job's record is
// the runner's sink: it gets the list, then each step's start and end. A job
// cannot be stopped, so its context never ends.
func (c *runCtx) steps(list ...steps.Step) error {
	r := c.r
	for _, s := range list {
		r.Steps = append(r.Steps, s.Name)
	}
	r.StepStarts, r.StepEnds = make([]time.Time, len(list)), make([]time.Time, len(list))
	return steps.Run(context.Background(), list, c).Err
}

// Begin, End and Log make the job the shared runner's sink.

func (c *runCtx) Begin(i int) error {
	c.r.StepNumber, c.r.StepStarts[i] = i+1, c.s.now().UTC()
	c.mark()
	return nil
}

func (c *runCtx) End(i int, _ steps.Outcome, _ error) error {
	c.r.StepEnds[i] = c.s.now().UTC()
	c.mark()
	return nil
}

// mark writes a step's start or end. A lost write leaves the record behind,
// never ahead, and does not end the job, since package work must not stop
// half way for it. The end is written either way.
func (c *runCtx) mark() {
	if err := c.s.write(*c.r); err != nil {
		fmt.Fprintf(os.Stderr, "job %s: record step: %v\n", c.r.ID, err)
	}
}

// Log writes a step's line to the journal, with the step's number.
func (c *runCtx) Log(i int, text string) {
	logJob(*c.r, "step", fmt.Sprintf("step %d/%d %s", i+1, len(c.r.Steps), text))
}

// finish writes the job's end.
func (c *runCtx) finish(result any, runErr error) error {
	r := c.r
	now := c.s.now().UTC()
	r.Ended = now
	if runErr != nil {
		r.State, r.Error = api.JobFailed, runErr.Error()
		r.NothingChanged = errors.Is(runErr, host.ErrNothingChanged)
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
