package update

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/virtpriv/node/internal/update/files"
	"github.com/virtpriv/node/internal/update/protocol"
)

// The worker runs only the plan of its own release, and no change to the
// machine it does not implement. This release implements none, so a plan that
// names one is refused before anything changes.
func TestWorkerRejectsPlanForDifferentBuild(t *testing.T) {
	m := workflowFixture().Review.Manifest
	if err := ValidateBuild(m, "0.7.1"); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*protocol.Manifest){
		"another version": func(m *protocol.Manifest) { m.Version = "0.7.2" },
		"a host step":     func(m *protocol.Manifest) { m.HostSteps = []string{"unsupported-host-change"} },
	} {
		t.Run(name, func(t *testing.T) {
			changed := m
			change(&changed)
			if err := changed.Validate(); err != nil {
				t.Fatal("fixture must pass general plan validation", err)
			}
			if ValidateBuild(changed, "0.7.1") == nil {
				t.Fatal("worker accepted a plan it cannot execute")
			}
		})
	}
}

// The installed side may understand less of a plan than the worker must.
// The worker therefore reads the approved plan bytes itself, in full.
func TestWorkerReadsApprovedPlanInFull(t *testing.T) {
	for _, tc := range []struct {
		name    string
		rewrite func(map[string]any)
		rehash  bool
		refused string
	}{
		{name: "approved plan"},
		{name: "plan changed after approval", rewrite: func(m map[string]any) { m["minimum_free_mib"] = 1024 }, refused: "integrity"},
		{name: "plan needs a newer worker", rewrite: func(m map[string]any) { m["snapshots"] = true }, rehash: true, refused: "unknown field"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := workflowFixture()
			root, _ := workerFiles(t, j)
			path := filepath.Join(j.dir(root), "update.json")
			if tc.rewrite != nil {
				b, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var m map[string]any
				if err := json.Unmarshal(b, &m); err != nil {
					t.Fatal(err)
				}
				tc.rewrite(m)
				if b, err = json.Marshal(m); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, b, 0600); err != nil {
					t.Fatal(err)
				}
				if tc.rehash {
					if j.PlanHash, err = files.Hash(path); err != nil {
						t.Fatal(err)
					}
				}
			}
			m, err := loadPlan(root, j)
			if tc.refused != "" {
				if err == nil || !strings.Contains(err.Error(), tc.refused) {
					t.Fatalf("wrong refusal: %v", err)
				}
				return
			}
			if err != nil || m.Version != j.Review.Manifest.Version {
				t.Fatal("approved plan was not read:", err)
			}
		})
	}
}

// Real files exercise retained-byte verification and atomic VPN publication.
func workerFiles(t *testing.T, j *job) (string, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(j.dir(root), 0700); err != nil {
		t.Fatal(err)
	}
	worker := filepath.Join(j.dir(root), "vpn")
	if err := os.WriteFile(worker, []byte("new VPN"), 0700); err != nil {
		t.Fatal(err)
	}
	j.WorkerHash, err = files.Hash(worker)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := json.Marshal(j.Review.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(j.dir(root), "update.json"), plan, 0600); err != nil {
		t.Fatal(err)
	}
	j.PlanHash, err = files.Hash(filepath.Join(j.dir(root), "update.json"))
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "installed-vpn")
	if err := os.WriteFile(binary, []byte("old VPN"), 0755); err != nil {
		t.Fatal(err)
	}
	return root, binary
}

// The program is replaced only by the bytes the owner approved, and never
// through a file another user could have changed.
func TestCommitRequiresVerifiedBytes(t *testing.T) {
	for _, failure := range []string{"", "worker", "destination"} {
		t.Run("failure="+failure, func(t *testing.T) {
			j := workflowFixture()
			root, binary := workerFiles(t, j)
			if failure == "worker" {
				if err := os.WriteFile(filepath.Join(j.dir(root), "vpn"), []byte("changed"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if failure == "destination" {
				if err := os.Chmod(binary, 0666); err != nil {
					t.Fatal(err)
				}
			}
			err := commitJob(root, binary, j)
			if (err != nil) != (failure != "") {
				t.Fatalf("wrong outcome: %v", err)
			}
			if failure == "worker" && !strings.Contains(err.Error(), "integrity") {
				t.Fatalf("wrong refusal: %v", err)
			}
			if failure == "destination" && !strings.Contains(err.Error(), "unsafe update path") {
				t.Fatalf("wrong refusal: %v", err)
			}
			want := "old VPN"
			if failure == "" {
				want = "new VPN"
			}
			if data, readErr := os.ReadFile(binary); readErr != nil || string(data) != want {
				t.Fatalf("wrong publication state: %q, err=%v", data, readErr)
			}
		})
	}
}

func TestSpaceNeedsAreCombinedPerFilesystem(t *testing.T) {
	const mib = 1 << 20
	needs := []spaceNeed{
		{path: "/staging", floor: 2048 * mib},
		{path: "/data", floor: 2048 * mib},
		{path: "/bin", floor: 1024 * mib, write: 200 * mib},
	}
	type fs struct{ dev, avail uint64 }
	for _, tc := range []struct {
		name    string
		mounts  map[string]fs
		refused string
	}{
		{"one disk short by one MiB", map[string]fs{"/staging": {1, 2247 * mib}, "/data": {1, 2247 * mib}, "/bin": {1, 2247 * mib}}, "/staging"},
		{"one disk exact", map[string]fs{"/staging": {1, 2248 * mib}, "/data": {1, 2248 * mib}, "/bin": {1, 2248 * mib}}, ""},
		{"separate disks exact", map[string]fs{"/staging": {1, 2048 * mib}, "/data": {2, 2048 * mib}, "/bin": {3, 1224 * mib}}, ""},
		{"only the install disk is short", map[string]fs{"/staging": {1, 9000 * mib}, "/data": {2, 9000 * mib}, "/bin": {3, 1223 * mib}}, "/bin"},
		{"unreadable filesystem", map[string]fs{"/staging": {1, 9000 * mib}, "/bin": {3, 9000 * mib}}, "/data"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkSpaceNeeds(needs, func(path string) (uint64, uint64, error) {
				m, ok := tc.mounts[path]
				if !ok {
					return 0, 0, errors.New("cannot read " + path)
				}
				return m.dev, m.avail, nil
			})
			if (err != nil) != (tc.refused != "") || (err != nil && !strings.Contains(err.Error(), tc.refused)) {
				t.Fatalf("wrong outcome: %v", err)
			}
		})
	}
}

// The update's first step refuses a node whose settings are not the ones the
// owner reviewed the update for, or cannot be read.
func TestNodeCheckRefusesChangedOrUnreadableSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"schema": 2}`), 0640); err != nil {
		t.Fatal(err)
	}
	j := workflowFixture()
	var err error
	if j.ConfigHash, err = files.Hash(path); err != nil {
		t.Fatal(err)
	}
	loads := func() error { return nil }
	if err := checkNode(j, loads, path); err != nil {
		t.Fatal("the reviewed settings were refused:", err)
	}
	unreadable := errors.New("config file: mode is 0600, want 0640")
	if err := checkNode(j, func() error { return unreadable }, path); !errors.Is(err, unreadable) {
		t.Fatalf("settings that cannot be read: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"schema": 2, "later": true}`), 0640); err != nil {
		t.Fatal(err)
	}
	if err := checkNode(j, loads, path); err == nil || !strings.Contains(err.Error(), "configuration changed") {
		t.Fatalf("settings changed since the review: %v", err)
	}
}
