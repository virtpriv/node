package update

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/virtpriv/node/internal/update/protocol"
)

func workflowFixture() *job {
	h := strings.Repeat("a", 64)
	source := protocol.Versions{VPN: "0.7.0", Bitcoin: "29.2", LND: "0.21.1-beta", Syncthing: "2.1.4"}
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
	// wallet and device are what the live node would answer. identifies and
	// walletReads count the readings, and eventsBeforeIdentity is how much had
	// happened by the last one.
	wallet               bool
	device               string
	identifies           int
	walletReads          int
	eventsBeforeIdentity int
}

// identify runs the worker's own rule against the fixture's services.
func (h *workflowHarness) identify(j *job) error {
	h.identifies++
	h.eventsBeforeIdentity = len(h.events)
	return observeIdentity(j, h.checkRunning,
		func() (bool, error) { h.walletReads++; return h.wallet, nil },
		func() string { return h.device })
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
		identify: h.identify,
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
	h := &workflowHarness{t: t, root: root, binary: binary, running: map[protocol.Component]bool{}, device: "fixture device"}
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

// The worker reads the node's identity itself, once, before it changes
// anything. After host changes begin, a stopped LND could no longer say
// whether it has a wallet.
func TestWorkerReadsIdentityOnlyBeforeHostChanges(t *testing.T) {
	t.Run("read once before any download or service change", func(t *testing.T) {
		j := workflowFixture()
		h := newHarness(t, j)
		h.wallet, h.device = true, "device"
		if err := runJob(j, h.ops()); err != nil {
			t.Fatal(err)
		}
		if h.identifies != 1 || h.eventsBeforeIdentity != 0 {
			t.Fatalf("identity read %d times, after %d host steps", h.identifies, h.eventsBeforeIdentity)
		}
		if saved := h.reload(); !saved.WalletPresent || saved.SyncthingID != "device" {
			t.Fatal("identity was not saved with the job")
		}
	})
	t.Run("a stopped service is refused without touching the others, and the refusal can be cancelled or retried", func(t *testing.T) {
		for _, answer := range []string{"cancel", "retry"} {
			j := workflowFixture()
			h := newHarness(t, j)
			h.running[protocol.LND] = false
			if runJob(j, h.ops()) == nil {
				t.Fatal("a node with a stopped service was updated")
			}
			if saved := h.reload(); saved.Phase != "failed" || saved.HostChanges || len(h.events) != 0 {
				t.Fatal("refusal reached the host:", saved.Phase, saved.HostChanges, h.events)
			}
			if h.walletReads != 0 {
				t.Fatal("asked a stopped LND about its wallet")
			}
			if !h.running[protocol.Bitcoin] || !h.running[protocol.Syncthing] {
				t.Fatal("refusal stopped a running service")
			}
			if answer == "cancel" {
				if err := cancelUpdate(h.root, j.Review.Digest); err != nil {
					t.Fatal("refusal could not be cancelled:", err)
				}
				continue
			}
			if err := retryUpdate(h.root, j.Review.Digest); err != nil {
				t.Fatal(err)
			}
			h.running[protocol.LND], h.wallet = true, true
			if err := runJob(h.reload(), h.ops()); err != nil || h.reload().Phase != "complete" {
				t.Fatal("retry after the refusal did not complete:", err)
			}
			if h.identifies != 2 || !h.reload().WalletPresent {
				t.Fatal("retry did not read the node again")
			}
		}
	})
	t.Run("a device without an identity is refused", func(t *testing.T) {
		j := workflowFixture()
		h := newHarness(t, j)
		h.device = ""
		if runJob(j, h.ops()) == nil || h.reload().HostChanges || len(h.events) != 0 {
			t.Fatal("updated Syncthing with no identity to compare afterwards", h.events)
		}
	})
	t.Run("an update that leaves LND alone does not need LND", func(t *testing.T) {
		j := workflowFixture()
		j.Review.Source.Bitcoin, j.Review.Source.LND = j.Review.Manifest.Bitcoin.Version, j.Review.Manifest.LND.Version
		j.Review.Manifest.Sources = []protocol.Versions{j.Review.Source}
		j.Affected = j.Review.Manifest.Affected(j.Review.Source)
		h := newHarness(t, j)
		if err := runJob(j, h.ops()); err != nil {
			t.Fatal(err)
		}
		if h.walletReads != 0 || !slices.Equal(j.Affected, []protocol.Component{protocol.Syncthing}) {
			t.Fatal("asked LND about its wallet for an update that does not touch it", j.Affected)
		}
	})
	t.Run("not read again after host changes began", func(t *testing.T) {
		j := workflowFixture()
		h := newHarness(t, j)
		h.wallet = true
		h.failure = "start:lnd"
		if runJob(j, h.ops()) == nil {
			t.Fatal("fixture did not fail")
		}
		if err := retryUpdate(h.root, j.Review.Digest); err != nil {
			t.Fatal(err)
		}
		h.failure = ""
		if err := runJob(h.reload(), h.ops()); err != nil {
			t.Fatal(err)
		}
		if h.identifies != 1 || !h.reload().WalletPresent {
			t.Fatal("retry read a stopped node and lost the wallet record")
		}
	})
	t.Run("a job from an earlier helper keeps the identity that helper saved", func(t *testing.T) {
		j := workflowFixture()
		j.WalletPresent, j.SyncthingID = true, "original device"
		j.HostChanges, j.Phase = true, "retry"
		j.Completed["staged"] = true
		h := newHarness(t, j)
		h.running = map[protocol.Component]bool{}
		if err := runJob(j, h.ops()); err != nil {
			t.Fatal(err)
		}
		if saved := h.reload(); h.identifies != 0 || !saved.WalletPresent || saved.SyncthingID != "original device" {
			t.Fatal("identity saved by the earlier helper was replaced")
		}
	})
}

// A repair inherits the identity saved before the failed update changed the
// host. If that update failed before it changed anything, nothing was saved
// and the node is still as it was, so the repair reads it.
func TestRepairReadsIdentityOnlyWhenTheFailedUpdateChangedNothing(t *testing.T) {
	for _, changed := range []bool{true, false} {
		t.Run(fmt.Sprintf("host changed=%v", changed), func(t *testing.T) {
			failed := workflowFixture()
			failed.Phase, failed.HostChanges = "failed", changed
			if changed {
				failed.WalletPresent, failed.SyncthingID = true, "original device"
				failed.MayHaveRun[protocol.LND] = true
			}
			j := workflowFixture()
			j.Review.Digest, j.Previous = strings.Repeat("b", 64), failed.Review.Digest
			j.HostChanges = changed
			h := newHarness(t, j)
			h.wallet, h.device = true, "device read now"
			if changed {
				// The failed update left its services stopped.
				h.running = map[protocol.Component]bool{}
			}
			if err := os.Mkdir(failed.dir(h.root), 0700); err != nil {
				t.Fatal(err)
			}
			if err := writeJSON(filepath.Join(failed.dir(h.root), "result.json"), failed); err != nil {
				t.Fatal(err)
			}
			if err := inheritFailed(h.root, j); err != nil {
				t.Fatal(err)
			}
			if err := runJob(j, h.ops()); err != nil {
				t.Fatal(err)
			}
			saved := h.reload()
			if changed && (h.identifies != 0 || !saved.WalletPresent || saved.SyncthingID != "original device") {
				t.Fatal("repair replaced the identity saved before the failed update")
			}
			if !changed && (h.identifies != 1 || h.eventsBeforeIdentity != 0 || !saved.WalletPresent || saved.SyncthingID != "device read now") {
				t.Fatal("repair went ahead without an identity")
			}
		})
	}
}
