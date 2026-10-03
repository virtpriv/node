package release

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ArchiveName identifies the fixed amd64 VPN artifact for a release version.
func ArchiveName(version string) (string, error) {
	if !ValidVersion(version) {
		return "", fmt.Errorf("%q is not a valid release version", version)
	}
	return "vpn-" + version + "-amd64.tar.gz", nil
}

// VerifyChecksum requires exactly one entry for the requested archive in the
// already authenticated SHA256SUMS. Checking other files cannot authorize it.
func VerifyChecksum(version, workDir string) error {
	archive, err := ArchiveName(version)
	if err != nil {
		return err
	}
	manifest, err := os.Open(filepath.Join(workDir, "SHA256SUMS"))
	if err != nil {
		return fmt.Errorf("open checksum manifest: %w", err)
	}
	defer manifest.Close()

	var expected []byte
	scanner := bufio.NewScanner(manifest)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		// GNU SHA256SUMS uses 64 hex digits, a space, a text/binary
		// marker, then the exact filename. VPN artifact names need no escaping.
		if len(line) < 67 || line[64] != ' ' || (line[65] != ' ' && line[65] != '*') {
			return fmt.Errorf("malformed checksum manifest entry")
		}
		digest, err := hex.DecodeString(line[:64])
		if err != nil {
			return fmt.Errorf("malformed checksum digest: %w", err)
		}
		if line[66:] != archive {
			continue
		}
		if expected != nil {
			return fmt.Errorf("duplicate checksum entry for %s", archive)
		}
		expected = digest
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read checksum manifest: %w", err)
	}
	if expected == nil {
		return fmt.Errorf("checksum manifest does not contain %s", archive)
	}

	file, err := os.Open(filepath.Join(workDir, archive))
	if err != nil {
		return fmt.Errorf("open update archive: %w", err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return fmt.Errorf("read update archive: %w", err)
	}
	if !bytes.Equal(hash.Sum(nil), expected) {
		return fmt.Errorf("checksum mismatch for %s", archive)
	}
	return nil
}
