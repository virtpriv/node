package host

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/virtualprivatenode/vpn/internal/artifact"
	"github.com/virtualprivatenode/vpn/internal/logger"
	"github.com/virtualprivatenode/vpn/internal/system"
)

// DownloadLND fetches the release archive and manifest over Tor.
func DownloadLND(version, workDir string) error {
	filename := fmt.Sprintf("lnd-linux-amd64-v%s.tar.gz", version)
	url := fmt.Sprintf(
		"https://github.com/lightningnetwork/lnd/releases/download/v%s/%s",
		version, filename)
	manifestURL := fmt.Sprintf(
		"https://github.com/lightningnetwork/lnd/releases/download/v%s/manifest-v%s.txt",
		version, version)
	if err := system.DownloadRequireTor(
		url, filepath.Join(workDir, filename)); err != nil {
		return err
	}
	if err := system.DownloadRequireTor(
		manifestURL,
		filepath.Join(workDir, "manifest.txt")); err != nil {
		return fmt.Errorf("download LND manifest: %w", err)
	}
	return nil
}

// InstallLNDBinaries installs the archive after VerifyLND succeeds.
// The caller owns the private workspace and lifecycle admission.
func InstallLNDBinaries(version, workDir string) error {
	filename := fmt.Sprintf("lnd-linux-amd64-v%s.tar.gz", version)
	if err := system.Run("tar", "-xzf",
		filepath.Join(workDir, filename),
		"-C", workDir); err != nil {
		return err
	}
	extractDir := filepath.Join(workDir,
		fmt.Sprintf("lnd-linux-amd64-v%s", version))
	for _, bin := range []string{"lnd", "lncli"} {
		src := filepath.Join(extractDir, bin)
		if err := system.RunRoot("install", "-m", "0755",
			"-o", "root", "-g", "root",
			src, "/usr/local/bin/"); err != nil {
			return err
		}
	}
	return nil
}

// lndSigner is the trusted LND release signer.
// Source: github.com/lightningnetwork/lnd/tree/master/scripts/keys
// Cross-check: roasbeef.asc at the listed URL. Verified against
// the LND v0.21.2-beta manifest signature during release intake
// (August 28, 2026). Primary-key fingerprint; the signing
// subkey (2962...) is owned by this primary.
var lndSigner = struct {
	name        string
	fingerprint string
	keyURL      string
}{
	name:        "roasbeef",
	fingerprint: "A5B61896952D9FDA83BC054CDC42612E89237182",
	keyURL:      "https://raw.githubusercontent.com/lightningnetwork/lnd/master/scripts/keys/roasbeef.asc",
}

// VerifyLND requires the pinned release signer before checking the archive.
func VerifyLND(version, workDir string) error {
	logger.Verify("--- LND signature verification ---")

	manifestFile := filepath.Join(workDir, "manifest.txt")
	if _, err := os.Stat(manifestFile); err != nil {
		logger.Verify("FAIL: LND manifest not found")
		return fmt.Errorf("LND manifest not found at %s",
			manifestFile)
	}

	// Download signature file.
	sigFile := filepath.Join(workDir,
		fmt.Sprintf("manifest-roasbeef-v%s.sig", version))
	sigURL := fmt.Sprintf(
		"https://github.com/lightningnetwork/lnd/releases/download/v%s/manifest-roasbeef-v%s.sig",
		version, version)
	if err := system.DownloadRequireTor(
		sigURL, sigFile); err != nil {
		logger.Verify("FAIL: download LND signature: %v", err)
		return fmt.Errorf("download LND signature: %w", err)
	}

	// Download signing key.
	keyFile := filepath.Join(workDir, "lnd-key-roasbeef.asc")
	if err := system.DownloadRequireTor(
		lndSigner.keyURL, keyFile); err != nil {
		logger.Verify("FAIL: download LND signing key: %v", err)
		return fmt.Errorf("download LND signing key: %w", err)
	}

	pinnedFPs := map[string]bool{lndSigner.fingerprint: true}

	result, err := artifact.VerifySignature(
		[]string{keyFile}, sigFile, manifestFile, pinnedFPs)
	if err != nil {
		return fmt.Errorf(
			"LND signature verification failed: %w", err)
	}
	distinct, hasBadSig := result.Signers, result.Bad
	if !hasBadSig && distinct < 1 && len(result.Revoked) > 0 {
		logger.Verify("FAIL: LND release signing key is revoked")
		return fmt.Errorf("LND's release signing key has been revoked; a newer VPN release is needed")
	}

	if hasBadSig {
		logger.Verify("FAIL: bad LND signature detected")
		return fmt.Errorf(
			"bad LND signature detected — verification aborted")
	}

	if distinct < 1 {
		logger.Verify(
			"FAIL: LND signature not valid against pinned fingerprint")
		return fmt.Errorf("LND signature verification failed")
	}

	logger.Verify(
		"OK LND: signature valid (roasbeef, pinned fingerprint)")
	return verifyLNDChecksum(workDir)
}

func verifyLNDChecksum(workDir string) error {
	logger.Verify("--- LND checksum verification ---")
	manifestFile := filepath.Join(workDir, "manifest.txt")
	if _, err := os.Stat(manifestFile); err != nil {
		logger.Verify("FAIL: LND manifest not found")
		return fmt.Errorf("LND manifest not found")
	}
	// exec.Command used directly because sha256sum --check needs
	// working directory set to where the tarball was downloaded.
	cmd := exec.Command("sha256sum",
		"--ignore-missing", "--check", "manifest.txt")
	cmd.Dir = workDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Verify("FAIL: LND checksum: %s",
			string(output))
		return fmt.Errorf("checksum failed: %w: %s", err, output)
	}
	logger.Verify("OK LND checksum: %s",
		strings.TrimSpace(string(output)))
	return nil
}
