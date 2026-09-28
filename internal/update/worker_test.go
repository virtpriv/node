package update

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/virtualprivatenode/vpn/internal/component"
	"github.com/virtualprivatenode/vpn/internal/update/files"
	"github.com/virtualprivatenode/vpn/internal/update/protocol"
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
