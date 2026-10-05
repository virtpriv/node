package update

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/virtpriv/node/internal/helper"
	"github.com/virtpriv/node/internal/release"
)

func TestSelfUpdateStopsAtFailedBoundary(t *testing.T) {
	failure := errors.New("injected update failure")
	const version = "0.7.1"
	const archive = "vpn-0.7.1-amd64.tar.gz"
	for _, tc := range []struct {
		name      string
		failCall  int
		wantSteps []int
	}{
		{name: "archive download", failCall: 1},
		{name: "manifest download", failCall: 2},
		{name: "signature download", failCall: 3},
		{name: "signature verification", failCall: 4, wantSteps: []int{0}},
		{name: "checksum verification", failCall: 5, wantSteps: []int{0, 1}},
		{name: "binary installation", failCall: 6, wantSteps: []int{0, 1, 2}},
		{name: "complete", wantSteps: []int{0, 1, 2, 3}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workDir := t.TempDir()
			var calls []string
			boundary := func(name string) error {
				calls = append(calls, name)
				if len(calls) == tc.failCall {
					return failure
				}
				return nil
			}
			ops := selfUpdateOps{
				download: func(url, dest string) error {
					name := filepath.Base(dest)
					if filepath.Dir(dest) != workDir || url != "https://github.com/virtpriv/node/releases/download/v0.7.1/"+name {
						t.Fatalf("artifact escaped requested release/workspace: %s %s", url, dest)
					}
					return boundary(name)
				},
				verifySignature: func(dir string) error {
					if dir != workDir {
						t.Fatal("verified a different workspace")
					}
					return boundary("signature")
				},
				verifyChecksum: func(v, dir string) error {
					if v != version || dir != workDir {
						t.Fatal("verified a different artifact")
					}
					return boundary("checksum")
				},
				install: func(v, dir string) error {
					if v != version || dir != workDir {
						t.Fatal("installed a different artifact")
					}
					return boundary("install")
				},
			}
			var steps []int
			err := runSelfUpdate(version, archive, workDir, ops, func(i int) {
				// Progress is completion evidence, not admission to the next stage.
				wantCalls := []int{3, 4, 5, 6}
				if i < 0 || i >= len(wantCalls) || len(calls) != wantCalls[i] {
					t.Fatalf("premature progress %d after %v", i, calls)
				}
				steps = append(steps, i)
			})
			wantCalls := []string{archive, "SHA256SUMS", "SHA256SUMS.asc", "signature", "checksum", "install"}
			if tc.failCall != 0 {
				wantCalls = wantCalls[:tc.failCall]
			}
			if !slices.Equal(calls, wantCalls) || !slices.Equal(steps, tc.wantSteps) {
				t.Fatalf("calls=%v completed=%v", calls, steps)
			}
			if tc.failCall == 0 {
				if err != nil || len(steps) != len(helper.SelfUpdateStepNames(version)) {
					t.Fatalf("incomplete success: %v", err)
				}
			} else if !errors.Is(err, failure) {
				t.Fatalf("failure lost its cause: %v", err)
			}
		})
	}
}

func TestUnlistedArchiveCannotReachInstallation(t *testing.T) {
	workDir := t.TempDir()
	const archive = "vpn-0.7.1-amd64.tar.gz"
	data := []byte("unrelated file with a valid checksum")
	manifest := fmt.Sprintf("%x  release-key.asc\n", sha256.Sum256(data))
	for name, contents := range map[string][]byte{
		archive:           []byte("unverified archive"),
		"release-key.asc": data,
		"SHA256SUMS":      []byte(manifest),
	} {
		if err := os.WriteFile(filepath.Join(workDir, name), contents, 0600); err != nil {
			t.Fatal(err)
		}
	}
	installed := false
	var steps []int
	err := runSelfUpdate("0.7.1", archive, workDir, selfUpdateOps{
		download: func(string, string) error { return nil },
		// Signature mechanics have their own real-GPG tests. Here the manifest
		// is treated as authenticated to isolate the archive-binding regression.
		verifySignature: func(string) error { return nil },
		verifyChecksum:  release.VerifyChecksum,
		install:         func(string, string) error { installed = true; return nil },
	}, func(i int) { steps = append(steps, i) })
	if err == nil || !strings.Contains(err.Error(), "does not contain "+archive) || installed || !slices.Equal(steps, []int{0, 1}) {
		t.Fatalf("unverified archive advanced: error=%v installed=%v steps=%v", err, installed, steps)
	}
}
