package host

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/virtpriv/node/internal/artifact"
	"github.com/virtpriv/node/internal/logger"
	"github.com/virtpriv/node/internal/system"
	"github.com/virtpriv/node/internal/update/files"
)

// syncthingSigner is the trusted Syncthing release signer.
// Source: https://syncthing.net/release-key.txt, linked from
// https://syncthing.net/security/ ("Release Signatures").
// Cross-checked June 9 2026 against an independent temporal
// channel: the apt keyring on a months-old production install
// (/etc/apt/keyrings/syncthing-archive-keyring.gpg) holds the
// identical primary fingerprint. Empirically bound: the v2.1.1
// sha256sum.txt.asc VALIDSIG primary fingerprint (last field)
// matches this pin on a live verification.
//
// NOTE: Syncthing dual-signs releases during key rotation: the
// v2.1.1 checksum file carries a second signature from the
// pre-rotation key (ends D26E6ED000654A3E), which our keyring
// cannot check (ERRSIG/NO_PUBKEY) and which makes gpg exit
// non-zero even on a genuine release. This is why exit-code
// trust is unusable here and VALIDSIG parsing is the sole
// source of truth (the v0.6.1 finding A/B design).
var syncthingSigner = struct {
	name        string
	fingerprint string
	keyURL      string
}{
	name:        "Syncthing Release Management",
	fingerprint: "FBA2E162F2F44657B38F0309E5665F9BD5970C47",
	keyURL:      "https://syncthing.net/release-key.txt",
}

// downloadSyncthing fetches the pinned release tarball and its
// clearsigned checksum file from GitHub over Tor.
func downloadSyncthing(version, workDir string) error {
	filename := syncthingArchiveName(version)
	url := fmt.Sprintf(
		"https://github.com/syncthing/syncthing/releases/download/v%s/%s",
		version, filename)
	ascURL := fmt.Sprintf(
		"https://github.com/syncthing/syncthing/releases/download/v%s/sha256sum.txt.asc",
		version)
	if err := system.DownloadRequireTor(
		url, filepath.Join(workDir, filename)); err != nil {
		return err
	}
	if err := system.DownloadRequireTor(ascURL,
		filepath.Join(workDir, "sha256sum.txt.asc")); err != nil {
		return fmt.Errorf("download Syncthing checksums: %w", err)
	}
	return nil
}

// extractAndInstallSyncthing unpacks the verified tarball and
// installs the binary to /usr/local/bin (LND pattern).
// Tarball layout (verified June 9 2026):
// syncthing-linux-amd64-v<ver>/syncthing
func extractAndInstallSyncthing(version, workDir string) error {
	filename := syncthingArchiveName(version)
	if err := system.Run("tar", "-xzf",
		filepath.Join(workDir, filename),
		"-C", workDir); err != nil {
		return err
	}
	src := filepath.Join(workDir,
		fmt.Sprintf("syncthing-linux-amd64-v%s", version),
		"syncthing")
	return system.RunRoot("install", "-m", "0755",
		"-o", "root", "-g", "root",
		src, "/usr/local/bin/")
}

// ── Syncthing verification ──────────────────────────────

func syncthingArchiveName(version string) string {
	return fmt.Sprintf("syncthing-linux-amd64-v%s.tar.gz", version)
}

// verifySyncthing checks the release that downloadSyncthing left in workDir.
func verifySyncthing(version, workDir string) error {
	keyFile := filepath.Join(workDir, "syncthing-release-key.txt")
	if err := system.DownloadRequireTor(
		syncthingSigner.keyURL, keyFile); err != nil {
		logger.Verify("FAIL: download Syncthing signing key: %v", err)
		return fmt.Errorf("download Syncthing signing key: %w", err)
	}
	return verifySyncthingFiles(version, workDir, keyFile, syncthingSigner.fingerprint)
}

// verifySyncthingFiles checks the signature on the checksum file and then the
// archive against the signed checksums. Syncthing ships the checksum list and
// its signature in one clearsigned file, unlike Bitcoin Core and LND. Only
// the text inside the signed block is trusted: the file itself can carry
// unsigned lines around the block.
func verifySyncthingFiles(version, workDir, keyFile, fingerprint string) error {
	signedText, err := verifySyncthingSig(workDir, keyFile, fingerprint)
	if err != nil {
		return err
	}
	return verifySyncthingChecksum(version, workDir, signedText)
}

// verifySyncthingSig returns the signed checksum text once the pinned release
// key is accepted for it.
func verifySyncthingSig(workDir, keyFile, fingerprint string) ([]byte, error) {
	logger.Verify("--- Syncthing signature verification ---")

	ascFile := filepath.Join(workDir, "sha256sum.txt.asc")
	if _, err := os.Stat(ascFile); err != nil {
		logger.Verify("FAIL: sha256sum.txt.asc not found")
		return nil, fmt.Errorf("sha256sum.txt.asc not found")
	}

	pinnedFPs := map[string]bool{fingerprint: true}

	// dataFile "" means clearsigned.
	result, err := artifact.VerifySignature(
		[]string{keyFile}, ascFile, "", pinnedFPs)
	if err != nil {
		logger.Verify("FAIL: Syncthing signature: %v", err)
		return nil, fmt.Errorf(
			"Syncthing signature verification failed: %w", err)
	}
	distinct, hasBadSig := result.Signers, result.Bad
	if !hasBadSig && distinct < 1 && len(result.Revoked) > 0 {
		logger.Verify("FAIL: Syncthing release signing key is revoked")
		return nil, fmt.Errorf("the syncthing release signing key has been revoked; a newer VPN release is needed")
	}

	if hasBadSig {
		logger.Verify("FAIL: bad Syncthing signature detected")
		return nil, fmt.Errorf(
			"bad Syncthing signature detected — verification aborted")
	}

	if distinct < 1 {
		logger.Verify(
			"FAIL: Syncthing signature not valid against pinned fingerprint")
		return nil, fmt.Errorf("Syncthing signature verification failed")
	}

	logger.Verify(
		"OK Syncthing: signature valid (release key, pinned fingerprint)")
	return result.SignedText, nil
}

// verifySyncthingChecksum compares the archive with the one checksum the
// signed text gives for it.
func verifySyncthingChecksum(version, workDir string, signedText []byte) error {
	logger.Verify("--- Syncthing checksum verification ---")
	name := syncthingArchiveName(version)
	want, err := artifact.SignedSHA256(signedText, name)
	if err != nil {
		logger.Verify("FAIL: Syncthing checksum: %v", err)
		return fmt.Errorf("checksum failed: %w", err)
	}
	got, err := files.Hash(filepath.Join(workDir, name))
	if err != nil {
		logger.Verify("FAIL: Syncthing checksum: %v", err)
		return fmt.Errorf("checksum failed: %w", err)
	}
	if got != want {
		logger.Verify("FAIL: Syncthing checksum: %s differs from the signed checksum", name)
		return fmt.Errorf("checksum failed: %s differs from the signed checksum", name)
	}
	logger.Verify("OK Syncthing checksum: %s", name)
	return nil
}
