package app

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/virtpriv/node/internal/api"
)

// fakeVPND answers start-job with one job and each job-status read with the
// next entry of reads, the last one repeated.
type fakeVPND struct {
	startErr error
	reads    []func() (api.Job, error)
	n        int
}

func (f *fakeVPND) call(_ context.Context, verb string, _, result any) error {
	switch verb {
	case api.VerbStartJob:
		if f.startErr != nil {
			return f.startErr
		}
		*result.(*api.StartedJob) = api.StartedJob{ID: "20261109-100000-aaaaaa", Title: "Update Packages"}
		return nil
	case api.VerbJobStatus:
		read := f.reads[min(f.n, len(f.reads)-1)]
		f.n++
		job, err := read()
		if err != nil {
			return err
		}
		*result.(*api.JobStatus) = api.JobStatus{Found: true, Job: job}
		return nil
	}
	return errors.New("unexpected request " + verb)
}

func job(state api.JobState, nothingChanged bool) func() (api.Job, error) {
	return func() (api.Job, error) {
		return api.Job{ID: "20261109-100000-aaaaaa", Kind: api.KindPackageUpdate, State: state, NothingChanged: nothingChanged}, nil
	}
}

func lostRead() (api.Job, error) { return api.Job{}, errors.New("no answer from vpnd") }

// final drains the updates and returns the last one, failing on a final
// update that is not the last.
func final(t *testing.T, updates <-chan JobUpdate) JobUpdate {
	t.Helper()
	var last JobUpdate
	seen := false
	for u := range updates {
		if seen {
			t.Fatalf("an update after the final one: %+v", u)
		}
		last, seen = u, u.Final
	}
	if !seen {
		t.Fatal("no final update")
	}
	return last
}

// S1: the record decides a job's outcome, never the screen's own clock or a
// few lost reads.
func TestWatchLetsTheRecordDecide(t *testing.T) {
	for _, c := range []struct {
		name  string
		vpnd  fakeVPND
		want  JobOutcome
		error bool
	}{
		{"lost reads then done", fakeVPND{reads: []func() (api.Job, error){
			job(api.JobStarted, false), lostRead, lostRead, lostRead, job(api.JobDone, false)}}, JobCompleted, false},
		{"refused as busy", fakeVPND{startErr: &api.AnswerError{Message: "Reboot is running (job x). Nothing was changed.", Code: api.CodeBusy}}, JobNotStarted, true},
		{"failed before any change", fakeVPND{reads: []func() (api.Job, error){job(api.JobFailed, true)}}, JobNotStarted, false},
		{"failed after a change", fakeVPND{reads: []func() (api.Job, error){job(api.JobFailed, false)}}, JobFailed, false},
		{"ended without a result", fakeVPND{reads: []func() (api.Job, error){job(api.JobStarted, false), job(api.JobUnknown, false)}}, JobUnconfirmed, false},
		{"answer to the start lost", fakeVPND{startErr: errors.New("no answer from vpnd")}, JobUnconfirmed, true},
		{"vpnd silent for good", fakeVPND{reads: []func() (api.Job, error){job(api.JobStarted, false), lostRead}}, JobUnconfirmed, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				jobs := NewJobs()
				defer jobs.Close()
				jobs.call = c.vpnd.call
				got := final(t, jobs.Start(api.KindPackageUpdate, nil))
				if got.Outcome != c.want || (got.Err != nil) != c.error {
					t.Fatalf("final = %+v, want outcome %v, error %v", got, c.want, c.error)
				}
			})
		})
	}
}

// S1: a job that runs for hours stays running. No observation limit turns
// it into a failure.
func TestWatchOfALongJobNeverEndsOnItsOwn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		jobs := NewJobs()
		vpnd := &fakeVPND{reads: []func() (api.Job, error){job(api.JobStarted, false)}}
		jobs.call = vpnd.call
		updates := jobs.Start(api.KindPackageUpdate, nil)
		deadline := time.After(6 * time.Hour)
	watch:
		for {
			select {
			case u := <-updates:
				if u.Final {
					t.Fatalf("a running job ended its watch: %+v", u)
				}
			case <-deadline:
				break watch
			}
		}
		jobs.Close()
		for u := range updates {
			if u.Final && u.Outcome != JobRunning {
				t.Fatalf("closing the TUI reported an outcome: %+v", u)
			}
		}
	})
}
