package app

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/virtpriv/node/internal/api"
)

// JobOutcome is what a screen may say about a job, under UX-012's freeze:
// an unknown outcome stays unknown and nothing is retried.
type JobOutcome int

const (
	// JobRunning: the job has not ended.
	JobRunning JobOutcome = iota
	// JobCompleted: done.
	JobCompleted
	// JobNotStarted: refused or failed before it changed anything.
	JobNotStarted
	// JobFailed: failed, and something may have changed.
	JobFailed
	// JobUnconfirmed: ended without a result, or the answer was lost.
	JobUnconfirmed
	// JobRestarted: a Reboot whose node restarted.
	JobRestarted
)

// Outcome says what a job's record means for a screen.
func Outcome(j api.Job) JobOutcome {
	switch j.State {
	case api.JobDone:
		if j.Restarted {
			return JobRestarted
		}
		return JobCompleted
	case api.JobFailed:
		if j.NothingChanged {
			return JobNotStarted
		}
		return JobFailed
	case api.JobUnknown:
		return JobUnconfirmed
	}
	return JobRunning
}

// JobUpdate is one look at a watched job. Final is set on the last one.
// Err is set when vpnd refused the job or could not be read, and then Job
// may be empty.
type JobUpdate struct {
	Job     api.Job
	Outcome JobOutcome
	Final   bool
	Err     error
}

// Error is nil for a completed job, else what went wrong.
func (u JobUpdate) Error() error {
	switch {
	case u.Outcome == JobCompleted || u.Outcome == JobRestarted:
		return nil
	case u.Err != nil:
		return u.Err
	case u.Job.Error != "":
		return errors.New(u.Job.Error)
	case u.Outcome == JobUnconfirmed:
		return errors.New(u.Job.Title + " ended without a result")
	}
	return errors.New(u.Job.Title + " did not complete")
}

// Watching a job: reads come quickly at first, then every two seconds. Each
// read has its own deadline. A job may run for hours (a stuck apt has no
// limit), so there is no limit on the watch itself: the record decides. Only
// a vpnd that stays silent for a minute ends the watch as unconfirmed.
const (
	watchFirst    = time.Second
	watchLater    = 2 * time.Second
	watchSwitch   = 20 * time.Second
	readTimeout   = 10 * time.Second
	startTimeout  = 30 * time.Second
	silentTimeout = time.Minute
)

// Jobs is the TUI's side of vpnd's jobs. Close stops every watch and waits
// for its readers. It never stops a job.
type Jobs struct {
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	workers sync.WaitGroup
	call    func(context.Context, string, any, any) error
}

func NewJobs() *Jobs {
	ctx, cancel := context.WithCancel(context.Background())
	return &Jobs{ctx: ctx, cancel: cancel, call: api.Call}
}

func (j *Jobs) Close() {
	j.mu.Lock()
	j.cancel()
	j.mu.Unlock()
	j.workers.Wait()
}

// Start asks vpnd for a job of kind and watches it. The channel carries the
// job as it changes and closes after the final update.
func (j *Jobs) Start(kind string, params any) <-chan JobUpdate {
	return j.goWatch(func(ctx context.Context) (string, *JobUpdate) {
		req := api.StartJobParams{Kind: kind}
		if params != nil {
			raw, err := marshal(params)
			if err != nil {
				return "", &JobUpdate{Outcome: JobNotStarted, Final: true, Err: err}
			}
			req.Params = raw
		}
		startCtx, cancel := context.WithTimeout(ctx, startTimeout)
		defer cancel()
		var started api.StartedJob
		if err := j.call(startCtx, api.VerbStartJob, req, &started); err != nil {
			outcome := JobUnconfirmed
			if api.NothingChanged(err) {
				outcome = JobNotStarted
			}
			return "", &JobUpdate{Outcome: outcome, Final: true, Err: err}
		}
		return started.ID, nil
	})
}

// Watch follows a job already started, for example by another TUI.
func (j *Jobs) Watch(id string) <-chan JobUpdate {
	return j.goWatch(func(context.Context) (string, *JobUpdate) { return id, nil })
}

// Latest reads the newest job of a kind. found is false when there is none.
func (j *Jobs) Latest(kind string) (api.Job, bool, error) {
	ctx, done, err := j.begin()
	if err != nil {
		return api.Job{}, false, err
	}
	defer done()
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	var status api.JobStatus
	err = j.call(ctx, api.VerbJobStatus, api.JobStatusParams{Kind: kind}, &status)
	return status.Job, status.Found, err
}

func (j *Jobs) begin() (context.Context, func(), error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.ctx.Err(); err != nil {
		return nil, nil, err
	}
	j.workers.Add(1)
	return j.ctx, j.workers.Done, nil
}

// goWatch runs one watch. first starts or names the job. A look that cannot
// be delivered at once is dropped, the next one carries newer facts. The
// final update always arrives unless the TUI closes.
func (j *Jobs) goWatch(first func(context.Context) (string, *JobUpdate)) <-chan JobUpdate {
	updates := make(chan JobUpdate, 1)
	ctx, done, err := j.begin()
	if err != nil {
		updates <- JobUpdate{Outcome: JobNotStarted, Final: true, Err: err}
		close(updates)
		return updates
	}
	go func() {
		defer done()
		defer close(updates)
		deliver := func(u JobUpdate) {
			if u.Final {
				select {
				case updates <- u:
				case <-ctx.Done():
				}
				return
			}
			select {
			case updates <- u:
			default:
			}
		}
		id, end := first(ctx)
		if end != nil {
			deliver(*end)
			return
		}
		began, lastAnswer := time.Now(), time.Now()
		var lastSent api.Job
		var readErr error
		for {
			readCtx, cancel := context.WithTimeout(ctx, readTimeout)
			var status api.JobStatus
			err := j.call(readCtx, api.VerbJobStatus, api.JobStatusParams{ID: id}, &status)
			cancel()
			switch {
			case ctx.Err() != nil:
				return
			case err != nil:
				readErr = err
				if time.Since(lastAnswer) >= silentTimeout {
					deliver(JobUpdate{Job: lastSent, Outcome: JobUnconfirmed, Final: true, Err: readErr})
					return
				}
			case !status.Found:
				deliver(JobUpdate{Job: lastSent, Outcome: JobUnconfirmed, Final: true, Err: errors.New("vpnd has no record of job " + id)})
				return
			default:
				lastAnswer = time.Now()
				outcome := Outcome(status.Job)
				if outcome != JobRunning {
					deliver(JobUpdate{Job: status.Job, Outcome: outcome, Final: true})
					return
				}
				if !sameLook(status.Job, lastSent) {
					lastSent = status.Job
					deliver(JobUpdate{Job: status.Job, Outcome: JobRunning})
				}
			}
			wait := watchFirst
			if time.Since(began) > watchSwitch {
				wait = watchLater
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
	}()
	return updates
}

// sameLook reports whether two looks at a running job show the same thing.
func sameLook(a, b api.Job) bool {
	return a.ID == b.ID && a.State == b.State && a.StepNumber == b.StepNumber
}

func marshal(v any) ([]byte, error) { return json.Marshal(v) }
