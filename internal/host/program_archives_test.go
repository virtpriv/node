package host

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/virtpriv/node/internal/component"
	"github.com/virtpriv/node/internal/update/protocol"
)

// An update's Retry stages again into the folder of the staging that was
// refused, which still holds the archive it took. Taking the owner's
// replaced file must then work, and give the new bytes.
func TestTakingAnArchiveAgainReplacesTheEarlierCopy(t *testing.T) {
	folder, work := t.TempDir(), t.TempDir()
	name := component.ArchiveName(protocol.LND, component.LNDVersion)
	for _, content := range []string{"the wrong file", "the owner's replacement"} {
		if err := os.WriteFile(filepath.Join(folder, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		from, err := TakeArchive(protocol.LND, component.LNDVersion, folder, work)
		if err != nil || from != folder {
			t.Fatalf("take %q: from %q, %v", content, from, err)
		}
		got, err := os.ReadFile(filepath.Join(work, name))
		if err != nil || string(got) != content {
			t.Fatalf("work copy is %q, %v, want %q", got, err, content)
		}
	}
}
