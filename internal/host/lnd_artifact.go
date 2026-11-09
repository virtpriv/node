package host

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/virtpriv/node/internal/artifact"
	"github.com/virtpriv/node/internal/component"
	"github.com/virtpriv/node/internal/logger"
	"github.com/virtpriv/node/internal/system"
	"github.com/virtpriv/node/internal/update/protocol"
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

// InstallLNDBinaries installs lnd and lncli after VerifyLND succeeds.
// The caller owns the private workspace and lifecycle admission.
func InstallLNDBinaries(version, workDir string) error {
	return installPrograms(protocol.LND, version, workDir)
}

// lndSigners are the LND developers whose keys may sign a release, by the
// name in their signature and key file names, with each key's primary
// fingerprint.
// Source: scripts/verify-install.sh and scripts/keys in
// github.com/lightningnetwork/lnd at master 11f79be (November 6 2026). Each
// key file's primary fingerprint matched the script's list. Every LND final
// release from v0.20.0 to v0.21.6-beta carries at least four of their
// signatures (November 9 2026). An expired key still counts.
var lndSigners = map[string]string{
	"roasbeef":    "A5B61896952D9FDA83BC054CDC42612E89237182",
	"bhandras":    "9FC6B0BFD597A94DBF09708280E5375C094198D8",
	"sputn1ck":    "4DC235556B18694E08518DBB671103D881A5F0E4",
	"yyforyongyu": "E85497D2DBA0EB9ADB0024279BCD95C4FF296868",
	"hieblmi":     "32F7EA1E7A0339F7D37164B9F82D456EA023C9BF",
	"suheb":       "3E9BD4436C288039CA827A9200C9E2BC2E45666F",
	"ziggie1984":  "5F75437E11695F86D50C11BB1AFF9C4DCED6D666",
	"ViktorT-11":  "C20A78516A0944900EBFCA29961CC8259AE675D4",
	"georgetsagk": "1583B601BB57CC7CD2DF8A87E08DEA9B12B66AF6",
	"gijswijs":    "7530B54D5E45A68760E68926019A44857735FD20",
	"boris":       "BCEE34B0F9CD832214CE53005EA98470361ACB4F",
}

// lndMinSigners is how many distinct listed developers must have signed.
// LND's own script asks for five, but it is meant for LND's latest release
// only, and LND removes the key of a developer who stops signing.
const lndMinSigners = 3

// VerifyLND requires three distinct listed LND developers, then checks the
// archive against the signed manifest and the hash this release pins.
func VerifyLND(version, workDir string) error {
	logger.Verify("LND signature verification")

	manifestFile := filepath.Join(workDir, "manifest.txt")
	if _, err := os.Stat(manifestFile); err != nil {
		logger.Verify("FAIL: LND manifest not found")
		return fmt.Errorf("LND manifest not found at %s",
			manifestFile)
	}

	// Pin every listed key, whichever signatures and keys arrive.
	pinnedFPs := make(map[string]bool, len(lndSigners))
	for _, fingerprint := range lndSigners {
		pinnedFPs[fingerprint] = true
	}

	// Each developer signs in a file of their own. gpg reads every
	// signature in one file, as in Bitcoin Core's SHA256SUMS.asc, so the
	// files are joined. A signer whose file or key cannot be downloaded is
	// left out, as LND removes the keys of developers who stop signing.
	var signatures []byte
	var keyFiles []string
	for _, name := range component.LNDSigners {
		if _, listed := lndSigners[name]; !listed {
			logger.Verify("FAIL: %s is not a listed LND developer", name)
			return fmt.Errorf("LND signer %s is not pinned in this VPN release", name)
		}
		sigFile := filepath.Join(workDir,
			fmt.Sprintf("manifest-%s-v%s.sig", name, version))
		sigURL := fmt.Sprintf(
			"https://github.com/lightningnetwork/lnd/releases/download/v%s/manifest-%s-v%s.sig",
			version, name, version)
		if err := system.DownloadRequireTor(sigURL, sigFile); err != nil {
			logger.Verify("SKIP %s: signature download failed: %v", name, err)
			continue
		}
		keyFile := filepath.Join(workDir, "lnd-key-"+name+".asc")
		keyURL := "https://raw.githubusercontent.com/lightningnetwork/lnd/master/scripts/keys/" + name + ".asc"
		if err := system.DownloadRequireTor(keyURL, keyFile); err != nil {
			logger.Verify("SKIP %s: key download failed: %v", name, err)
			continue
		}
		signature, err := os.ReadFile(sigFile)
		if err != nil {
			return fmt.Errorf("read LND signature: %w", err)
		}
		signatures = append(signatures, signature...)
		keyFiles = append(keyFiles, keyFile)
		logger.Verify("OK %s: signature and key downloaded", name)
	}
	if len(keyFiles) == 0 {
		logger.Verify("FAIL: no LND signature downloaded with its key")
		return fmt.Errorf("could not download any LND signatures")
	}

	joined := filepath.Join(workDir, "manifest-signatures.sig")
	if err := os.WriteFile(joined, signatures, 0o600); err != nil {
		return fmt.Errorf("write LND signatures: %w", err)
	}
	result, err := artifact.VerifySignature(
		keyFiles, joined, manifestFile, pinnedFPs)
	if err != nil {
		return fmt.Errorf(
			"signature verification failed: %w", err)
	}
	if err := requirePinnedSigners("LND", result, lndMinSigners); err != nil {
		return err
	}
	if err := verifyLNDChecksum(workDir); err != nil {
		return err
	}
	return checkPinnedArchive(protocol.LND, workDir)
}

func verifyLNDChecksum(workDir string) error {
	logger.Verify("LND checksum verification")
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
