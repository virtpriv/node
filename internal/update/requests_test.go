package update

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/virtualprivatenode/vpn/internal/config"
	"github.com/virtualprivatenode/vpn/internal/update/protocol"
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
	for _, change := range []string{"", "configuration", "source", "worker", "plan", "observation", "identity", "launcher"} {
		t.Run("change="+change, func(t *testing.T) {
			j := workflowFixture()
			root, _ := workerFiles(t, j)
			p := prepared{Review: j.Review, WorkerHash: j.WorkerHash, PlanHash: j.PlanHash, ConfigHash: j.ConfigHash}
			token := saveReview(t, root, p)
			cfg := config.Default()
			cfg.Network = j.Review.Network
			node := nodeObservation{source: j.Review.Source, config: cfg, configHash: j.ConfigHash}
			wantError := ""
			wantIdentities, wantLaunches := 0, 0
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
			case "identity":
				wantError, wantIdentities = "identity unavailable", 1
			case "launcher":
				wantError, wantIdentities, wantLaunches = "launcher unavailable", 1, 1
			}
			identities, launches := 0, 0
			ops := admissionOps{
				observe: func() (nodeObservation, error) {
					if change == "observation" {
						return nodeObservation{}, errors.New(wantError)
					}
					return node, nil
				},
				identify: func(*job, *config.AppConfig) error {
					identities++
					if change == "identity" {
						return errors.New(wantError)
					}
					return nil
				},
				launcher: func() error {
					launches++
					if change == "launcher" {
						return errors.New(wantError)
					}
					return nil
				},
			}
			err := acceptUpdate(root, token, ops)
			if wantError != "" {
				if err == nil || !strings.Contains(err.Error(), wantError) {
					t.Fatalf("wrong refusal: %v", err)
				}
				if _, err := os.Stat(filepath.Join(root, "current.json")); !os.IsNotExist(err) || identities != wantIdentities || launches != wantLaunches {
					t.Fatal("rejected review advanced admission", err)
				}
				return
			}
			if err != nil || identities != 1 || launches != 1 {
				t.Fatalf("valid review not accepted: %v", err)
			}
			accepted, err := loadJob(root)
			if err != nil || accepted == nil || accepted.Review.Token != token || accepted.Phase != "accepted" {
				t.Fatal("accepted the wrong review", err)
			}
			if err := acceptUpdate(root, token, ops); err == nil || !strings.Contains(err.Error(), "already accepted") || launches != 1 {
				t.Fatal("replayed approval launched another job", err)
			}
		})
	}
}

// A plan may name networks and services added after this helper was built.
// The helper must still accept the job; the newer worker checks those names.
func TestHelperAcceptsReviewOfPlanWithLaterNames(t *testing.T) {
	j := workflowFixture()
	j.Review.Manifest.Networks = append(j.Review.Manifest.Networks, "testnet5")
	j.Review.Manifest.HostServices = []protocol.Component{"tor"}
	root, _ := workerFiles(t, j)
	p := prepared{Review: j.Review, WorkerHash: j.WorkerHash, PlanHash: j.PlanHash, ConfigHash: j.ConfigHash}
	token := saveReview(t, root, p)
	cfg := config.Default()
	cfg.Network = j.Review.Network
	err := acceptUpdate(root, token, admissionOps{
		observe: func() (nodeObservation, error) {
			return nodeObservation{source: j.Review.Source, config: cfg, configHash: j.ConfigHash}, nil
		},
		identify: func(*job, *config.AppConfig) error { return nil },
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
	if acceptUpdate(root, saveReview(t, root, p), admissionOps{
		observe: func() (nodeObservation, error) {
			return nodeObservation{source: j.Review.Source, config: cfg, configHash: j.ConfigHash}, nil
		},
		identify: func(*job, *config.AppConfig) error { return nil },
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
				wantError = "does not declare recovery"
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
			err = acceptUpdate(root, token, admissionOps{
				observe: func() (nodeObservation, error) {
					return nodeObservation{source: p.Review.Source, config: cfg, configHash: p.ConfigHash}, nil
				},
				identify: func(*job, *config.AppConfig) error {
					t.Fatal("recovery tried to replace pre-update wallet/device identity")
					return nil
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
