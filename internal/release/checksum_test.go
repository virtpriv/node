package release

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChecksumRequiresExactArchive(t *testing.T) {
	const archive = "vpn-0.7.1-x86_64.tar.gz"
	contents := []byte("requested release archive")
	digest := fmt.Sprintf("%x", sha256.Sum256(contents))
	entry := digest + "  " + archive + "\n"
	for _, tc := range []struct {
		name, manifest  string
		missingArchive  bool
		missingManifest bool
		wantErr         string
	}{
		{name: "text marker", manifest: entry},
		{name: "binary marker", manifest: digest + " *" + archive + "\n"},
		{name: "other architectures may be absent", manifest: entry + digest + "  vpn-0.7.1-aarch64.tar.gz\n"},
		{name: "another present file is insufficient", manifest: digest + "  release-key.asc\n", wantErr: "does not contain"},
		{name: "another version is insufficient", manifest: digest + "  vpn-0.7.0-x86_64.tar.gz\n", wantErr: "does not contain"},
		{name: "empty manifest", wantErr: "does not contain"},
		{name: "wrong digest", manifest: strings.Repeat("0", 64) + "  " + archive + "\n", wantErr: "checksum mismatch"},
		{name: "invalid hex", manifest: strings.Repeat("z", 64) + "  " + archive + "\n", wantErr: "malformed checksum digest"},
		{name: "truncated entry", manifest: digest + "\n", wantErr: "malformed checksum manifest"},
		{name: "duplicate entry", manifest: entry + entry, wantErr: "duplicate checksum entry"},
		{name: "missing archive", manifest: entry, missingArchive: true, wantErr: "open update archive"},
		{name: "missing manifest", missingManifest: true, wantErr: "open checksum manifest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			write := func(name string, data []byte) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if !tc.missingArchive {
				write(archive, contents)
			}
			// The old whole-manifest check succeeds for this unrelated file.
			write("release-key.asc", contents)
			if !tc.missingManifest {
				write("vpn-0.7.1-manifest.txt", []byte(tc.manifest))
			}
			err := VerifyChecksum("0.7.1", dir)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}
