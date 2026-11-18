package update

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/virtpriv/node/internal/update/protocol"
)

// The record keeps, across a save and a load, that the program may have been
// replaced. A record of another schema is refused, never read as an empty job.
func TestDurableRecordKeepsThatChangesBegan(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	j := workflowFixture()
	j.Review.Manifest.Version = "0.7.1-rc.1"
	j.Phase, j.HostChanges = "committing", true
	if err := saveJob(root, j); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadJob(root)
	if err != nil {
		t.Fatal("read saved update record:", err)
	}
	if loaded == nil || loaded.Review.Manifest.Version != "0.7.1-rc.1" || !loaded.HostChanges || loaded.Phase != "committing" {
		t.Fatal("lost that changes began")
	}
	// Change only the schema so another missing field cannot mask the refusal.
	for _, schema := range []int{1, 99} {
		j.Schema = schema
		if err := saveJob(root, j); err != nil {
			t.Fatal(err)
		}
		if _, err := loadJob(root); err == nil {
			t.Fatalf("a record of schema %d became a job", schema)
		}
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
	// Each token has its own record. Changing the data under an old token must
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
		"source":        func(p *prepared) { p.Review.Source.VPN = "0.6.9" },
		"archive":       func(p *prepared) { p.Review.Digest = strings.Repeat("b", 64) },
		"worker":        func(p *prepared) { p.WorkerHash = strings.Repeat("b", 64) },
		"plan":          func(p *prepared) { p.PlanHash = strings.Repeat("b", 64) },
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

// The installed side and launcher stay in service for every later release
// that admits them. A later worker may add fields, phases and programs.
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
				// A later worker may shape the screen's list differently.
				m["steps"] = map[string]any{"changed": true}
				// And save a failure code and the programs it changes.
				m["failure"] = map[string]any{"code": "snapshot_failed", "service": "tor", "detail": 1}
				m["affected"] = []any{"tor"}
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
				t.Fatal("a later record locked the installed side out:", err)
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
			if (m["phase"] == "canceled") == tc.begun {
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
		"later schema":   func(m map[string]any) { m["schema"] = 3 },
		"earlier schema": func(m map[string]any) { m["schema"] = 1 },
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
	if err := os.WriteFile(filepath.Join(root, "current.json"), []byte(`{"schema":2,"phase":"sta`), 0600); err != nil {
		t.Fatal(err)
	}
	if r, err := loadRecord(root); err == nil {
		t.Fatal("truncated record was accepted", r)
	}
}

// The installed side passes the screen's newer fields only in a shape it
// can read, and the next step and advice only with the error they explain.
// A later worker may add next values, which pass by shape so the screen can
// ignore them. Anything else is left out and status stays readable.
func TestStatusPassesOnlyReadableStepTimesAndAdvice(t *testing.T) {
	now := time.Now().UTC()
	at := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339Nano) }
	advice := "LND was busy.\n\nSelect Continue."
	for _, tc := range []struct {
		name   string
		set    map[string]any
		err    string
		times  int
		ends   int
		next   protocol.Next
		advice string
	}{
		{"all readable", map[string]any{"step_starts": []any{at(-time.Minute), at(0)}, "step_ends": []any{at(0)}, "next": "continue", "advice": advice}, "x", 2, 1, protocol.NextContinue, advice},
		{"next and advice without an error", map[string]any{"step_starts": []any{at(0)}, "next": "continue", "advice": advice}, "", 1, 0, "", ""},
		{"starts without ends", map[string]any{"step_starts": []any{at(-time.Minute), at(0)}}, "x", 2, 0, "", ""},
		{"an end before its start", map[string]any{"step_starts": []any{at(0)}, "step_ends": []any{at(-time.Minute)}}, "x", 1, 0, "", ""},
		{"an end without its start", map[string]any{"step_starts": []any{at(0)}, "step_ends": []any{at(0), at(0)}}, "x", 1, 0, "", ""},
		{"an end in the future", map[string]any{"step_starts": []any{at(0)}, "step_ends": []any{at(time.Hour)}}, "x", 1, 0, "", ""},
		{"more ends than steps", map[string]any{"step_starts": []any{at(0)}, "step_ends": []any{at(0), at(0), at(0), at(0)}}, "x", 1, 0, "", ""},
		{"ends of another shape", map[string]any{"step_starts": []any{at(0)}, "step_ends": "later"}, "x", 1, 0, "", ""},
		{"a later next value", map[string]any{"next": "wait_for_owner"}, "x", 0, 0, "wait_for_owner", ""},
		{"next in capitals", map[string]any{"next": "Continue"}, "x", 0, 0, "", ""},
		{"next of another shape", map[string]any{"next": 1}, "x", 0, 0, "", ""},
		{"advice that clears the screen", map[string]any{"advice": "a\x1b[2Jb"}, "x", 0, 0, "", ""},
		{"advice too long", map[string]any{"advice": strings.Repeat("a", 401)}, "x", 0, 0, "", ""},
		{"a start in the future", map[string]any{"step_starts": []any{at(time.Hour)}}, "x", 0, 0, "", ""},
		{"more starts than steps", map[string]any{"step_starts": []any{at(0), at(0), at(0), at(0)}}, "x", 0, 0, "", ""},
		{"starts of another shape", map[string]any{"step_starts": "yesterday"}, "x", 0, 0, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			j := workflowFixture()
			j.Phase, j.Error = "failed", tc.err
			j.Steps, j.StepNumber = []string{"Check the node", "Check free space", "Finish VPN update"}, 2
			if err := saveJob(root, j); err != nil {
				t.Fatal(err)
			}
			rewriteRecord(t, root, func(m map[string]any) { maps.Copy(m, tc.set) })
			r, err := loadRecord(root)
			if err != nil || r == nil {
				t.Fatal("record unreadable:", err)
			}
			s := r.status()
			if len(s.StepStarts) != tc.times || len(s.StepEnds) != tc.ends || s.Next != tc.next || s.Advice != tc.advice {
				t.Fatalf("status passed %d starts, %d ends, next %q, advice %q", len(s.StepStarts), len(s.StepEnds), s.Next, s.Advice)
			}
			if len(s.Steps) != 3 || s.Error != tc.err || s.Phase != "failed" {
				t.Fatal("the new fields changed the rest of the status", s)
			}
		})
	}
}

// The screen scrolls, so a later worker may list as many steps as it needs.
// A list longer than any update of this release has still reaches it.
func TestStatusPassesALongStepList(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	j := workflowFixture()
	j.Phase = "committing"
	for i := range 20 {
		j.Steps = append(j.Steps, fmt.Sprintf("Step %d", i+1))
	}
	j.StepNumber = 15
	if err := saveJob(root, j); err != nil {
		t.Fatal(err)
	}
	r, err := loadRecord(root)
	if err != nil || r == nil {
		t.Fatal("record unreadable:", err)
	}
	if s := r.status(); len(s.Steps) != 20 || s.StepNumber != 15 {
		t.Fatalf("status passed %d steps at step %d, want 20 at 15", len(s.Steps), s.StepNumber)
	}
}
