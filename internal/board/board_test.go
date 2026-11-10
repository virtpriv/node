package board

import (
	"path/filepath"
	"strings"
	"testing"
)

// A missing board file must produce an error that tells the operator what to
// check: the reader never guesses, and the error carries the pointer.
func TestReadMissingIsNoisy(t *testing.T) {
	_, err := Read(filepath.Join(t.TempDir(), "no-such-fact"))
	if err == nil {
		t.Fatal("missing board file: expected error")
	}
	msg := err.Error()
	for _, want := range []string{
		"no-such-fact", "journalctl -t vpnd",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %q", msg, want)
		}
	}
}
