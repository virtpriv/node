package system

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Without torsocks a download must fail before any fetch program starts, so
// nothing can leave the node outside Tor.
func TestDownloadWithoutTorsocksStartsNoFetch(t *testing.T) {
	bin := t.TempDir()
	mark := filepath.Join(t.TempDir(), "fetched")
	// A shell builtin writes the mark, since PATH holds only the stand-ins.
	stub := []byte("#!/bin/sh\n: > '" + mark + "'\n")
	for _, name := range []string{"wget", "curl"} {
		if err := os.WriteFile(filepath.Join(bin, name), stub, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The stand-ins must be able to leave the mark, or the check below proves
	// nothing.
	if err := exec.Command(filepath.Join(bin, "wget")).Run(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(mark); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	dest := filepath.Join(t.TempDir(), "file")
	if err := DownloadRequireTor("https://example.invalid/file", dest); err == nil {
		t.Fatal("download without torsocks succeeded")
	}
	if _, err := os.Stat(mark); !os.IsNotExist(err) {
		t.Fatalf("a fetch program ran without torsocks (stat: %v)", err)
	}
}
