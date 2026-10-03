package update

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDurableRecordPreservesMigrationBoundary(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	j := workflowFixture()
	j.Review.Manifest.Version = "0.7.1-rc.1"
	j.Started["lnd"] = true
	j.MayHaveRun["lnd"] = true
	j.Phase = "starting"
	if err := saveJob(root, j); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadJob(root)
	if err != nil {
		t.Fatal("read saved update record:", err)
	}
	if loaded == nil || loaded.Review.Manifest.Version != "0.7.1-rc.1" || !loaded.MayHaveRun["lnd"] || !loaded.Started["lnd"] {
		t.Fatal("lost possible database migration")
	}
	// Change only the schema so another missing field cannot mask the refusal.
	j.Schema = 99
	if err := saveJob(root, j); err != nil {
		t.Fatal(err)
	}
	if _, err := loadJob(root); err == nil {
		t.Fatal("unknown record became an empty job")
	}
}

func TestApprovalBindsSourceConfigurationAndRelease(t *testing.T) {
	j := workflowFixture()
	p := prepared{Review: j.Review, WorkerHash: j.WorkerHash, PlanHash: j.PlanHash, ConfigHash: j.ConfigHash}
	first, err := approvalToken(p)
	if err != nil {
		t.Fatal(err)
	}
	p.Review.Token = first
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Each token has its own record; changing the data under an old token must
	// be rejected, even when two terminals reviewed the same release archive.
	if err := os.Mkdir(filepath.Join(root, "reviews"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "reviews", first+".json")
	if err := writeJSON(path, p); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrepared(root, first); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*prepared){
		"configuration": func(p *prepared) { p.ConfigHash = strings.Repeat("b", 64) },
		"source":        func(p *prepared) { p.Review.Source.LND = "0.21.0-beta" },
		"archive":       func(p *prepared) { p.Review.Digest = strings.Repeat("b", 64) },
		"worker":        func(p *prepared) { p.WorkerHash = strings.Repeat("b", 64) },
		"plan":          func(p *prepared) { p.PlanHash = strings.Repeat("b", 64) },
		"network":       func(p *prepared) { p.Review.Network = "mainnet" },
		"recovery":      func(p *prepared) { p.Previous = strings.Repeat("b", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			changed := p
			change(&changed)
			if err := writeJSON(path, changed); err != nil {
				t.Fatal(err)
			}
			if _, err := readPrepared(root, first); err == nil {
				t.Fatal("accepted changed review under old token")
			}
		})
	}
}

// rewriteRecord edits the saved record as raw JSON, the way code other than
// this build would leave it.
func rewriteRecord(t *testing.T, root string, change func(map[string]any)) {
	t.Helper()
	path := filepath.Join(root, "current.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	change(m)
	if b, err = json.Marshal(m); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}

func rawRecord(t *testing.T, root string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "current.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// An installed helper and launcher stay in service for every later release
// that admits them. A later worker may add fields, phases and components.
func TestInstalledSideHandlesLaterWorkerRecord(t *testing.T) {
	for _, tc := range []struct {
		name, phase string
		begun       bool
	}{
		{"running before host changes", "verifying-snapshot", false},
		{"running after host changes", "verifying-snapshot", true},
		{"failed before host changes", "failed", false},
		{"failed after host changes", "failed", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			j := workflowFixture()
			j.HostChanges = tc.begun
			j.Error = "earlier failure"
			if err := saveJob(root, j); err != nil {
				t.Fatal(err)
			}
			rewriteRecord(t, root, func(m map[string]any) {
				m["phase"] = tc.phase
				m["snapshot"] = map[string]any{"taken": true}
				m["affected"] = append(m["affected"].([]any), "tor")
				m["review"].(map[string]any)["manifest"].(map[string]any)["tor"] = "0.5.0"
			})
			kept := func(when string) map[string]any {
				t.Helper()
				m := rawRecord(t, root)
				affected, _ := m["affected"].([]any)
				snapshot, _ := m["snapshot"].(map[string]any)
				if snapshot["taken"] != true || len(affected) == 0 || affected[len(affected)-1] != "tor" || m["review"].(map[string]any)["manifest"].(map[string]any)["tor"] != "0.5.0" {
					t.Fatalf("%s erased the later worker's fields: %v", when, m)
				}
				return m
			}
			r, err := loadRecord(root)
			if err != nil || r == nil {
				t.Fatal("a later record locked the helper out:", err)
			}
			if !r.active() {
				t.Fatal("an unknown phase lifted the maintenance restriction")
			}
			if r.relaunch() != (tc.phase != "failed") {
				t.Fatal("launcher misread whether the worker should continue")
			}
			if err := retryUpdate(root, j.Review.Digest); err != nil {
				t.Fatal(err)
			}
			if m := kept("Retry"); m["phase"] != "retry" || m["error"] != nil {
				t.Fatal("Retry did not hand the job back to the worker", m["phase"], m["error"])
			}
			err = cancelUpdate(root, j.Review.Digest)
			if (err == nil) == tc.begun {
				t.Fatal("wrong cancellation boundary:", err)
			}
			m := kept("Cancel")
			if (m["phase"] == "cancelled") == tc.begun {
				t.Fatal("cancellation outcome was not recorded as decided", m["phase"])
			}
			if r, err = loadRecord(root); err != nil || r.active() == !tc.begun {
				t.Fatal("maintenance restriction does not match the cancellation", err)
			}
		})
	}
}

// Ignoring unknown fields must not turn a damaged record into "no update".
func TestInstalledSideRefusesDamagedRecord(t *testing.T) {
	for name, damage := range map[string]func(map[string]any){
		"unknown schema": func(m map[string]any) { m["schema"] = 2 },
		"no release":     func(m map[string]any) { delete(m, "review") },
	} {
		t.Run(name, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := saveJob(root, workflowFixture()); err != nil {
				t.Fatal(err)
			}
			rewriteRecord(t, root, damage)
			if r, err := loadRecord(root); err == nil {
				t.Fatal("damaged record was accepted", r)
			}
		})
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "current.json"), []byte(`{"schema":1,"phase":"sta`), 0600); err != nil {
		t.Fatal(err)
	}
	if r, err := loadRecord(root); err == nil {
		t.Fatal("truncated record was accepted", r)
	}
}
