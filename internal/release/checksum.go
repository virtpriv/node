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

// Assets names the three files published with a release. Every installed node
// downloads exactly these names, so they never change.
type Assets struct {
	// Archive holds vpn and, for a managed release, update.json.
	Archive string
	// Manifest lists the archive's SHA-256 in the sha256sum format.
	Manifest string
	// Signature is the release key's detached signature of the manifest.
	Signature string
}

// AssetsFor returns the file names of a release version.
func AssetsFor(version string) (Assets, error) {
	if !ValidVersion(version) {
		return Assets{}, fmt.Errorf("%q is not a valid release version", version)
	}
	manifest := "vpn-" + version + "-manifest.txt"
	return Assets{Archive: "vpn-" + version + "-x86_64.tar.gz", Manifest: manifest, Signature: manifest + ".asc"}, nil
}

// VerifyChecksum requires exactly one entry for the requested archive in the
// already authenticated manifest. Checking other files cannot authorize it.
func VerifyChecksum(version, workDir string) error {
	assets, err := AssetsFor(version)
	if err != nil {
		return err
	}
	archive := assets.Archive
	manifest, err := os.Open(filepath.Join(workDir, assets.Manifest))
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
		// The sha256sum format is 64 hex digits, a space, a text or binary
		// marker, then the exact file name. VPN file names need no escaping.
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
