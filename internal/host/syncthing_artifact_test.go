package host

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/virtpriv/node/internal/artifact/gpgtest"
)

// Syncthing publishes one file that holds its checksum list and the signature
// over it. Only text inside the signed block may decide which archive is
// installed. These cases use the real gpg program with throwaway keys.
func TestSyncthingArchiveIsCheckedOnlyAgainstSignedChecksums(t *testing.T) {
	const (
		version     = "9.9.9"
		archiveName = "syncthing-linux-amd64-v9.9.9.tar.gz"
	)
	archive := []byte("the archive that was downloaded")
	sum := sha256.Sum256(archive)
	archiveLine := hex.EncodeToString(sum[:]) + "  " + archiveName + "\n"
	otherHash := strings.Repeat("0", 64)

	h := gpgtest.NewHome(t)
	release := h.Key("release", "never", "")
	// Syncthing also signs with an older key that the node does not have.
	retired := h.Key("retired", "never", "")
	stranger := h.Key("stranger", "never", "")
	keyFile := h.Export("release", "release-key.asc")

	n := 0
	signed := func(list string, signers ...string) string {
		t.Helper()
		n++
		name := "list-" + string(rune('a'+n))
		b, err := os.ReadFile(h.ClearsignBy(h.File(name+".txt", list), name+".asc", signers...))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	thisRelease := otherHash + "  syncthing-linux-386-v9.9.9.tar.gz\n" + archiveLine
	olderRelease := signed(otherHash+"  syncthing-linux-amd64-v9.9.8.tar.gz\n", release)
	genuine := signed(thisRelease, release, retired)

	cases := []struct {
		name     string
		checksum string
		archive  []byte
		accepted bool
	}{
		{"genuine list with a second signature from a key the node lacks",
			strings.TrimRight(genuine, "\n"), archive, true},
		{"unsigned line before an older signed list",
			archiveLine + olderRelease, archive, false},
		{"unsigned line after an older signed list",
			olderRelease + archiveLine, archive, false},
		{"second signed block from an unknown key",
			olderRelease + signed(archiveLine, stranger), archive, false},
		{"signed list does not name the archive",
			olderRelease, archive, false},
		{"signed list names the archive twice",
			signed(thisRelease+otherHash+"  "+archiveName+"\n", release), archive, false},
		{"archive differs from the signed checksum",
			genuine, []byte("another archive"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "sha256sum.txt.asc"), []byte(c.checksum), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, archiveName), c.archive, 0600); err != nil {
				t.Fatal(err)
			}
			err := verifySyncthingFiles(version, dir, keyFile, release)
			if c.accepted && err != nil {
				t.Fatalf("genuine release refused: %v", err)
			}
			if !c.accepted && err == nil {
				t.Fatal("accepted")
			}
		})
	}
}
