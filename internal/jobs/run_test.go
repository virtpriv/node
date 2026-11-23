package jobs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"

	"github.com/virtpriv/node/internal/api"
	"github.com/virtpriv/node/internal/host"
	"github.com/virtpriv/node/internal/steps"
)

// startedJob is a job as its service finds it after "started" was written.
func startedJob(t *testing.T) *runCtx {
	t.Helper()
	s := testStore(t, func(string) (bool, error) { return true, nil })
	r, err := s.create(Record{Schema: schema, BootID: "boot-a",
		Job: api.Job{Kind: api.KindPackageUpdate, Title: "Update Packages", State: api.JobStarted, Accepted: s.now().UTC()}})
	if err != nil {
		t.Fatal(err)
	}
	return &runCtx{s: s, r: &r}
}

// A job's record is what a screen reads. While a step runs the record holds
// the list and names that step. A failed step ends the steps with its own
// error, and no later step runs.
func TestAJobsRecordFollowsItsSteps(t *testing.T) {
	c := startedJob(t)
	names := []string{"first", "second", "later"}
	boom := errors.New("boom")
	later := 0
	at := func(number int, result error) func(context.Context) error {
		return func(context.Context) error {
			if r, err := c.s.read(c.r.ID); err != nil || !slices.Equal(r.Steps, names) || r.StepNumber != number {
				t.Errorf("while step %d runs the record holds steps %q and number %d, %v", number, r.Steps, r.StepNumber, err)
			}
			return result
		}
	}
	err := c.steps(
		steps.Step{Name: names[0], Run: at(1, nil)},
		steps.Step{Name: names[1], Run: at(2, boom)},
		steps.Step{Name: names[2], Run: func(context.Context) error { later++; return nil }})
	if err != boom || later != 0 {
		t.Fatalf("got %v with the later step run %d times, want the failed step's own error and no later step", err, later)
	}
}

// A step's mark that cannot be written does not end the job. Package work
// must not stop half way because a record could not be saved.
func TestALostStepWriteDoesNotEndTheJob(t *testing.T) {
	c := startedJob(t)
	second := 0
	err := c.steps(
		steps.Step{Name: "first", Run: func(context.Context) error { return os.RemoveAll(c.s.dir(c.r.ID)) }},
		steps.Step{Name: "second", Run: func(context.Context) error { second++; return nil }})
	if err != nil || second != 1 {
		t.Fatalf("got %v with the second step run %d times, want the job to go on", err, second)
	}
}

// A job that failed before it changed anything says so in its record, which
// is what lets a screen say "was not started". Any other failure does not.
func TestAJobsRecordSaysWhenNothingWasChanged(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{fmt.Errorf("refused: %w", host.ErrNothingChanged), true},
		{errors.New("apt failed half way"), false},
	} {
		c := startedJob(t)
		if err := c.finish(nil, tc.err); err != tc.err {
			t.Fatal(err)
		}
		r, err := c.s.read(c.r.ID)
		if err != nil || r.State != api.JobFailed || r.NothingChanged != tc.want || r.Error != tc.err.Error() {
			t.Fatalf("after %q the record holds %+v, %v", tc.err, r.Job, err)
		}
	}
}
