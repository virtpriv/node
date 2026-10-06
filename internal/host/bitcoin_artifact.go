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

// DownloadBitcoinCore fetches the release archive and detached manifest over Tor.
func DownloadBitcoinCore(version, workDir string) error {
	filename := fmt.Sprintf("bitcoin-%s-x86_64-linux-gnu.tar.gz", version)
	baseURL := fmt.Sprintf("https://bitcoincore.org/bin/bitcoin-core-%s", version)
	if err := system.DownloadRequireTor(
		baseURL+"/"+filename,
		filepath.Join(workDir, filename)); err != nil {
		return err
	}
	if err := system.DownloadRequireTor(
		baseURL+"/SHA256SUMS",
		filepath.Join(workDir, "SHA256SUMS")); err != nil {
		return err
	}
	return system.DownloadRequireTor(
		baseURL+"/SHA256SUMS.asc",
		filepath.Join(workDir, "SHA256SUMS.asc"))
}

// InstallBitcoinCoreBinaries installs the archive after VerifyBitcoinCore succeeds.
// The caller owns the private workspace and lifecycle admission.
func InstallBitcoinCoreBinaries(version, workDir string) error {
	filename := fmt.Sprintf("bitcoin-%s-x86_64-linux-gnu.tar.gz", version)
	if err := system.Run("tar", "-xzf",
		filepath.Join(workDir, filename),
		"-C", workDir); err != nil {
		return err
	}
	extractDir := filepath.Join(workDir,
		fmt.Sprintf("bitcoin-%s", version), "bin")
	entries, err := os.ReadDir(extractDir)
	if err != nil {
		return fmt.Errorf("read dir: %w", err)
	}
	for _, entry := range entries {
		src := filepath.Join(extractDir, entry.Name())
		if err := system.RunRoot("install", "-m", "0755",
			"-o", "root", "-g", "root",
			src, "/usr/local/bin/"); err != nil {
			return err
		}
	}
	return nil
}

// bitcoinCoreSigners are the trusted Bitcoin Core builder keys.
// Source: github.com/bitcoin-core/guix.sigs/tree/main/builder-keys
// Cross-check: each key's fingerprint against the .gpg file at
// the listed URL. Verified against the Bitcoin Core 29.4
// SHA256SUMS.asc, which all five signed (October 6 2026).
// Primary-key fingerprints, not subkeys. Threshold: 2 of 5
// distinct signers required.
var bitcoinCoreSigners = []struct {
	name        string
	fingerprint string
	keyURL      string
}{
	{
		name:        "fanquake",
		fingerprint: "E777299FC265DD04793070EB944D35F9AC3DB76A",
		keyURL:      "https://raw.githubusercontent.com/bitcoin-core/guix.sigs/main/builder-keys/fanquake.gpg",
	},
	{
		name:        "guggero",
		fingerprint: "F4FC70F07310028424EFC20A8E4256593F177720",
		keyURL:      "https://raw.githubusercontent.com/bitcoin-core/guix.sigs/main/builder-keys/guggero.gpg",
	},
	{
		name:        "hebasto",
		fingerprint: "D1DBF2C4B96F2DEBF4C16654410108112E7EA81F",
		keyURL:      "https://raw.githubusercontent.com/bitcoin-core/guix.sigs/main/builder-keys/hebasto.gpg",
	},
	{
		name:        "theStack",
		fingerprint: "6A8F9C266528E25AEB1D7731C2371D91CB716EA7",
		keyURL:      "https://raw.githubusercontent.com/bitcoin-core/guix.sigs/main/builder-keys/theStack.gpg",
	},
	{
		name:        "willcl-ark",
		fingerprint: "67AA5B46E7AF78053167FE343B8F814A784218F8",
		keyURL:      "https://raw.githubusercontent.com/bitcoin-core/guix.sigs/main/builder-keys/willcl-ark.gpg",
	},
}

// VerifyBitcoinCore requires two distinct pinned signers before checking the archive.
func VerifyBitcoinCore(workDir string) error {
	const minValid = 2
	logger.Verify("--- Bitcoin Core signature verification ---")

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

	// Pin ALL fingerprints regardless of download success.
	// The pinned set is the trust anchor; it does not change
	// based on which keys we managed to download.
	pinnedFPs := make(map[string]bool)
	for _, signer := range bitcoinCoreSigners {
		pinnedFPs[signer.fingerprint] = true
	}

	// Download signing keys into the pipeline work directory.
	var keyFiles []string
	for _, signer := range bitcoinCoreSigners {
		keyFile := filepath.Join(workDir,
			fmt.Sprintf("btc-key-%s.gpg", signer.name))
		if err := system.DownloadRequireTor(
			signer.keyURL, keyFile); err != nil {
			logger.Verify("SKIP %s: download failed: %v",
				signer.name, err)
			continue
		}
		keyFiles = append(keyFiles, keyFile)
		logger.Verify("OK %s: key downloaded", signer.name)
	}

	if len(keyFiles) == 0 {
		logger.Verify("FAIL: no Bitcoin Core signing keys downloaded")
		return fmt.Errorf(
			"could not download any Bitcoin Core signing keys")
	}

	result, err := artifact.VerifySignature(
		keyFiles, sigFile, sumsFile, pinnedFPs)
	if err != nil {
		return fmt.Errorf(
			"signature verification failed: %w", err)
	}
	distinct, hasBadSig := result.Signers, result.Bad

	if hasBadSig {
		logger.Verify("FAIL: bad signature detected")
		return fmt.Errorf(
			"bad signature detected, verification aborted")
	}

	logger.Verify(
		"Bitcoin Core valid pinned signatures: %d/%d required",
		distinct, minValid)

	if distinct < minValid {
		logger.Verify(
			"FAIL: insufficient valid signatures: got %d, need %d, revoked signers %d",
			distinct, minValid, len(result.Revoked))
		if len(result.Revoked) > 0 {
			return fmt.Errorf(
				"insufficient valid signatures: got %d, need %d. %d pinned signer key(s) revoked, a newer VPN release is needed",
				distinct, minValid, len(result.Revoked))
		}
		return fmt.Errorf(
			"insufficient valid signatures: got %d, need %d",
			distinct, minValid)
	}

	logger.Verify("OK Bitcoin Core: %d valid pinned signatures",
		distinct)
	return verifyBitcoinChecksum(workDir)
}

func verifyBitcoinChecksum(workDir string) error {
	logger.Verify("--- Bitcoin Core checksum verification ---")
	// exec.Command used directly because sha256sum --check needs
	// working directory set to where the tarball was downloaded.
	cmd := exec.Command("sha256sum",
		"--ignore-missing", "--check", "SHA256SUMS")
	cmd.Dir = workDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Verify("FAIL: Bitcoin Core checksum: %s",
			string(output))
		return fmt.Errorf("checksum failed: %w: %s", err, output)
	}
	logger.Verify("OK Bitcoin Core checksum: %s",
		strings.TrimSpace(string(output)))
	return nil
}
