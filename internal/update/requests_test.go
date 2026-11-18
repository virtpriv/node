package update

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/virtpriv/node/internal/system"
	"github.com/virtpriv/node/internal/update/files"
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
			node := nodeObservation{source: j.Review.Source, configHash: j.ConfigHash}
			wantError := ""
			wantLaunches := 0
			switch change {
			case "configuration":
				node.configHash = strings.Repeat("b", 64)
				wantError = "node changed since review"
			case "source":
				node.source.VPN = "0.6.9"
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

// A repair release follows a failed update. It is accepted only when its plan
// names that failed release, and it keeps the failed update's record and its
// answer to Cancel.
func TestCorrectiveAdmissionPreservesFailedUpdateObligations(t *testing.T) {
	for _, refusal := range []string{"", "ordinary release", "different failed update", "configuration", "unfinished"} {
		t.Run("refusal="+refusal, func(t *testing.T) {
			previous := workflowFixture()
			previous.Phase, previous.HostChanges = "failed", true
			j := workflowFixture()
			j.Review.Digest = strings.Repeat("b", 64)
			j.Review.Manifest.Version = "0.7.2"
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
				previous.Phase = "committing"
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
			launches := 0
			err = acceptUpdate(root, token, "20261109-100000-aaaaaa", admissionOps{
				observe: func() (nodeObservation, error) {
					return nodeObservation{source: p.Review.Source, configHash: p.ConfigHash}, nil
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
			if err != nil || j == nil || j.Previous != previous.Review.Digest {
				t.Fatal("corrective record missing", err)
			}
			// The installed side carries the cancellation boundary, and keeps
			// the failed update's record for the repair's worker.
			if cancelUpdate(root, j.Review.Digest) == nil {
				t.Fatal("repair of a changed node could be cancelled before its worker ran")
			}
			var retained job
			if err := readJSON(filepath.Join(previous.dir(root), "result.json"), &retained); err != nil || retained.Review.Digest != previous.Review.Digest || !retained.HostChanges {
				t.Fatal("lost failed release evidence", err)
			}
			h := newHarness(t, j)
			if err := runJob(j, h.ops()); err != nil || h.installed() != "new VPN" {
				t.Fatal("the repair did not finish the update:", err)
			}
		})
	}
}

// A repair can itself fail before its worker ever runs. The next repair must
// still refuse Cancel, since the first failure may have replaced the program.
func TestRepairOfRepairKeepsTheCancellationBoundary(t *testing.T) {
	first := workflowFixture()
	first.Phase, first.HostChanges = "failed", true
	second := workflowFixture()
	second.Review.Digest = strings.Repeat("b", 64)
	second.Review.Manifest.Version = "0.7.2"
	second.Review.Manifest.RecoveryFrom = []string{first.Review.Digest}
	third := workflowFixture()
	third.Review.Digest = strings.Repeat("c", 64)
	third.Review.Manifest.Version = "0.7.3"
	third.Review.Manifest.RecoveryFrom = []string{second.Review.Digest}
	root, _ := workerFiles(t, second)
	if err := os.Mkdir(first.dir(root), 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveJob(root, first); err != nil {
		t.Fatal(err)
	}
	accept := func(j *job, previous string) {
		t.Helper()
		token := saveReview(t, root, prepared{Review: j.Review, WorkerHash: j.WorkerHash, PlanHash: j.PlanHash, ConfigHash: j.ConfigHash, Previous: previous})
		if err := acceptUpdate(root, token, "20261109-100000-aaaaaa", admissionOps{
			observe: func() (nodeObservation, error) {
				return nodeObservation{source: j.Review.Source, configHash: j.ConfigHash}, nil
			},
			launcher: func() error { return nil },
		}); err != nil {
			t.Fatal(err)
		}
	}
	accept(second, first.Review.Digest)
	// The second update fails before its worker runs.
	if err := amendRecord(root, map[string]any{"phase": "failed", "error": "worker did not start"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(third.dir(root), 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"vpn": "new VPN"} {
		if err := os.WriteFile(filepath.Join(third.dir(root), name), []byte(data), 0700); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := json.Marshal(third.Review.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(third.dir(root), "update.json"), plan, 0600); err != nil {
		t.Fatal(err)
	}
	if third.WorkerHash, err = files.Hash(filepath.Join(third.dir(root), "vpn")); err != nil {
		t.Fatal(err)
	}
	if third.PlanHash, err = files.Hash(filepath.Join(third.dir(root), "update.json")); err != nil {
		t.Fatal(err)
	}
	accept(third, second.Review.Digest)
	if cancelUpdate(root, third.Review.Digest) == nil {
		t.Fatal("the second repair could be cancelled")
	}
	for _, failed := range []*job{first, second} {
		if _, err := os.Stat(filepath.Join(failed.dir(root), "result.json")); err != nil {
			t.Fatal("lost a failed update's record:", err)
		}
	}
}

// Node Updates offers Retry and Cancel update only for an update that does
// not run. An update that systemd is starting, or whose start waits for
// another unit, runs with no step from the owner. The answers are systemd's
// own on Debian 13, with the update's unit name.
func TestStartingOrWaitingUpdateCountsAsRunning(t *testing.T) {
	for _, c := range []struct {
		name, answer string
		runs         bool
	}{
		{"its start waits for another unit", "Id=vpn-update.service\nActiveState=inactive\nJob=7104\n", true},
		{"systemd is starting it", "Id=vpn-update.service\nActiveState=activating\nJob=7611\n", true},
		{"it runs", "Id=vpn-update.service\nActiveState=active\nJob=\n", true},
		{"it ended or never ran", "Id=vpn-update.service\nActiveState=inactive\nJob=\n", false},
		{"its program could not be run", "Id=vpn-update.service\nActiveState=failed\nJob=\n", false},
		{"systemd is starting it, with no job on it", "Id=vpn-update.service\nActiveState=activating\nJob=\n", true},
		{"it is being stopped", "Id=vpn-update.service\nActiveState=deactivating\nJob=1048\n", true},
		{"its program ended and its shutdown still runs", "Id=vpn-update.service\nActiveState=deactivating\nJob=\n", false},
		{"systemd gave no answer", "", false},
		// systemd chooses the order of the lines.
		{"the lines come in another order", "Job=7104\nActiveState=inactive\nId=vpn-update.service\n", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := unitRuns(system.ParseUnitProperties(c.answer)[Service]); got != c.runs {
				t.Fatalf("running is %v, want %v", got, c.runs)
			}
		})
	}
}

// Retry needs the update on record, and cannot reopen a finished one.
func TestRetryNeedsTheSavedUpdateAndCannotReopenAFinishedOne(t *testing.T) {
	j := workflowFixture()
	h := newHarness(t, j)
	h.failure = "check"
	if err := runJob(j, h.ops()); err == nil {
		t.Fatal("fixture did not reach the failure")
	}
	if err := retryUpdate(h.root, strings.Repeat("b", 64)); err == nil || h.reload().Phase != "failed" {
		t.Fatal("stale retry changed the saved job", err)
	}
	if err := retryUpdate(h.root, j.Review.Digest); err != nil {
		t.Fatal(err)
	}
	h.failure = ""
	if err := runJob(h.reload(), h.ops()); err != nil || h.reload().Phase != "complete" {
		t.Fatal("explicit retry could not complete the saved job", err)
	}
	if err := retryUpdate(h.root, j.Review.Digest); err == nil || h.reload().Phase != "complete" {
		t.Fatal("retry reopened a completed update", err)
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
	err := acceptUpdate(root, token, "20261109-100000-aaaaaa", admissionOps{
		observe: func() (nodeObservation, error) {
			return nodeObservation{source: j.Review.Source, configHash: j.ConfigHash}, nil
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
			h.failure = "check"
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
