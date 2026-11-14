package tui

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/virtpriv/node/internal/component"
	"github.com/virtpriv/node/internal/update/protocol"
)

// The TUI removes the Bitcoin Core, LND and Syncthing archives the owner
// brought only once they were used: after a complete update for Core and
// LND, once Syncthing is enabled for Syncthing's. A node keeps its last
// update's "complete" until the next one, so a Syncthing archive brought for
// the add-on must survive it. It removes the versions this vpn pins and
// older ones, by their exact names, and keeps newer versions, which a coming
// update may need, and every other file.
func TestUsedProgramFilesLeaveTheReleasesFolder(t *testing.T) {
	const running = "0.7.0"
	core := component.ArchiveName(protocol.Bitcoin, component.BitcoinCoreVersion)
	lnd := component.ArchiveName(protocol.LND, component.LNDVersion)
	syncthing := component.ArchiveName(protocol.Syncthing, component.SyncthingVersion)
	olderCore := "bitcoin-29.4-x86_64-linux-gnu.tar.gz"
	olderLND := "lnd-linux-amd64-v0.21.4-beta.tar.gz"
	olderSyncthing := "syncthing-linux-amd64-v2.1.1.tar.gz"
	kept := []string{
		"bitcoin-99.0-x86_64-linux-gnu.tar.gz",
		"lnd-linux-amd64-v9.0.0-beta.tar.gz",
		"syncthing-linux-amd64-v9.0.0.tar.gz",
		// Not exact archive names: an LND release candidate, a partial copy,
		// upstream's lists and anything else the owner keeps there.
		"lnd-linux-amd64-v0.21.5-beta.rc1.tar.gz",
		core + ".part",
		"SHA256SUMS",
		"manifest-v" + component.LNDVersion + ".txt",
		"ripsline.asc",
		"notes.txt",
	}
	programs := []string{core, lnd, syncthing, olderCore, olderLND, olderSyncthing}
	coreAndLND := []string{core, lnd, olderCore, olderLND}

	complete := protocol.Status{Phase: "complete", Version: running}
	for _, tc := range []struct {
		name      string
		status    protocol.Status
		syncthing bool
		removed   []string
	}{
		{name: "no update finished, Syncthing off", status: protocol.Status{}},
		{name: "an update still running", status: protocol.Status{Phase: "running", Version: running}},
		{name: "an update failed", status: protocol.Status{Phase: "failed", Version: running}},
		{name: "a complete update, Syncthing off", status: complete, removed: coreAndLND},
		{name: "a complete update, Syncthing enabled", status: complete, syncthing: true, removed: programs},
		{name: "Syncthing enabled, no update", status: protocol.Status{}, syncthing: true, removed: []string{syncthing, olderSyncthing}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			all := append(slices.Clone(kept), programs...)
			for _, name := range all {
				if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			// Only files go, never a folder that carries an archive's name.
			folderNamed := filepath.Join(dir, "lnd-linux-amd64-v0.21.3-beta.tar.gz")
			if err := os.Mkdir(folderNamed, 0o700); err != nil {
				t.Fatal(err)
			}
			clearUsedReleaseFiles(dir, &nodeUpdateClientStub{status: tc.status}, running, tc.syncthing)
			if _, err := os.Lstat(folderNamed); err != nil {
				t.Errorf("a folder named as an archive was removed: %v", err)
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
