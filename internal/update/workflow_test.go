package update

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/virtualprivatenode/vpn/internal/update/protocol"
)

func workflowFixture() *job {
	h := strings.Repeat("a", 64)
	source := protocol.Versions{VPN: "0.7.0", Bitcoin: "29.2", LND: "0.21.1-beta", Syncthing: "2.1.3"}
	m := protocol.Manifest{Protocol: 1, Platform: "debian-13-amd64", Version: "0.7.1", Summary: "Fixture only", MinimumFreeMiB: 2048, Sources: []protocol.Versions{source}, Networks: []string{"public-signet"}, Bitcoin: protocol.Artifact{Version: "29.3", SHA256: h}, LND: protocol.Artifact{Version: "0.21.2-beta", SHA256: h}, Syncthing: protocol.Artifact{Version: "2.1.5", SHA256: h}}
	return &job{Schema: 1, Review: protocol.Review{Digest: h, Manifest: m, Source: source, Network: "public-signet"}, WorkerHash: h, PlanHash: h, ConfigHash: h, Phase: "accepted", Started: map[protocol.Component]bool{}, MayHaveRun: map[protocol.Component]bool{}, Completed: map[string]bool{}, BinaryHashes: map[string]string{}, Affected: m.Affected(source)}
}

type workflowHarness struct {
	t         *testing.T
	root      string
	binary    string
	events    []string
	running   map[protocol.Component]bool
	interrupt string
	after     bool
	failure   string
	// crash, when set, is asked after each durable save whether power is lost.
	crash func(*job) bool
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
func (h *workflowHarness) checkRunning(c protocol.Component) error {
	if !h.running[c] {
		return errors.New("process absent")
	}
	return nil
}
func (h *workflowHarness) effect(name string, fn func() error) error {
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
		stage:    func(*job) error { return h.effect("stage", nil) },
		capacity: func(*job) error { return h.effect("capacity", nil) },
		discard:  func(*job) error { return h.effect("discard", nil) },
		guard:    func([]protocol.Component) error { return h.effect("guard", nil) },
		stop: func(c protocol.Component) error {
			return h.effect("stop:"+string(c), func() error { h.running[c] = false; return nil })
		},
		install: func(*job) error { return h.effect("install", nil) },
		start: func(c protocol.Component) error {
			j := h.reload()
			if !j.Started[c] || !j.MayHaveRun[c] {
				h.t.Fatal("started a component before saving the database boundary")
			}
			return h.effect("start:"+string(c), func() error { h.running[c] = true; return nil })
		},
		running: h.checkRunning,
		health:  func(_ *job, c protocol.Component) error { return h.effect("health:"+string(c), nil) },
		commit: func(j *job) error {
			return h.effect("commit", func() error {
				return commitJob(h.root, h.binary, j, h.checkRunning, func([]protocol.Component) error { return nil })
			})
		},
		quarantine: func(components []protocol.Component) error {
			return h.effect("quarantine", func() error {
				for _, c := range components {
					h.running[c] = false
				}
				return nil
			})
		},
	}
}
func newHarness(t *testing.T, j *job) *workflowHarness {
	root, binary := workerFiles(t, j)
	h := &workflowHarness{t: t, root: root, binary: binary, running: map[protocol.Component]bool{}}
	for _, c := range j.Affected {
		h.running[c] = true
	}
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

func TestWorkflowStagesBeforeDowntimeAndCommitsVPNLast(t *testing.T) {
	j := workflowFixture()
	h := newHarness(t, j)
	if err := runJob(j, h.ops()); err != nil {
		t.Fatal(err)
	}
	before := func(a, b string) {
		t.Helper()
		i, k := slices.Index(h.events, a), slices.Index(h.events, b)
		if i < 0 || k < 0 || i >= k {
			t.Fatalf("%s must precede %s: %v", a, b, h.events)
		}
	}
	before("stage", "capacity")
	before("capacity", "guard")
	before("stop:lnd", "stop:bitcoin")
	before("health:bitcoin", "start:lnd")
	for _, c := range j.Affected {
		before("guard", "stop:"+string(c))
		before("stop:"+string(c), "install")
		before("install", "start:"+string(c))
		before("start:"+string(c), "health:"+string(c))
		before("health:"+string(c), "commit")
	}
	if h.reload().Phase != "complete" {
		t.Fatal("completion was not durable")
	}
	h.events = nil
	if err := runJob(h.reload(), h.ops()); err != nil || len(h.events) != 0 {
		t.Fatal("completed job repeated host changes on relaunch", err, h.events)
	}
}

func TestWorkflowInterruptionRequiresRetryAfterUncertainStart(t *testing.T) {
	// These are workflow boundaries, not individual file writes or systemd calls.
	// Once a start may have happened, reboot must require an explicit retry.
	boundaries := []struct {
		name         string
		mayHaveRun   bool
		changesState bool
	}{
		{"stage", false, false}, {"guard", false, false}, {"stop:syncthing", false, true},
		{"stop:lnd", false, true}, {"stop:bitcoin", false, true}, {"install", false, false},
		{"start:bitcoin", true, true}, {"health:bitcoin", true, false},
		{"start:lnd", true, true}, {"health:lnd", true, false},
		{"start:syncthing", true, true}, {"health:syncthing", true, false}, {"commit", true, true},
	}
	for _, boundary := range boundaries {
		for _, after := range []bool{false, true} {
			// Only split before/after when the boundary changes fixture state.
			if after && !boundary.changesState {
				continue
			}
			for _, reboot := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/after=%v/reboot=%v", boundary.name, after, reboot), func(t *testing.T) {
					j := workflowFixture()
					h := newHarness(t, j)
					h.interrupt, h.after = boundary.name, after
					runInterrupted(t, j, h.ops())
					j = h.reload()
					history := maps.Clone(j.MayHaveRun)
					h.interrupt = ""
					h.events = nil
					if reboot {
						h.running = map[protocol.Component]bool{}
					}
					beforeBinary, readErr := os.ReadFile(h.binary)
					if readErr != nil {
						t.Fatal(readErr)
					}
					err := runJob(j, h.ops())
					wantRetry := (reboot && boundary.mayHaveRun) || (!after && strings.HasPrefix(boundary.name, "start:"))
					if wantRetry {
						if err == nil || h.reload().Phase != "failed" {
							t.Fatal("uncertain start did not require explicit retry", err)
						}
						if !slices.Contains(h.events, "quarantine") || slices.ContainsFunc(h.events, func(event string) bool {
							return event != "quarantine" && !strings.HasPrefix(event, "health:") && !(event == "commit" && boundary.name == "commit")
						}) {
							t.Fatal("uncertain start caused further host changes", h.events)
						}
						// The real commit may be entered to check processes, but
						// refusal must leave the installed executable intact.
						afterBinary, readErr := os.ReadFile(h.binary)
						if readErr != nil || string(afterBinary) != string(beforeBinary) {
							t.Fatal("uncertain start replaced VPN", readErr)
						}
					} else if err != nil || h.reload().Phase != "complete" {
						t.Fatal("safe continuation failed", err, j.Phase)
					}
					for c, v := range history {
						if v && !h.reload().MayHaveRun[c] {
							t.Fatal("forgot possible database migration")
						}
					}
				})
			}
		}
	}
}

func TestRejectedArtifactLeavesServicesAloneAndFailureNeverCommits(t *testing.T) {
	failures := []string{"stage", "guard", "stop:lnd", "install", "start:lnd", "health:bitcoin", "health:syncthing"}
	// The last entry repeats the download failure for a repair job, which
	// starts with host changes already begun.
	for i, failure := range append(failures, "stage") {
		j := workflowFixture()
		if i == len(failures) {
			j.Previous, j.HostChanges = strings.Repeat("b", 64), true
		}
		h := newHarness(t, j)
		h.failure = failure
		if runJob(j, h.ops()) == nil {
			t.Fatal(failure, "reported success")
		}
		if saved := h.reload(); saved.Phase != "failed" || !strings.Contains(saved.Error, "injected failure") {
			t.Fatal(failure, "lost its failure record", saved)
		}
		data, err := os.ReadFile(h.binary)
		if err != nil || string(data) != "old VPN" || slices.Contains(h.events, "commit") {
			t.Fatal("installed VPN before healthy components", err)
		}
		if failure == "stage" && !slices.Equal(h.events, []string{"stage"}) {
			t.Fatal("verification failure touched services", h.events)
		}
		for _, c := range j.Affected {
			if h.running[c] != (failure == "stage") {
				t.Fatal(failure, "left unsafe service state", c, h.running)
			}
		}
		h.events = nil
		if err := runJob(h.reload(), h.ops()); err != nil || len(h.events) != 0 {
			t.Fatal("known failure automatically retried on boot")
		}
	}
}

func TestFailedBoundarySavePreventsStart(t *testing.T) {
	j := workflowFixture()
	h := newHarness(t, j)
	ops := h.ops()
	ops.save = func(j *job) error {
		if j.Started[protocol.Bitcoin] {
			return errors.New("disk full")
		}
		return h.checkpoint(j)
	}
	if runJob(j, ops) == nil {
		t.Fatal("ignored failed journal write")
	}
	if slices.Contains(h.events, "start:bitcoin") {
		t.Fatal("started with no durable migration boundary")
	}
}

// Cancel is decided by whether host changes may have begun, not by how far the
// download got. Retry must not reopen that choice.
func TestCancellationStopsWhereHostChangesBegin(t *testing.T) {
	for _, tc := range []struct {
		failure     string
		cancellable bool
	}{
		{"stage", true}, {"guard", false}, {"stop:lnd", false}, {"start:bitcoin", false},
	} {
		t.Run(tc.failure, func(t *testing.T) {
			j := workflowFixture()
			h := newHarness(t, j)
			h.failure = tc.failure
			if runJob(j, h.ops()) == nil {
				t.Fatal("fixture did not fail")
			}
			if !tc.cancellable {
				if err := retryUpdate(h.root, j.Review.Digest); err != nil {
					t.Fatal(err)
				}
			}
			err := cancelUpdate(h.root, j.Review.Digest)
			if (err == nil) != tc.cancellable {
				t.Fatal("wrong cancellation boundary:", err)
			}
			if h.reload().active() == tc.cancellable {
				t.Fatal("maintenance restriction does not match the cancellation")
			}
		})
	}
}

// A space refusal must never be the reason services stop, start or get blocked.
func TestSpaceRefusalLeavesServicesAsFound(t *testing.T) {
	for _, tc := range []struct {
		name        string
		reach       func(*testing.T, *workflowHarness, *job)
		discards    bool
		running     bool
		cancellable bool
	}{
		{"first run", func(*testing.T, *workflowHarness, *job) {}, true, true, true},
		{"resumed after download, before host changes", func(t *testing.T, h *workflowHarness, j *job) {
			h.crash = func(j *job) bool { return j.Completed["staged"] && !j.HostChanges }
			runInterrupted(t, j, h.ops())
			h.crash = nil
		}, true, true, true},
		{"repair before its download finishes", func(t *testing.T, h *workflowHarness, j *job) {
			j.Previous, j.HostChanges = strings.Repeat("b", 64), true
			if err := h.checkpoint(j); err != nil {
				t.Fatal(err)
			}
		}, true, true, false},
		{"resumed at the service guard", func(t *testing.T, h *workflowHarness, j *job) {
			h.interrupt = "guard"
			runInterrupted(t, j, h.ops())
			h.interrupt = ""
		}, false, true, false},
		{"retry after quarantine", func(t *testing.T, h *workflowHarness, j *job) {
			h.failure = "start:lnd"
			if runJob(j, h.ops()) == nil {
				t.Fatal("fixture did not fail")
			}
			if err := retryUpdate(h.root, j.Review.Digest); err != nil {
				t.Fatal(err)
			}
		}, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := workflowFixture()
			h := newHarness(t, j)
			tc.reach(t, h, j)
			j = h.reload()
			history := maps.Clone(j.MayHaveRun)
			h.events, h.failure = nil, "capacity"
			if runJob(j, h.ops()) == nil {
				t.Fatal("ignored insufficient space")
			}
			if slices.ContainsFunc(h.events, func(e string) bool { return e != "stage" && e != "capacity" && e != "discard" }) {
				t.Fatalf("space refusal touched services: %v", h.events)
			}
			// Downloads are returned only while no host change can have begun;
			// afterwards Retry still needs them.
			if slices.Contains(h.events, "discard") != tc.discards {
				t.Fatalf("wrong download retention: %v", h.events)
			}
			saved := h.reload()
			if saved.Phase != "failed" || !strings.Contains(saved.Error, "injected failure") || !maps.Equal(saved.MayHaveRun, history) {
				t.Fatal("lost the refusal or migration history", saved)
			}
			for _, c := range j.Affected {
				if h.running[c] != tc.running {
					t.Fatal("space refusal changed service state", c, h.running)
				}
			}
			// Removed downloads must be fetched again; kept ones must not be.
			if saved.Completed["staged"] == tc.discards {
				t.Fatal("download record disagrees with the files on disk")
			}
			if (cancelUpdate(h.root, j.Review.Digest) == nil) != tc.cancellable {
				t.Fatal("wrong cancellation boundary")
			}
		})
	}
}

func TestFailedCleanupDoesNotUndoCompletion(t *testing.T) {
	j := workflowFixture()
	h := newHarness(t, j)
	h.failure = "discard"
	if err := runJob(j, h.ops()); err != nil || h.reload().Phase != "complete" {
		t.Fatal("leftover downloads failed a completed update", err)
	}
}
