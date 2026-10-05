package update

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/virtpriv/node/internal/component"
	"github.com/virtpriv/node/internal/update/files"
	"github.com/virtpriv/node/internal/update/protocol"
)

func TestWorkerRejectsPlanForDifferentBuild(t *testing.T) {
	m := workflowFixture().Review.Manifest
	m.Bitcoin.Version, m.LND.Version, m.Syncthing.Version = component.BitcoinCoreVersion, component.LNDVersion, component.SyncthingVersion
	if err := ValidateBuild(m, "0.7.1"); err != nil {
		t.Fatal(err)
	}
	for _, mismatch := range []string{"vpn", "bitcoin", "lnd", "syncthing", "host step"} {
		t.Run(mismatch, func(t *testing.T) {
			changed := m
			switch mismatch {
			case "vpn":
				changed.Version = "0.7.2"
			case "bitcoin":
				changed.Bitcoin.Version = "999.0"
			case "lnd":
				changed.LND.Version = "999.0.0-beta"
			case "syncthing":
				changed.Syncthing.Version = "999.0.0"
			case "host step":
				changed.HostSteps = []string{"unsupported-host-change"}
			}
			if err := changed.Validate(); err != nil {
				t.Fatal("fixture must pass general plan validation", err)
			}
			if ValidateBuild(changed, "0.7.1") == nil {
				t.Fatal("worker accepted a plan it cannot execute")
			}
		})
	}
}

// The installed helper may understand less of a plan than the worker must.
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

func TestCommitRequiresLiveComponentsAndVerifiedBytes(t *testing.T) {
	for _, failure := range []string{"", "bitcoin", "lnd", "syncthing", "worker", "destination", "guards"} {
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
			injected := errors.New("injected boundary failure")
			var checked []protocol.Component
			released := false
			err := commitJob(root, binary, j, func(c protocol.Component) error {
				data, err := os.ReadFile(binary)
				if err != nil || string(data) != "old VPN" {
					t.Fatal("published before all process checks", err)
				}
				checked = append(checked, c)
				if string(c) == failure {
					return injected
				}
				return nil
			}, func(components []protocol.Component) error {
				if err := verifyHash(binary, j.WorkerHash); err != nil {
					t.Fatal("released guards before verified publication", err)
				}
				if !slices.Equal(checked, j.Affected) || !slices.Equal(components, j.Affected) {
					t.Fatal("omitted affected service")
				}
				released = true
				if failure == "guards" {
					return injected
				}
				return nil
			})
			if (err != nil) != (failure != "") {
				t.Fatalf("wrong outcome: %v", err)
			}
			if slices.Contains([]string{"bitcoin", "lnd", "syncthing", "guards"}, failure) && !errors.Is(err, injected) {
				t.Fatalf("lost boundary failure: %v", err)
			}
			if failure == "worker" && !strings.Contains(err.Error(), "integrity") {
				t.Fatalf("wrong refusal: %v", err)
			}
			if failure == "destination" && !strings.Contains(err.Error(), "unsafe update path") {
				t.Fatalf("wrong refusal: %v", err)
			}
			published := failure == "" || failure == "guards"
			want := "old VPN"
			if published {
				want = "new VPN"
			}
			data, readErr := os.ReadFile(binary)
			if readErr != nil || string(data) != want || released != published {
				t.Fatalf("wrong publication state: %q, guards=%v, err=%v", data, released, readErr)
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
