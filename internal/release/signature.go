// Trust model
//
// VPN release packaging and verification share the signing key pinned here.
// Component provisioning and signer policies belong to host.
// artifact.VerifySignature owns isolated GPG verification; each caller
// rejects bad signatures and insufficient trusted signers.

package release

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/virtpriv/node/internal/artifact"
	"github.com/virtpriv/node/internal/logger"
	"github.com/virtpriv/node/internal/system"
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
// keys.openpgp.org and kept in this repository as keys/ripsline.asc.
// Cross-check: docs/verifying.md publishes the same fingerprint used
// for manual release verification.
const SigningFingerprint = "AFA0EBACDC9A4C4AA7B0154AC97CE10F170BA5FE"

// ── Release verification ────────────────────────────

// The release key is fetched from two places that fail independently. Both
// addresses are fixed in every installed helper, so a later release can only
// replace one of them while the other still works.
const (
	keyFileURL    = "https://raw.githubusercontent.com/virtpriv/node/main/keys/ripsline.asc"
	keyWebsiteURL = "https://keys.openpgp.org/vks/v1/by-fingerprint/"
)

var errKeyUnavailable = errors.New("the release signing key could not be downloaded from the repository or the key server")

// KeyCheck reports how the release key was obtained.
type KeyCheck struct {
	// WebsiteUnreachable means the key server could not be asked, so a
	// revocation published only there was not seen.
	WebsiteUnreachable bool
}

// VerifySignature requires the pinned VPN signer and rejects bad signatures.
// The caller owns the private workspace containing the downloaded manifest.
func VerifySignature(workDir string) (KeyCheck, error) {
	return verifySignature(workDir, SigningFingerprint, system.DownloadRequireTor)
}

func verifySignature(workDir, fingerprint string, download func(url, dest string) error) (KeyCheck, error) {
	var check KeyCheck
	logger.Verify("--- VPN release signature verification ---")

	sumsFile := filepath.Join(workDir, "SHA256SUMS")
	sigFile := filepath.Join(workDir, "SHA256SUMS.asc")

	if _, err := os.Stat(sumsFile); err != nil {
		logger.Verify("FAIL: SHA256SUMS not found")
		return check, fmt.Errorf("SHA256SUMS not found")
	}
	if _, err := os.Stat(sigFile); err != nil {
		logger.Verify("FAIL: SHA256SUMS.asc not found")
		return check, fmt.Errorf("SHA256SUMS.asc not found")
	}

	// Both copies are always fetched and used together, so a revocation or a
	// new signing subkey known to either one is seen. Only material signed by
	// the pinned key has any effect, so neither source has to be trusted.
	// The repository file is loaded first: see artifact.VerifySignature.
	var keyFiles []string
	repositoryKey := filepath.Join(workDir, "release-key-repository.asc")
	if err := download(keyFileURL, repositoryKey); err != nil {
		logger.Verify("SKIP release key from the repository: %v", err)
	} else {
		keyFiles = append(keyFiles, repositoryKey)
	}
	websiteKey := filepath.Join(workDir, "release-key-website.asc")
	if err := download(keyWebsiteURL+fingerprint, websiteKey); err != nil {
		logger.Verify("SKIP release key from the key server: %v", err)
		check.WebsiteUnreachable = true
	} else {
		keyFiles = append(keyFiles, websiteKey)
	}
	if len(keyFiles) == 0 {
		logger.Verify("FAIL: release signing key not downloaded")
		return check, errKeyUnavailable
	}

	result, err := artifact.VerifySignature(
		keyFiles, sigFile, sumsFile, map[string]bool{fingerprint: true})
	if err != nil {
		return check, fmt.Errorf(
			"signature verification failed: %w", err)
	}

	if result.Bad {
		logger.Verify("FAIL: bad signature detected")
		return check, fmt.Errorf(
			"bad signature detected: verification aborted")
	}

	// An answer that is not the pinned key is no better than no answer.
	if !result.Imported[websiteKey] {
		check.WebsiteUnreachable = true
	}
	if !result.Imported[repositoryKey] && !result.Imported[websiteKey] {
		logger.Verify("FAIL: release signing key not obtained")
		return check, errKeyUnavailable
	}

	if result.Signers < 1 {
		if len(result.Revoked) > 0 {
			logger.Verify("FAIL: release signing key is revoked")
			return check, fmt.Errorf(
				"the key that signed this release has been revoked; do not install it")
		}
		logger.Verify(
			"FAIL: signature not from the release signing key")
		return check, fmt.Errorf(
			"signature not from the release signing key")
	}

	logger.Verify("OK release: signature valid " +
		"(release key, pinned fingerprint)")
	return check, nil
}
