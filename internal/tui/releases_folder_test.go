package tui

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/virtpriv/node/internal/release"
	"github.com/virtpriv/node/internal/update/protocol"
)

// The TUI removes the release files the owner brought only once they were
// used: after a complete update, the files of this program's version and of
// older ones, by their exact names. It keeps newer versions, which a coming
// update may need, and every other file.
func TestUsedReleaseFilesLeaveTheReleasesFolder(t *testing.T) {
	const running = "0.7.0"
	files := func(version string) []string {
		t.Helper()
		assets, err := release.AssetsFor(version)
		if err != nil {
			t.Fatal(err)
		}
		return []string{assets.Archive, assets.Manifest, assets.Signature}
	}
	used := append(files(running), files("0.6.9")...)
	kept := append(files("0.7.1"),
		// Not exact release file names: a partial copy, program archives and
		// anything else the owner keeps there.
		used[0]+".part",
		"bitcoin-31.2-x86_64-linux-gnu.tar.gz",
		"lnd-linux-amd64-v0.21.8-beta.tar.gz",
		"syncthing-linux-amd64-v2.1.5.tar.gz",
		"SHA256SUMS",
		"ripsline.asc",
		"notes.txt",
	)

	for _, tc := range []struct {
		name    string
		status  protocol.Status
		removed []string
	}{
		{name: "no update finished", status: protocol.Status{}},
		{name: "an update still running", status: protocol.Status{Phase: "running", Version: running}},
		{name: "an update failed", status: protocol.Status{Phase: "failed", Version: running}},
		{name: "a complete update to a newer version than this program", status: protocol.Status{Phase: "complete", Version: "0.7.1"}},
		{name: "a complete update", status: protocol.Status{Phase: "complete", Version: running}, removed: used},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			all := append(slices.Clone(kept), used...)
			for _, name := range all {
				if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			// Only files go, never a folder that carries a release file's name.
			folderNamed := filepath.Join(dir, files("0.6.8")[0])
			if err := os.Mkdir(folderNamed, 0o700); err != nil {
				t.Fatal(err)
			}
			clearUsedReleaseFiles(dir, &nodeUpdateClientStub{status: tc.status}, running)
			if _, err := os.Lstat(folderNamed); err != nil {
				t.Errorf("a folder named as a release file was removed: %v", err)
			}
			for _, name := range all {
				_, err := os.Lstat(filepath.Join(dir, name))
				gone := os.IsNotExist(err)
				if want := slices.Contains(tc.removed, name); gone != want {
					t.Errorf("%s: removed %v, want %v", name, gone, want)
				}
			}
		})
	}
}
