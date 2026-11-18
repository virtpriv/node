package update

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/virtpriv/node/internal/update/protocol"
)

func workflowFixture() *job {
	h := strings.Repeat("a", 64)
	source := protocol.Versions{VPN: "0.7.0"}
	m := protocol.Manifest{Protocol: protocol.Protocol, Platform: "debian-13-amd64", Version: "0.7.1", Summary: "Fixture only", MinimumFreeMiB: 2048, Sources: []protocol.Versions{source}}
	return &job{Schema: jobSchema, Review: protocol.Review{Digest: h, Manifest: m, Source: source}, WorkerHash: h, PlanHash: h, ConfigHash: h, Phase: "accepted"}
}

// workflowHarness runs the worker's steps against real files for the record
// and the program, and stand-ins for the node check and the space check.
type workflowHarness struct {
	t         *testing.T
	root      string
	binary    string
	events    []string
	interrupt string
	after     bool
	failure   string
	// crash, when set, is asked after each durable save whether power is lost.
	crash func(*job) bool
	// seen, when set, is told each event before it happens.
	seen func(event string)
}

func (h *workflowHarness) checkpoint(j *job) error {
	if err := saveJob(h.root, j); err != nil {
		return err
	}
	if h.crash != nil && h.crash(j) {
		panic("power loss")
	}
	return nil
}

func (h *workflowHarness) reload() *job {
	h.t.Helper()
	j, err := loadJob(h.root)
	if err != nil || j == nil {
		h.t.Fatalf("reload saved job: %v", err)
	}
	return j
}

func (h *workflowHarness) installed() string {
	h.t.Helper()
	data, err := os.ReadFile(h.binary)
	if err != nil {
		h.t.Fatal(err)
	}
	return string(data)
}

func (h *workflowHarness) effect(name string, fn func() error) error {
	if h.seen != nil {
		h.seen(name)
	}
	h.events = append(h.events, name)
	if h.interrupt == name && !h.after {
		panic("power loss")
	}
	if h.failure == name {
		return errors.New("injected failure")
	}
	if fn != nil {
		if err := fn(); err != nil {
			return err
		}
	}
	if h.interrupt == name && h.after {
		panic("power loss")
	}
	return nil
}

func (h *workflowHarness) ops() workflowOps {
	return workflowOps{
		save:     h.checkpoint,
		check:    func(*job) error { return h.effect("check", nil) },
		capacity: func(*job) error { return h.effect("capacity", nil) },
		commit: func(j *job) error {
			return h.effect("commit", func() error { return commitJob(h.root, h.binary, j) })
		},
	}
}

func newHarness(t *testing.T, j *job) *workflowHarness {
	root, binary := workerFiles(t, j)
	h := &workflowHarness{t: t, root: root, binary: binary}
	if err := h.checkpoint(j); err != nil {
		t.Fatal(err)
	}
	return h
}

func runInterrupted(t *testing.T, j *job, ops workflowOps) {
	t.Helper()
	defer func() {
		if r := recover(); r != "power loss" && r != nil {
			panic(r)
		}
	}()
	err := runJob(j, ops)
	t.Fatalf("did not reach interruption boundary: %v", err)
}

var everyStep = []string{"check", "capacity", "commit"}

// stepOwner names the screen step that is running while the worker does
// event.
func stepOwner(event string) string {
	return map[string]string{
		"check":    "Check the node",
		"capacity": "Check free space",
		"commit":   "Finish VPN update",
	}[event]
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
		if got, want := savedStep(t, h), stepOwner(event); got != want {
			t.Fatalf("during %s the record names step %q, want %q", event, got, want)
		}
	}
}

func TestUpdateChecksTheNodeAndTheSpaceBeforeItReplacesVPN(t *testing.T) {
	j := workflowFixture()
	h := newHarness(t, j)
	watchSteps(t, h)
	if err := runJob(j, h.ops()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(h.events, everyStep) {
		t.Fatalf("the update did %v, want %v", h.events, everyStep)
	}
	saved := h.reload()
	if saved.Phase != "complete" || saved.StepNumber != len(saved.Steps) || saved.Ended.IsZero() {
		t.Fatalf("finished update at phase %s, step %d of %d", saved.Phase, saved.StepNumber, len(saved.Steps))
	}
	if !slices.Equal(saved.Steps, []string{"Check the node", "Check free space", "Finish VPN update"}) {
		t.Fatalf("step list %q", saved.Steps)
	}
	if h.installed() != "new VPN" {
		t.Fatal("a finished update left the old program")
	}
	h.events = nil
	if err := runJob(h.reload(), h.ops()); err != nil || len(h.events) != 0 {
		t.Fatal("completed job repeated host changes on relaunch", err, h.events)
	}
}

// A power cut at any point leaves the old program or the whole new one. The
// record says the program may have been replaced before it is, so Cancel is
// refused from then on, and the next run finishes the update from the record.
func TestInterruptedUpdateContinuesFromTheSavedRecord(t *testing.T) {
	type cut struct {
		name  string
		apply func(*workflowHarness)
	}
	cuts := []cut{
		{"before the node check", func(h *workflowHarness) { h.interrupt = "check" }},
		{"before the space check", func(h *workflowHarness) { h.interrupt = "capacity" }},
		{"before the program is replaced", func(h *workflowHarness) { h.interrupt = "commit" }},
		{"after the program is replaced", func(h *workflowHarness) { h.interrupt, h.after = "commit", true }},
	}
	// A cut right after each save of the record, as many as a run makes.
	saves := 0
	{
		j := workflowFixture()
		h := newHarness(t, j)
		h.crash = func(*job) bool { saves++; return false }
		if err := runJob(j, h.ops()); err != nil {
			t.Fatal(err)
		}
	}
	for n := 1; n <= saves; n++ {
		cuts = append(cuts, cut{fmt.Sprintf("after save %d of %d", n, saves), func(h *workflowHarness) {
			seen := 0
			h.crash = func(*job) bool { seen++; return seen == n }
		}})
	}
	for _, c := range cuts {
		t.Run(c.name, func(t *testing.T) {
			j := workflowFixture()
			h := newHarness(t, j)
			c.apply(h)
			runInterrupted(t, j, h.ops())
			saved := h.reload()
			was := h.installed()
			if was != "old VPN" && was != "new VPN" {
				t.Fatalf("the cut left a program that is neither the old nor the new one: %q", was)
			}
			if was == "new VPN" && !saved.HostChanges {
				t.Fatal("the program was replaced before the record said it may be")
			}
			if saved.active() {
				// Cancel is tried on the record and the record put back, so
				// the same cut also shows how the next run continues.
				path := filepath.Join(h.root, "current.json")
				record, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if cancelErr := cancelUpdate(h.root, saved.Review.Digest); (cancelErr == nil) == saved.HostChanges {
					t.Fatalf("with changes begun %v, Cancel answered %v", saved.HostChanges, cancelErr)
				}
				if err := os.WriteFile(path, record, 0600); err != nil {
					t.Fatal(err)
				}
			}
			h.interrupt, h.crash, h.events = "", nil, nil
			if err := runJob(h.reload(), h.ops()); err != nil {
				t.Fatal("the next run did not finish the update:", err)
			}
			if h.reload().Phase != "complete" || h.installed() != "new VPN" {
				t.Fatalf("after the next run: phase %s, program %q", h.reload().Phase, h.installed())
			}
		})
	}
}

// A failure is saved with the step it happened in and the words for the
// screen, and waits for Retry. Before the program may have been replaced the
// update can still be canceled, and the program is the old one.
func TestFailureIsSavedAndWaitsForRetry(t *testing.T) {
	for _, failure := range everyStep {
		t.Run(failure, func(t *testing.T) {
			j := workflowFixture()
			h := newHarness(t, j)
			h.failure = failure
			if runJob(j, h.ops()) == nil {
				t.Fatal("reported success")
			}
			saved := h.reload()
			if saved.Phase != "failed" || !strings.Contains(saved.Error, "injected failure") {
				t.Fatal("lost its failure record", saved)
			}
			if got := savedStep(t, h); got != stepOwner(failure) {
				t.Fatalf("failed step %q, want %q", got, stepOwner(failure))
			}
			if saved.HostChanges != (failure == "commit") {
				t.Fatalf("changes begun is %v after a failed %s", saved.HostChanges, failure)
			}
			if next, advice := protocol.Advise(!saved.HostChanges); saved.Next != next || saved.Advice != advice {
				t.Fatalf("saved next step %q and advice %q", saved.Next, saved.Advice)
			}
			if h.installed() != "old VPN" {
				t.Fatal("a failed update replaced the program")
			}
			h.events = nil
			if err := runJob(h.reload(), h.ops()); err != nil || len(h.events) != 0 {
				t.Fatal("known failure automatically retried on boot")
			}
			if failure == "commit" {
				// Retry must not reopen the choice to cancel.
				if err := retryUpdate(h.root, j.Review.Digest); err != nil {
					t.Fatal(err)
				}
			}
			if err := cancelUpdate(h.root, j.Review.Digest); (err == nil) != (failure != "commit") {
				t.Fatal("wrong cancellation boundary:", err)
			}
		})
	}
}

// Retry runs every step again, so the node and the space are checked again
// before the program is replaced, and leaves nothing of the failure behind.
func TestRetryChecksAgainAndFinishes(t *testing.T) {
	for _, failure := range everyStep {
		t.Run(failure, func(t *testing.T) {
			j := workflowFixture()
			h := newHarness(t, j)
			h.failure = failure
			if runJob(j, h.ops()) == nil {
				t.Fatal("fixture did not fail")
			}
			if err := retryUpdate(h.root, j.Review.Digest); err != nil {
				t.Fatal(err)
			}
			h.failure, h.events = "", nil
			watchSteps(t, h)
			if err := runJob(h.reload(), h.ops()); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(h.events, everyStep) {
				t.Fatalf("Retry did %v, want %v", h.events, everyStep)
			}
			saved := h.reload()
			if saved.Phase != "complete" || saved.Error != "" || saved.Next != "" || saved.Advice != "" {
				t.Fatalf("after Retry: %+v", saved)
			}
			if h.installed() != "new VPN" {
				t.Fatal("Retry left the old program")
			}
		})
	}
}

// The record must say the program may be replaced before it is. A record
// that cannot be saved stops the update there.
func TestUnsavedRecordStopsBeforeTheProgramIsReplaced(t *testing.T) {
	j := workflowFixture()
	h := newHarness(t, j)
	ops := h.ops()
	ops.save = func(j *job) error {
		if j.HostChanges {
			return errors.New("disk full")
		}
		return h.checkpoint(j)
	}
	if runJob(j, ops) == nil {
		t.Fatal("ignored failed record write")
	}
	if slices.Contains(h.events, "commit") || h.installed() != "old VPN" {
		t.Fatal("replaced the program with no durable record that it may be", h.events)
	}
}

// A resumed run may find a list of steps another worker wrote. It writes
// its own.
func TestRestartWritesItsOwnStepList(t *testing.T) {
	j := workflowFixture()
	h := newHarness(t, j)
	h.interrupt = "capacity"
	runInterrupted(t, j, h.ops())
	j = h.reload()
	j.Steps, j.StepNumber = []string{"An older step"}, 1
	if err := saveJob(h.root, j); err != nil {
		t.Fatal(err)
	}
	h.interrupt = ""
	watchSteps(t, h)
	if err := runJob(j, h.ops()); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(h.reload().Steps, "An older step") {
		t.Fatal("the restart kept a list it did not write")
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

// A Retry does every step again, so each begins again. However long the
// owner waited before Retry, no step's time counts that wait.
func TestRetryDoesNotCountTheWaitBeforeIt(t *testing.T) {
	wait := fakeClock(t)
	j := workflowFixture()
	h := newHarness(t, j)
	h.failure = "capacity"
	if err := runJob(j, h.ops()); err == nil {
		t.Fatal("injected failure did not fail the update")
	}
	failed := h.reload()
	at := slices.Index(failed.Steps, "Check free space")
	if at < 0 || at >= len(failed.StepEnds) || failed.StepEnds[at].IsZero() {
		t.Fatal("the failed step has no end")
	}
	wait(40 * time.Minute)
	if err := retryUpdate(h.root, j.Review.Digest); err != nil {
		t.Fatal(err)
	}
	h.failure = ""
	if err := runJob(h.reload(), h.ops()); err != nil {
		t.Fatal(err)
	}
	done := h.reload()
	for i, step := range done.Steps {
		if !done.StepStarts[i].After(failed.StepEnds[at]) {
			t.Fatalf("%q did not begin again", step)
		}
		if took := done.StepEnds[i].Sub(done.StepStarts[i]); took > time.Minute {
			t.Fatalf("%q took %s, which counts the wait before Retry", step, took)
		}
	}
}
