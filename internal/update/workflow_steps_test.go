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

// Each step keeps the time it began across the saves made while it runs, so
// the screen can show how long a step runs and how long each one took.
func TestEachStepKeepsTheTimeItBegan(t *testing.T) {
	tick := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	defer func(old func() time.Time) { now = old }(now)
	now = func() time.Time { tick = tick.Add(time.Second); return tick }
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
		if i < 0 || i >= len(saved.StepTimes) || saved.StepTimes[i].IsZero() {
			t.Fatalf("step %q has no time during %s", step, event)
		}
		if first, ok := began[step]; ok && !first.Equal(saved.StepTimes[i]) {
			t.Fatalf("step %q's time moved during %s", step, event)
		}
		began[step] = saved.StepTimes[i]
	}
	if err := runJob(j, h.ops()); err != nil {
		t.Fatal(err)
	}
	saved := h.reload()
	if len(saved.StepTimes) != len(saved.Steps) {
		t.Fatalf("%d times for %d steps", len(saved.StepTimes), len(saved.Steps))
	}
	for i := 1; i < len(saved.StepTimes); i++ {
		if !saved.StepTimes[i].After(saved.StepTimes[i-1]) {
			t.Fatalf("step %d began before the one ahead of it", i+1)
		}
	}
}
