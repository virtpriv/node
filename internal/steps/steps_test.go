package steps

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/virtpriv/node/internal/failure"
)

// recorder is a sink that keeps what the runner told it, in order.
type recorder struct {
	trail    []string
	beginErr map[int]error
	endErr   map[int]error
}

func (r *recorder) Begin(i int) error {
	r.trail = append(r.trail, fmt.Sprintf("begin %d", i))
	return r.beginErr[i]
}

func (r *recorder) End(i int, o Outcome, err error) error {
	r.trail = append(r.trail, fmt.Sprintf("end %d %s", i, [...]string{"done", "failed", "stopped"}[o]))
	return r.endErr[i]
}

func (r *recorder) Log(i int, text string) {
	r.trail = append(r.trail, fmt.Sprintf("log %d %s", i, text))
}

func (r *recorder) is(t *testing.T, want ...string) {
	t.Helper()
	if got := strings.Join(r.trail, "|"); got != strings.Join(want, "|") {
		t.Fatalf("the sink was told %q, want %q", r.trail, want)
	}
}

func ok(context.Context) error { return nil }

// A stop asked while a step's command runs does not cut the step short. The
// step ends as it would have, and the run stops before the next step.
func TestAStopWaitsForARunningStepAndAdmitsNoOther(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	later := 0
	sink := &recorder{}
	res := Run(ctx, []Step{
		{Name: "command", Run: func(context.Context) error { stop(); return nil }},
		{Name: "later", Run: func(context.Context) error { later++; return nil }},
	}, sink)
	if res.Outcome != Stopped || res.Index != 1 || res.Err != nil || later != 0 {
		t.Fatalf("got %+v with the later step run %d times, want a stop before step 1", res, later)
	}
	sink.is(t, "begin 0", "log 0 "+logStarting+"command", "end 0 done", "log 0 "+logComplete+"command")
}

// A wait that gives way to a stop ends its step as stopped, not failed. The
// sink is told so before the log says it, so that it does not record the
// step.
func TestAWaitThatGivesWayEndsTheStepAsStopped(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	sink := &recorder{}
	res := Run(ctx, []Step{{Name: "wait", Run: func(ctx context.Context) error {
		stop()
		<-ctx.Done()
		return fmt.Errorf("waiting for Tor: %w", ctx.Err())
	}}}, sink)
	if res.Outcome != Stopped || res.Index != 0 || res.Err != nil {
		t.Fatalf("got %+v, want step 0 stopped with no error", res)
	}
	sink.is(t, "begin 0", "log 0 "+logStarting+"wait", "end 0 stopped", "log 0 "+logStopped+"wait")
}

// A real failure after a stop was asked stays a failure.
func TestAFailureAfterAStopStaysAFailure(t *testing.T) {
	boom := errors.New("apt failed")
	ctx, stop := context.WithCancel(context.Background())
	sink := &recorder{}
	res := Run(ctx, []Step{{Name: "apt", Run: func(context.Context) error { stop(); return boom }}}, sink)
	if res.Outcome != Failed || !errors.Is(res.Err, boom) {
		t.Fatalf("got %+v, want the failure", res)
	}
	sink.is(t, "begin 0", "log 0 "+logStarting+"apt", "end 0 failed", "log 0 "+logFailed+"apt: apt failed")
}

// A step cannot call itself stopped. The stop's error with no stop asked is
// a failure.
func TestAStepCannotCallItselfStopped(t *testing.T) {
	sink := &recorder{}
	res := Run(context.Background(), []Step{{Name: "liar", Run: func(context.Context) error { return context.Canceled }}}, sink)
	if res.Outcome != Failed || !errors.Is(res.Err, context.Canceled) {
		t.Fatalf("got %+v, want a failure", res)
	}
	if last := sink.trail[len(sink.trail)-2]; last != "end 0 failed" {
		t.Fatalf("the sink was told %q", sink.trail)
	}
}

// The record comes before the log. A finished step whose record could not be
// written is a failure, the log never calls it complete, and no later step
// runs.
func TestAStepWhoseRecordFailsIsNeverLoggedComplete(t *testing.T) {
	lost := errors.New("disk full")
	later := 0
	sink := &recorder{endErr: map[int]error{0: lost}}
	res := Run(context.Background(), []Step{
		{Name: "first", Run: ok},
		{Name: "later", Run: func(context.Context) error { later++; return nil }},
	}, sink)
	if res.Outcome != Failed || res.Index != 0 || !errors.Is(res.Err, lost) || later != 0 {
		t.Fatalf("got %+v, later ran %d times", res, later)
	}
	sink.is(t, "begin 0", "log 0 "+logStarting+"first", "end 0 done", "log 0 "+logFailed+"first: disk full")
}

// A step that cannot be marked as begun does not run, and no later step
// does. The sink is not told that it ended, since it never began.
func TestAStepThatCannotBeMarkedAsBegunDoesNotRun(t *testing.T) {
	lost := errors.New("disk full")
	ran := 0
	sink := &recorder{beginErr: map[int]error{1: lost}}
	count := func(context.Context) error { ran++; return nil }
	res := Run(context.Background(), []Step{{Name: "first", Run: ok}, {Name: "second", Run: count}, {Name: "later", Run: count}}, sink)
	if res.Outcome != Failed || res.Index != 1 || res.Err != lost || ran != 0 {
		t.Fatalf("got %+v with %d later steps run, want step 1 failed with the record's error and none run", res, ran)
	}
	sink.is(t, "begin 0", "log 0 "+logStarting+"first", "end 0 done", "log 0 "+logComplete+"first",
		"begin 1", "log 1 "+logFailed+"second: disk full")
}

// A failed step ends the run, keeps its cause, and a failure of the record
// is told beside it, never in its place.
func TestAFailedStepEndsTheRunAndKeepsItsCause(t *testing.T) {
	boom, lost := errors.New("boom"), errors.New("record not saved")
	later := 0
	sink := &recorder{endErr: map[int]error{1: lost}}
	res := Run(context.Background(), []Step{
		{Name: "first", Run: ok},
		{Name: "second", Run: func(context.Context) error { return boom }},
		{Name: "later", Run: func(context.Context) error { later++; return nil }},
	}, sink)
	if res.Outcome != Failed || res.Index != 1 || !errors.Is(res.Err, boom) || !errors.Is(res.Err, lost) || later != 0 {
		t.Fatalf("got %+v, later ran %d times", res, later)
	}
}

// A failure with a plain sentence logs the sentence, then the program's own
// words line by line. Those words are shown nowhere else, so the log must
// hold all of them. A control character in them cannot reach a terminal that
// shows the log.
func TestAFailuresOwnWordsFollowItsSentenceInTheLog(t *testing.T) {
	own := errors.New("apt-get update: exit status 100: E: Failed to fetch\x1b[2J\n\nE: Some index files failed to download.")
	sink := &recorder{}
	Run(context.Background(), []Step{{Name: "first", Run: func(context.Context) error {
		return &failure.Failure{Text: "The lists were not downloaded.", Cause: own}
	}}}, sink)
	sink.is(t, "begin 0", "log 0 "+logStarting+"first", "end 0 failed",
		"log 0 "+logFailed+"first: The lists were not downloaded.",
		"log 0 "+logCause+"apt-get update: exit status 100: E: Failed to fetch [2J",
		"log 0 "+logCause+"E: Some index files failed to download.")
}
