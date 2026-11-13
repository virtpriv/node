// Trust model
//
// VPN release packaging and verification share the signing key pinned here.
// Component provisioning and signer policies belong to host.
// artifact.VerifySignature owns isolated GPG verification. Each caller
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
// Source: generated locally, the public key hosted at
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

// ErrKeyUnavailable and ErrFolderKeyUnavailable refuse a release for want of
// the pinned key, the second after the releases folder's copy was tried too.
var (
	ErrKeyUnavailable       = errors.New("the release signing key could not be downloaded from the repository or the key server")
	ErrFolderKeyUnavailable = errors.New("the release signing key could not be downloaded, and ripsline.asc in the releases folder does not hold it")
)

// KeyCheck reports how the release key was obtained.
type KeyCheck struct {
	// WebsiteUnreachable means the key server could not be asked, so a
	// revocation published only there was not seen.
	WebsiteUnreachable bool
}

// VerifySignature requires the pinned VPN signer and rejects bad signatures.
// The caller owns the private workspace containing the manifest. folderKey is
// a private copy of the owner's ripsline.asc, or empty. It is used only when
// neither download gives the pinned key.
func VerifySignature(version, workDir, folderKey string) (KeyCheck, error) {
	return verifySignature(version, workDir, folderKey, SigningFingerprint, system.DownloadRequireTor)
}

func verifySignature(version, workDir, folderKey, fingerprint string, download func(url, dest string) error) (KeyCheck, error) {
	var check KeyCheck
	assets, err := AssetsFor(version)
	if err != nil {
		return check, err
	}
	logger.Verify("--- VPN release signature verification ---")

	sumsFile := filepath.Join(workDir, assets.Manifest)
	sigFile := filepath.Join(workDir, assets.Signature)

	if _, err := os.Stat(sumsFile); err != nil {
		logger.Verify("FAIL: %s not found", assets.Manifest)
		return check, fmt.Errorf("%s not found", assets.Manifest)
	}
	if _, err := os.Stat(sigFile); err != nil {
		logger.Verify("FAIL: %s not found", assets.Signature)
		return check, fmt.Errorf("%s not found", assets.Signature)
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
	pinned := map[string]bool{fingerprint: true}
	var result artifact.Result
	if len(keyFiles) > 0 {
		result, err = artifact.VerifySignature(keyFiles, sigFile, sumsFile, pinned)
		if err != nil {
			return check, fmt.Errorf("signature verification failed: %w", err)
		}
	}
	// An answer that is not the pinned key is no better than no answer.
	if !result.Imported[repositoryKey] && !result.Imported[websiteKey] {
		if folderKey == "" {
			logger.Verify("FAIL: release signing key not obtained")
			return check, ErrKeyUnavailable
		}
		// The owner's copy may be old, so the review says the key server
		// was not asked.
		logger.Verify("release key from the releases folder, both downloads gave none")
		check.WebsiteUnreachable = true
		result, err = artifact.VerifySignature([]string{folderKey}, sigFile, sumsFile, pinned)
		if err != nil {
			return check, fmt.Errorf("signature verification failed: %w", err)
		}
		if !result.Imported[folderKey] {
			logger.Verify("FAIL: release signing key not obtained")
			return check, ErrFolderKeyUnavailable
		}
	} else if !result.Imported[websiteKey] {
		check.WebsiteUnreachable = true
	}

	if result.Bad {
		logger.Verify("FAIL: bad signature detected")
		return check, fmt.Errorf(
			"bad signature detected, verification aborted")
	}

	if result.Signers < 1 {
		if len(result.Revoked) > 0 {
			logger.Verify("FAIL: release signing key is revoked")
			return check, fmt.Errorf(
				"the key that signed this release is revoked, do not install it")
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
