package host

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/virtpriv/node/internal/artifact"
	"github.com/virtpriv/node/internal/logger"
	"github.com/virtpriv/node/internal/system"
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
	filename := fmt.Sprintf(
		"syncthing-linux-amd64-v%s.tar.gz", version)
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
	filename := fmt.Sprintf(
		"syncthing-linux-amd64-v%s.tar.gz", version)
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

// verifySyncthingSig verifies the CLEARSIGNED checksum file
// (sha256sum.txt.asc) against the pinned release fingerprint.
// Unlike Bitcoin Core and LND (detached signatures: signature
// and data in separate files), Syncthing ships the checksum
// list and its signature in ONE file. Must run BEFORE
// verifySyncthingChecksum: the checksums inside the file are
// untrusted until the signature over them validates.
func verifySyncthingSig(workDir string) error {
	logger.Verify("--- Syncthing signature verification ---")

	ascFile := filepath.Join(workDir, "sha256sum.txt.asc")
	if _, err := os.Stat(ascFile); err != nil {
		logger.Verify("FAIL: sha256sum.txt.asc not found")
		return fmt.Errorf("sha256sum.txt.asc not found")
	}

	keyFile := filepath.Join(workDir, "syncthing-release-key.txt")
	if err := system.DownloadRequireTor(
		syncthingSigner.keyURL, keyFile); err != nil {
		logger.Verify("FAIL: download Syncthing signing key: %v", err)
		return fmt.Errorf("download Syncthing signing key: %w", err)
	}

	pinnedFPs := map[string]bool{syncthingSigner.fingerprint: true}

	// dataFile "" → clearsigned, single-argument verify.
	result, err := artifact.VerifySignature(
		[]string{keyFile}, ascFile, "", pinnedFPs)
	if err != nil {
		return fmt.Errorf(
			"Syncthing signature verification failed: %w", err)
	}
	distinct, hasBadSig := result.Signers, result.Bad
	if !hasBadSig && distinct < 1 && len(result.Revoked) > 0 {
		logger.Verify("FAIL: Syncthing release signing key is revoked")
		return fmt.Errorf("the syncthing release signing key has been revoked; a newer VPN release is needed")
	}

	if hasBadSig {
		logger.Verify("FAIL: bad Syncthing signature detected")
		return fmt.Errorf(
			"bad Syncthing signature detected — verification aborted")
	}

	if distinct < 1 {
		logger.Verify(
			"FAIL: Syncthing signature not valid against pinned fingerprint")
		return fmt.Errorf("Syncthing signature verification failed")
	}

	logger.Verify(
		"OK Syncthing: signature valid (release key, pinned fingerprint)")
	return nil
}

// verifySyncthingChecksum checks the tarball against the
// now-trusted clearsigned checksum file. Same sha256sum
// pattern as verifyBitcoinChecksum/verifyLNDChecksum: the only difference is
// that the checksum source is the clearsigned .asc itself:
// sha256sum skips the PGP armor lines (reported as "improperly
// formatted" warnings) and matches the real checksum lines.
func verifySyncthingChecksum(workDir string) error {
	logger.Verify("--- Syncthing checksum verification ---")
	// exec.Command used directly because sha256sum --check needs
	// working directory set to where the tarball was downloaded.
	cmd := exec.Command("sha256sum",
		"--ignore-missing", "--check", "sha256sum.txt.asc")
	cmd.Dir = workDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Verify("FAIL: Syncthing checksum: %s", string(output))
		return fmt.Errorf("checksum failed: %w: %s", err, output)
	}
	logger.Verify("OK Syncthing checksum: %s",
		strings.TrimSpace(string(output)))
	return nil
}
