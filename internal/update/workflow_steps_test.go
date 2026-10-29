package update

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/virtpriv/node/internal/update/protocol"
)

// stepOwner names the screen step that is running while the worker does
// event. Cleanup events have no step of their own.
func stepOwner(event string) string {
	kind, c, _ := strings.Cut(event, ":")
	switch kind {
	case "identify":
		return "Check the node"
	case "stage":
		return "Download and verify release files"
	case "capacity":
		return "Check free space"
	case "guard":
		return "Prepare services"
	case "stop":
		return "Stop " + c
	case "install":
		return "Install new files"
	case "start", "health":
		return "Start and check " + c
	case "commit":
		return "Finish VPN update"
	}
	return ""
}

// savedStep is the step the saved record says is running.
func savedStep(t *testing.T, h *workflowHarness) string {
	t.Helper()
	j := h.reload()
	if j.StepNumber < 1 || j.StepNumber > len(j.Steps) {
		t.Fatalf("step number %d outside a list of %d steps", j.StepNumber, len(j.Steps))
	}
	return j.Steps[j.StepNumber-1]
}

// watchSteps fails the test when the saved record names another step than
// the one doing the work.
func watchSteps(t *testing.T, h *workflowHarness) {
	h.seen = func(event string) {
		if want := stepOwner(event); want != "" {
			if got := savedStep(t, h); got != want {
				t.Fatalf("during %s the record names step %q, want %q", event, got, want)
			}
		}
	}
}

func TestStepListFollowsTheWorkAndEndsComplete(t *testing.T) {
	j := workflowFixture()
	h := newHarness(t, j)
	watchSteps(t, h)
	if err := runJob(j, h.ops()); err != nil {
		t.Fatal(err)
	}
	var ran []string
	for _, event := range append([]string{"identify"}, h.events...) {
		if name := stepOwner(event); name != "" && !slices.Contains(ran, name) {
			ran = append(ran, name)
		}
	}
	saved := h.reload()
	if !slices.Equal(saved.Steps, ran) {
		t.Fatalf("list %q, steps that ran %q", saved.Steps, ran)
	}
	if saved.Phase != "complete" || saved.StepNumber != len(saved.Steps) {
		t.Fatalf("finished update at phase %s, step %d of %d", saved.Phase, saved.StepNumber, len(saved.Steps))
	}
}

func TestFailureMarksTheStepThatFailed(t *testing.T) {
	for _, event := range []string{"stage", "capacity", "guard", "stop:syncthing", "stop:lnd", "stop:bitcoin",
		"install", "start:bitcoin", "health:bitcoin", "start:lnd", "health:lnd", "start:syncthing", "health:syncthing", "commit"} {
		t.Run(event, func(t *testing.T) {
			j := workflowFixture()
			h := newHarness(t, j)
			h.failure = event
			if err := runJob(j, h.ops()); err == nil {
				t.Fatal("injected failure did not fail the update")
			}
			if h.reload().Phase != "failed" {
				t.Fatal("failure was not saved")
			}
			if got, want := savedStep(t, h), stepOwner(event); got != want {
				t.Fatalf("failed step %q, want %q", got, want)
			}
		})
	}
}

func TestRestartWritesItsOwnListAndMarksAnInterruptedStart(t *testing.T) {
	j := workflowFixture()
	h := newHarness(t, j)
	h.interrupt, h.after = "start:lnd", true
	runInterrupted(t, j, h.ops())
	// The resumed run finds a list it did not write, as after an older worker.
	j = h.reload()
	j.Steps, j.StepNumber = []string{"An older step"}, 1
	if err := saveJob(h.root, j); err != nil {
		t.Fatal(err)
	}
	h.interrupt = ""
	h.running[protocol.LND] = false // the reboot ended lnd
	watchSteps(t, h)
	if err := runJob(j, h.ops()); err == nil {
		t.Fatal("an interrupted start did not require Retry")
	}
	if got := savedStep(t, h); got != "Start and check lnd" {
		t.Fatalf("interrupted start marked %q", got)
	}
	if slices.Contains(h.reload().Steps, "An older step") {
		t.Fatal("the restart kept a list it did not write")
	}
}

func TestRetryChecksTheNodeOnItsOwnStep(t *testing.T) {
	j := workflowFixture()
	h := newHarness(t, j)
	h.failure = "stage"
	if err := runJob(j, h.ops()); err == nil {
		t.Fatal("injected failure did not fail the update")
	}
	// Retry before any host change, after lnd stopped by itself.
	j = h.reload()
	j.Phase, j.Error = "retry", ""
	h.failure = ""
	h.running[protocol.LND] = false
	watchSteps(t, h)
	if err := runJob(j, h.ops()); err == nil {
		t.Fatal("the node check passed with lnd stopped")
	}
	if got := savedStep(t, h); got != "Check the node" {
		t.Fatalf("the failed node check marked %q", got)
	}
}

// fakeClock makes the worker's clock move one second on each reading, and
// returns a way to move it further, as time passing between two runs.
func fakeClock(t *testing.T) (wait func(time.Duration)) {
	tick := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	old := now
	t.Cleanup(func() { now = old })
	now = func() time.Time { tick = tick.Add(time.Second); return tick }
	return func(d time.Duration) { tick = tick.Add(d) }
}

// Each step keeps when it began across the saves made while it runs, and
// gets when it ended once the next step begins or the update ends, so the
// screen can show how long each step took without working it out from the
// step after it.
func TestEachStepKeepsWhenItBeganAndEnded(t *testing.T) {
	fakeClock(t)
	j := workflowFixture()
	h := newHarness(t, j)
	began := map[string]time.Time{}
	h.seen = func(event string) {
		step := stepOwner(event)
		if step == "" {
			return
		}
		saved := h.reload()
		i := slices.Index(saved.Steps, step)
		if i < 0 || i >= len(saved.StepStarts) || saved.StepStarts[i].IsZero() {
			t.Fatalf("step %q has no start during %s", step, event)
		}
		if first, ok := began[step]; ok && !first.Equal(saved.StepStarts[i]) {
			t.Fatalf("step %q's start moved during %s", step, event)
		}
		began[step] = saved.StepStarts[i]
		if i < len(saved.StepEnds) && !saved.StepEnds[i].IsZero() {
			t.Fatalf("step %q has an end while it runs", step)
		}
		for k := range i {
			if k >= len(saved.StepEnds) || saved.StepEnds[k].IsZero() {
				t.Fatalf("step %q has no end while %q runs", saved.Steps[k], step)
			}
		}
	}
	if err := runJob(j, h.ops()); err != nil {
		t.Fatal(err)
	}
	saved := h.reload()
	if len(saved.StepStarts) != len(saved.Steps) || len(saved.StepEnds) != len(saved.Steps) {
		t.Fatalf("%d starts and %d ends for %d steps", len(saved.StepStarts), len(saved.StepEnds), len(saved.Steps))
	}
	for i := range saved.Steps {
		if saved.StepEnds[i].Before(saved.StepStarts[i]) || saved.StepEnds[i].IsZero() {
			t.Fatalf("step %q ended before it began, or never", saved.Steps[i])
		}
		if i > 0 && saved.StepStarts[i].Before(saved.StepEnds[i-1]) {
			t.Fatalf("step %d began before the one ahead of it ended", i+1)
		}
	}
}

// A Retry after host changes does not redo the node check and the download.
// Those steps keep the times of the run that did them, however long the
// owner waited before Retry. The failed step begins again.
func TestRetryKeepsTheTimesOfStepsItDoesNotRedo(t *testing.T) {
	wait := fakeClock(t)
	j := workflowFixture()
	h := newHarness(t, j)
	h.failure = "stop:lnd"
	if err := runJob(j, h.ops()); err == nil {
		t.Fatal("injected failure did not fail the update")
	}
	failed := h.reload()
	stop := slices.Index(failed.Steps, "Stop lnd")
	if stop < 0 || stop >= len(failed.StepEnds) || failed.StepEnds[stop].IsZero() {
		t.Fatal("the failed step has no end")
	}
	starts, ends := slices.Clone(failed.StepStarts), slices.Clone(failed.StepEnds)
	wait(40 * time.Minute)
	j = h.reload()
	j.Phase, j.Error = "retry", ""
	h.failure = ""
	if err := runJob(j, h.ops()); err != nil {
		t.Fatal(err)
	}
	done := h.reload()
	for _, step := range []string{"Check the node", "Download and verify release files"} {
		i := slices.Index(done.Steps, step)
		if !done.StepStarts[i].Equal(starts[i]) || !done.StepEnds[i].Equal(ends[i]) {
			t.Fatalf("%q lost the times of the run that did it", step)
		}
		if took := done.StepEnds[i].Sub(done.StepStarts[i]); took > time.Minute {
			t.Fatalf("%q took %s, which counts the wait before Retry", step, took)
		}
	}
	if !done.StepStarts[stop].After(ends[stop]) {
		t.Fatal("the failed step did not begin again")
	}
}
