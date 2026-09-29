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
	source := protocol.Versions{VPN: "0.7.0", Bitcoin: "29.2", LND: "0.21.1-beta", Syncthing: "2.1.4"}
	m := protocol.Manifest{Protocol: 1, Platform: "debian-13-amd64", Version: "0.7.1", Summary: "Fixture only", MinimumFreeMiB: 2048, Sources: []protocol.Versions{source}, Networks: []string{"public-signet"}, Bitcoin: protocol.Artifact{Version: "29.3", SHA256: h}, LND: protocol.Artifact{Version: "0.21.2-beta", SHA256: h}, Syncthing: protocol.Artifact{Version: "2.1.5", SHA256: h}}
	return &job{Schema: 1, Review: protocol.Review{Digest: h, Manifest: m, Source: source, Network: "public-signet"}, WorkerHash: h, ConfigHash: h, Phase: "accepted", Started: map[protocol.Component]bool{}, MayHaveRun: map[protocol.Component]bool{}, Completed: map[string]bool{}, BinaryHashes: map[string]string{}, Affected: m.Affected(source)}
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
}

func (h *workflowHarness) checkpoint(j *job) error { return saveJob(h.root, j) }
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
		save:  h.checkpoint,
		stage: func(*job) error { return h.effect("stage", nil) },
		guard: func([]protocol.Component) error { return h.effect("guard", nil) },
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
	before("stage", "guard")
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
	for _, failure := range []string{"stage", "guard", "stop:lnd", "install", "start:lnd", "health:bitcoin", "health:syncthing"} {
		j := workflowFixture()
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

func TestCancellationCannotAbandonPartiallyInstalledComponents(t *testing.T) {
	j := workflowFixture()
	j.Phase = "failed"
	if err := cancelJob(j); err != nil || j.active() {
		t.Fatal("failed download kept node locked", err)
	}
	j = workflowFixture()
	j.Completed["staged"] = true
	j.Phase = "failed"
	if cancelJob(j) == nil || !j.active() {
		t.Fatal("abandoned possible host changes")
	}
	j = workflowFixture()
	j.Previous = strings.Repeat("b", 64)
	j.Phase = "failed"
	if cancelJob(j) == nil || !j.active() {
		t.Fatal("failed corrective download abandoned the earlier migration")
	}
}
