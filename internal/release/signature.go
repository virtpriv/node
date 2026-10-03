// Trust model
//
// VPN release packaging and verification share the signing key pinned here.
// Component provisioning and signer policies belong to host.
// artifact.VerifySignature owns isolated GPG verification; each caller
// rejects bad signatures and insufficient trusted signers.

package release

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/virtualprivatenode/vpn/internal/artifact"
	"github.com/virtualprivatenode/vpn/internal/logger"
	"github.com/virtualprivatenode/vpn/internal/system"
)

// ── Trust anchors ───────────────────────────────────────
//
// Each fingerprint was sourced and cross-checked as noted.
// To re-verify: download the key from the listed URL, import
// into an ephemeral keyring, and confirm the fingerprint with
// gpg --with-colons --list-keys.

// SigningFingerprint is the primary fingerprint of the vpn
// release signing key.
// Source: generated locally; public key hosted at
// keys.openpgp.org. Cross-check: docs/verifying.md publishes the
// same fingerprint used for manual release verification.
const SigningFingerprint = "AFA0EBACDC9A4C4AA7B0154AC97CE10F170BA5FE"

// ── Release verification ────────────────────────────

// VerifySignature requires the pinned VPN signer and rejects bad signatures.
// The caller owns the private workspace containing the downloaded manifest.
func VerifySignature(workDir string) error {
	logger.Verify("--- VPN release signature verification ---")

	sumsFile := filepath.Join(workDir, "SHA256SUMS")
	sigFile := filepath.Join(workDir, "SHA256SUMS.asc")

	if _, err := os.Stat(sumsFile); err != nil {
		logger.Verify("FAIL: SHA256SUMS not found")
		return fmt.Errorf("SHA256SUMS not found")
	}
	if _, err := os.Stat(sigFile); err != nil {
		logger.Verify("FAIL: SHA256SUMS.asc not found")
		return fmt.Errorf("SHA256SUMS.asc not found")
	}

	// Download the release signing key fresh into the work
	// directory. artifact.VerifySignature imports it into an ephemeral
	// GPG home; the shared keyring is never touched.
	keyFile := filepath.Join(workDir, "release-key.asc")
	keyURL := fmt.Sprintf(
		"https://keys.openpgp.org/vks/v1/by-fingerprint/%s",
		SigningFingerprint)
	if err := system.DownloadRequireTor(
		keyURL, keyFile); err != nil {
		logger.Verify(
			"FAIL: download release signing key: %v", err)
		return fmt.Errorf(
			"download release signing key: %w", err)
	}

	pinnedFPs := map[string]bool{SigningFingerprint: true}

	distinct, hasBadSig, err := artifact.VerifySignature(
		[]string{keyFile}, sigFile, sumsFile, pinnedFPs)
	if err != nil {
		return fmt.Errorf(
			"signature verification failed: %w", err)
	}

	if hasBadSig {
		logger.Verify("FAIL: bad signature detected")
		return fmt.Errorf(
			"bad signature detected: verification aborted")
	}

	if distinct < 1 {
		logger.Verify(
			"FAIL: signature not from the release signing key")
		return fmt.Errorf(
			"signature not from the release signing key")
	}

	logger.Verify("OK release: signature valid " +
		"(release key, pinned fingerprint)")
	return nil
}
