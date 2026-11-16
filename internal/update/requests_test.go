package update

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/update/protocol"
)

func saveReview(t *testing.T, root string, p prepared) string {
	t.Helper()
	token, err := approvalToken(p)
	if err != nil {
		t.Fatal(err)
	}
	p.Review.Token = token
	if err := os.MkdirAll(filepath.Join(root, "reviews"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(root, "reviews", token+".json"), p); err != nil {
		t.Fatal(err)
	}
	return token
}

func TestStartRechecksNodeAndWorkerBeforeAcceptingReview(t *testing.T) {
	for _, change := range []string{"", "configuration", "source", "worker", "plan", "observation", "launcher"} {
		t.Run("change="+change, func(t *testing.T) {
			j := workflowFixture()
			root, _ := workerFiles(t, j)
			p := prepared{Review: j.Review, WorkerHash: j.WorkerHash, PlanHash: j.PlanHash, ConfigHash: j.ConfigHash}
			token := saveReview(t, root, p)
			cfg := config.Default()
			cfg.Network = j.Review.Network
			node := nodeObservation{source: j.Review.Source, config: cfg, configHash: j.ConfigHash}
			wantError := ""
			wantLaunches := 0
			switch change {
			case "configuration":
				node.configHash = strings.Repeat("b", 64)
				wantError = "node changed since review"
			case "source":
				node.source.LND = "0.21.0-beta"
				wantError = "node changed since review"
			case "worker":
				if err := os.WriteFile(filepath.Join(j.dir(root), "vpn"), []byte("changed worker"), 0700); err != nil {
					t.Fatal(err)
				}
				wantError = "integrity verification"
			case "plan":
				if err := os.WriteFile(filepath.Join(j.dir(root), "update.json"), []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
				wantError = "integrity verification"
			case "observation":
				wantError = "node unavailable"
			case "launcher":
				wantError, wantLaunches = "launcher unavailable", 1
			}
			launches := 0
			ops := admissionOps{
				observe: func() (nodeObservation, error) {
					if change == "observation" {
						return nodeObservation{}, errors.New(wantError)
					}
					return node, nil
				},
				launcher: func() error {
					launches++
					if change == "launcher" {
						return errors.New(wantError)
					}
					return nil
				},
			}
			err := acceptUpdate(root, token, "20261109-100000-aaaaaa", ops)
			if wantError != "" {
				if err == nil || !strings.Contains(err.Error(), wantError) {
					t.Fatalf("wrong refusal: %v", err)
				}
				if _, err := os.Stat(filepath.Join(root, "current.json")); !os.IsNotExist(err) || launches != wantLaunches {
					t.Fatal("rejected review advanced admission", err)
				}
				return
			}
			if err != nil || launches != 1 {
				t.Fatalf("valid review not accepted: %v", err)
			}
			accepted, err := loadJob(root)
			if err != nil || accepted == nil || accepted.Review.Token != token || accepted.Phase != "accepted" {
				t.Fatal("accepted the wrong review", err)
			}
			if err := acceptUpdate(root, token, "20261109-100000-aaaaaa", ops); err == nil || !strings.Contains(err.Error(), "already accepted") || launches != 1 {
				t.Fatal("replayed approval launched another job", err)
			}
		})
	}
}

// A plan may name networks and services added after this helper was built.
// The helper must still accept the job. The newer worker checks those names.
func TestHelperAcceptsReviewOfPlanWithLaterNames(t *testing.T) {
	j := workflowFixture()
	j.Review.Manifest.Networks = append(j.Review.Manifest.Networks, "testnet5")
	j.Review.Manifest.HostServices = []protocol.Component{"tor"}
	root, _ := workerFiles(t, j)
	p := prepared{Review: j.Review, WorkerHash: j.WorkerHash, PlanHash: j.PlanHash, ConfigHash: j.ConfigHash}
	token := saveReview(t, root, p)
	cfg := config.Default()
	cfg.Network = j.Review.Network
	err := acceptUpdate(root, token, "20261109-100000-aaaaaa", admissionOps{
		observe: func() (nodeObservation, error) {
			return nodeObservation{source: j.Review.Source, config: cfg, configHash: j.ConfigHash}, nil
		},
		launcher: func() error { return nil },
	})
	if err != nil {
		t.Fatal("helper refused a plan over names it does not need:", err)
	}
	if r, err := loadRecord(root); err != nil || r == nil || !r.relaunch() {
		t.Fatal("accepted job is not ready for its worker:", err)
	}
	cfg.Network = "mainnet"
	if err := cancelUpdate(root, j.Review.Digest); err != nil {
		t.Fatal(err)
	}
	if acceptUpdate(root, saveReview(t, root, p), "20261109-100000-aaaaaa", admissionOps{
		observe: func() (nodeObservation, error) {
			return nodeObservation{source: j.Review.Source, config: cfg, configHash: j.ConfigHash}, nil
		},
		launcher: func() error { return nil },
	}) == nil {
		t.Fatal("later names admitted a node on a network the plan does not list")
	}
}

func TestCorrectiveAdmissionPreservesFailedUpdateObligations(t *testing.T) {
	for _, refusal := range []string{"", "ordinary release", "different failed update", "configuration", "unfinished"} {
		t.Run("refusal="+refusal, func(t *testing.T) {
			previous := workflowFixture()
			previous.Phase = "failed"
			previous.Completed["staged"], previous.HostChanges = true, true
			previous.Started[protocol.LND], previous.MayHaveRun[protocol.LND] = true, true
			previous.WalletPresent, previous.SyncthingID = true, "original device"
			j := workflowFixture()
			j.Review.Digest = strings.Repeat("b", 64)
			j.Review.Manifest.Version = "0.7.2"
			// Core and LND binaries were replaced by the failed update. The
			// corrective plan alone would now affect only Syncthing.
			j.Review.Source.Bitcoin = j.Review.Manifest.Bitcoin.Version
			j.Review.Source.LND = j.Review.Manifest.LND.Version
			j.Review.Manifest.Sources = []protocol.Versions{j.Review.Source}
			j.Review.Manifest.RecoveryFrom = []string{previous.Review.Digest}
			root, _ := workerFiles(t, j)
			p := prepared{Review: j.Review, WorkerHash: j.WorkerHash, PlanHash: j.PlanHash, ConfigHash: j.ConfigHash, Previous: previous.Review.Digest}
			wantError := ""
			switch refusal {
			case "ordinary release":
				p.Review.Manifest.RecoveryFrom = nil
				wantError = "cannot repair the unfinished update"
			case "different failed update":
				p.Previous = strings.Repeat("c", 64)
				wantError = "update state changed"
			case "configuration":
				p.ConfigHash = strings.Repeat("c", 64)
				wantError = "configuration changed during the failed update"
			case "unfinished":
				previous.Phase = "checking"
				wantError = "another update is unfinished"
			}
			if err := os.Mkdir(previous.dir(root), 0700); err != nil {
				t.Fatal(err)
			}
			if err := saveJob(root, previous); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(root, "current.json"))
			if err != nil {
				t.Fatal(err)
			}
			token := saveReview(t, root, p)
			cfg := config.Default()
			cfg.Network = p.Review.Network
			launches := 0
			err = acceptUpdate(root, token, "20261109-100000-aaaaaa", admissionOps{
				observe: func() (nodeObservation, error) {
					return nodeObservation{source: p.Review.Source, config: cfg, configHash: p.ConfigHash}, nil
				},
				launcher: func() error { launches++; return nil },
			})
			if wantError != "" {
				after, readErr := os.ReadFile(filepath.Join(root, "current.json"))
				if err == nil || !strings.Contains(err.Error(), wantError) || readErr != nil || string(after) != string(before) || launches != 0 {
					t.Fatalf("failed update was not preserved: admission=%v read=%v", err, readErr)
				}
				return
			}
			if err != nil || launches != 1 {
				t.Fatal("corrective admission failed", err)
			}
			j, err = loadJob(root)
			if err != nil || j == nil {
				t.Fatal("corrective record missing", err)
			}
			// The helper only carries the cancellation boundary. The repair
			// worker reads the failed job's duties from its retained record.
			if cancelUpdate(root, j.Review.Digest) == nil {
				t.Fatal("repair of a changed node could be cancelled before its worker ran")
			}
			if err := inheritFailed(root, j); err != nil {
				t.Fatal(err)
			}
			if !j.MayHaveRun[protocol.LND] || !j.WalletPresent || j.SyncthingID != previous.SyncthingID {
				t.Fatal("repair forgot data or identity obligations")
			}
			var retained job
			if err := readJSON(filepath.Join(previous.dir(root), "result.json"), &retained); err != nil || retained.Review.Digest != previous.Review.Digest || !retained.MayHaveRun[protocol.LND] {
				t.Fatal("lost failed release evidence", err)
			}
			h := newHarness(t, j)
			if err := runJob(j, h.ops()); err != nil {
				t.Fatal(err)
			}
			for _, c := range previous.Affected {
				if !slices.Contains(h.events, "stop:"+string(c)) || !slices.Contains(h.events, "health:"+string(c)) {
					t.Fatalf("corrective update abandoned %s: %v", c, h.events)
				}
			}
			if slices.Index(h.events, "health:bitcoin") >= slices.Index(h.events, "start:lnd") {
				t.Fatal("inherited dependencies started in the wrong order")
			}
		})
	}
}

// A repair can itself fail before its worker ever runs. The next repair must
// still find the duties of the first failure.
func TestRepairOfRepairKeepsFirstFailureDuties(t *testing.T) {
	first := workflowFixture()
	first.Phase, first.HostChanges = "failed", true
	first.MayHaveRun[protocol.LND] = true
	first.WalletPresent, first.SyncthingID = true, "original device"
	second := workflowFixture()
	second.Review.Digest, second.Previous = strings.Repeat("b", 64), first.Review.Digest
	second.Phase, second.HostChanges, second.Affected = "failed", true, nil
	third := workflowFixture()
	third.Review.Digest, third.Previous = strings.Repeat("c", 64), second.Review.Digest
	third.HostChanges, third.Affected = true, nil
	third.Review.Source = third.Review.Manifest.Target(third.Review.Source)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, failed := range []*job{first, second} {
		if err := os.Mkdir(failed.dir(root), 0700); err != nil {
			t.Fatal(err)
		}
		if err := writeJSON(filepath.Join(failed.dir(root), "result.json"), failed); err != nil {
			t.Fatal(err)
		}
	}
	if err := inheritFailed(root, third); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(third.Affected, first.Affected) || !third.MayHaveRun[protocol.LND] || !third.WalletPresent || third.SyncthingID != first.SyncthingID {
		t.Fatal("second repair lost the first failure's duties", third.Affected, third.MayHaveRun)
	}
	// A missing record must stop the repair, never shrink its duties.
	if err := os.Remove(filepath.Join(first.dir(root), "result.json")); err != nil {
		t.Fatal(err)
	}
	fresh := workflowFixture()
	fresh.Previous, fresh.Affected = second.Review.Digest, nil
	if inheritFailed(root, fresh) == nil {
		t.Fatal("repair continued without the first failure's record")
	}
}

func TestExplicitRetryCompletesSavedFailureWithoutLosingMigrationHistory(t *testing.T) {
	for _, failure := range []string{"stage", "start:lnd"} {
		t.Run(failure, func(t *testing.T) {
			j := workflowFixture()
			h := newHarness(t, j)
			h.failure = failure
			if err := runJob(j, h.ops()); err == nil {
				t.Fatal("fixture did not reach the failure")
			}
			if err := retryUpdate(h.root, strings.Repeat("b", 64)); err == nil || h.reload().Phase != "failed" {
				t.Fatal("stale retry changed the saved job", err)
			}
			if err := retryUpdate(h.root, j.Review.Digest); err != nil {
				t.Fatal(err)
			}
			j = h.reload()
			if failure == "start:lnd" && !j.MayHaveRun[protocol.LND] {
				t.Fatal("explicit retry erased possible migration")
			}
			h.failure, h.events = "", nil
			if err := runJob(j, h.ops()); err != nil || h.reload().Phase != "complete" {
				t.Fatal("explicit retry could not complete the saved job", err)
			}
			if slices.Contains(h.events, "stage") != (failure == "stage") {
				t.Fatal("retry discarded verified staging or skipped unfinished staging")
			}
			if err := retryUpdate(h.root, j.Review.Digest); err == nil || h.reload().Phase != "complete" {
				t.Fatal("retry reopened a completed update", err)
			}
		})
	}
}

// The notice about an unreachable key server is part of what the operator
// approved, but the job record is read by every later worker and keeps its
// existing fields.
func TestKeyServerNoticeIsApprovedButNotKeptInTheJob(t *testing.T) {
	j := workflowFixture()
	root, _ := workerFiles(t, j)
	review := j.Review
	review.KeyServerUnreachable = true
	token := saveReview(t, root, prepared{Review: review, WorkerHash: j.WorkerHash, PlanHash: j.PlanHash, ConfigHash: j.ConfigHash})
	cfg := config.Default()
	cfg.Network = j.Review.Network
	err := acceptUpdate(root, token, "20261109-100000-aaaaaa", admissionOps{
		observe: func() (nodeObservation, error) {
			return nodeObservation{source: j.Review.Source, config: cfg, configHash: j.ConfigHash}, nil
		},
		launcher: func() error { return nil },
	})
	if err != nil {
		t.Fatalf("review with the notice was not accepted: %v", err)
	}
	record, err := os.ReadFile(filepath.Join(root, "current.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(record), "key_server_unreachable") {
		t.Fatal("job record gained a field that later workers do not expect")
	}
}

// Retry and Cancel clear the next step and advice with the error, so the
// screen never shows advice about a failure that is gone.
func TestRetryAndCancelLeaveNoOldAdvice(t *testing.T) {
	for _, action := range []string{"retry", "cancel"} {
		t.Run(action, func(t *testing.T) {
			j := workflowFixture()
			h := newHarness(t, j)
			h.failure = "stage"
			if err := runJob(j, h.ops()); err == nil {
				t.Fatal("fixture did not reach the failure")
			}
			rewriteRecord(t, h.root, func(m map[string]any) { m["next"], m["advice"] = "retry", "Select Retry." })
			act := retryUpdate
			if action == "cancel" {
				act = cancelUpdate
			}
			if err := act(h.root, j.Review.Digest); err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(filepath.Join(h.root, "current.json"))
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]any
			if err := json.Unmarshal(b, &m); err != nil {
				t.Fatal(err)
			}
			if _, ok := m["next"]; ok {
				t.Fatalf("%s kept the next step", action)
			}
			if _, ok := m["advice"]; ok {
				t.Fatalf("%s kept the advice", action)
			}
		})
	}
}

// Prepare's cleanup decides what is deleted on every node's first update. A
// wrong delete breaks a repair, which reads past updates' folders.
func TestPrepareRemovesOnlyEarlierReviews(t *testing.T) {
	digest := func(c string) string { return strings.Repeat(c, 64) }
	mkdir := func(t *testing.T, path string, mode os.FileMode) {
		t.Helper()
		if err := os.Mkdir(path, mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	write := func(t *testing.T, path string) {
		t.Helper()
		if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	setup := func(t *testing.T) (root, outside string) {
		t.Helper()
		base, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		root, outside = filepath.Join(base, "updates"), filepath.Join(base, "outside")
		mkdir(t, root, 0700)
		mkdir(t, outside, 0700)
		write(t, filepath.Join(outside, "keep"))
		write(t, filepath.Join(root, "current.json"))
		write(t, filepath.Join(root, "launcher"))
		// The current update's folder, a past update's, and a review that
		// never became an update, with a link inside it.
		for _, c := range []string{"a", "b", "c"} {
			mkdir(t, filepath.Join(root, digest(c)), 0700)
			write(t, filepath.Join(root, digest(c), "vpn"))
		}
		write(t, filepath.Join(root, digest("b"), "result.json"))
		if err := os.Symlink(filepath.Join(outside, "keep"), filepath.Join(root, digest("c"), "link")); err != nil {
			t.Fatal(err)
		}
		mkdir(t, filepath.Join(root, "prepare-123"), 0700)
		write(t, filepath.Join(root, "prepare-123", "vpn.tar.gz"))
		mkdir(t, filepath.Join(root, "reviews"), 0700)
		write(t, filepath.Join(root, "reviews", digest("d")+".json"))
		write(t, filepath.Join(root, "reviews", digest("e")+".json"))
		write(t, filepath.Join(root, "reviews", "other.json"))
		// Entries vpn never makes stay, whatever their name.
		if err := os.Symlink(outside, filepath.Join(root, digest("f"))); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, "prepare-link")); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(root, digest("1")))
		write(t, filepath.Join(root, "prepare-file"))
		mkdir(t, filepath.Join(root, digest("2")), 0770)
		mkdir(t, filepath.Join(root, "other"), 0700)
		mkdir(t, filepath.Join(root, strings.ToUpper(digest("d"))), 0700)
		return root, outside
	}
	check := func(t *testing.T, root, outside string, kept, removed []string) {
		t.Helper()
		for _, name := range kept {
			if _, err := os.Lstat(filepath.Join(root, name)); err != nil {
				t.Errorf("%s was removed: %v", name, err)
			}
		}
		for _, name := range removed {
			if _, err := os.Lstat(filepath.Join(root, name)); !os.IsNotExist(err) {
				t.Errorf("%s was kept", name)
			}
		}
		if _, err := os.Stat(filepath.Join(outside, "keep")); err != nil {
			t.Errorf("a file outside the update folder was removed: %v", err)
		}
	}
	odd := []string{"current.json", "launcher", "reviews", "reviews/other.json", digest("f"), "prepare-link", digest("1"), "prepare-file", digest("2"), "other", strings.ToUpper(digest("d"))}
	earlier := []string{"prepare-123", "reviews/" + digest("d") + ".json", "reviews/" + digest("e") + ".json"}

	t.Run("with an update record", func(t *testing.T) {
		root, outside := setup(t)
		removeEarlierReviews(root, digest("a"))
		check(t, root, outside, append(odd, digest("a"), digest("b")), append(earlier, digest("c")))
	})
	t.Run("with a link for the review folder", func(t *testing.T) {
		root, outside := setup(t)
		if err := os.RemoveAll(filepath.Join(root, "reviews")); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(outside, digest("d")+".json"))
		if err := os.Symlink(outside, filepath.Join(root, "reviews")); err != nil {
			t.Fatal(err)
		}
		removeEarlierReviews(root, digest("a"))
		if _, err := os.Stat(filepath.Join(outside, digest("d")+".json")); err != nil {
			t.Errorf("a file the review folder's link points at was removed: %v", err)
		}
	})
	t.Run("with a review file vpn did not make", func(t *testing.T) {
		root, outside := setup(t)
		if err := os.Chmod(filepath.Join(root, "reviews", digest("d")+".json"), 0666); err != nil {
			t.Fatal(err)
		}
		removeEarlierReviews(root, digest("a"))
		check(t, root, outside, append(odd, digest("a"), digest("b"), digest("c"), "reviews/"+digest("d")+".json"), []string{"prepare-123", "reviews/" + digest("e") + ".json"})
	})
	t.Run("without an update record", func(t *testing.T) {
		root, outside := setup(t)
		removeEarlierReviews(root, "")
		check(t, root, outside, append(odd, digest("b")), append(earlier, digest("a"), digest("c")))
	})
}
