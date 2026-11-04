package host

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A config file that names an onion address is written only from a hostname
// Tor actually made. A missing or malformed hostname fails, so no file is
// written without the address or with a wrong one.
func TestReadRequiredOnion(t *testing.T) {
	dir := t.TempDir()
	valid := strings.Repeat("a", 56) + ".onion"
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	got, err := readRequiredOnion(write("valid", valid+"\n"), "Bitcoin Core")
	if err != nil || got != valid {
		t.Fatalf("Tor's hostname file gave %q, %v, want %q", got, err, valid)
	}
	if _, err := readRequiredOnion(filepath.Join(dir, "missing"), "Bitcoin Core"); err == nil {
		t.Error("missing hostname file accepted")
	}
	for i, malformed := range []string{
		"", "short.onion", strings.Repeat("A", 56) + ".onion",
		strings.Repeat("0", 56) + ".onion", strings.Repeat("a", 56),
	} {
		path := write("malformed"+string(rune('0'+i)), malformed+"\n")
		if _, err := readRequiredOnion(path, "Bitcoin Core"); err == nil {
			t.Errorf("malformed hostname accepted: %q", malformed)
		}
	}
}
