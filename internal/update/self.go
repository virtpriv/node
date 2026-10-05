package update

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/virtualprivatenode/vpn/internal/host"
	"github.com/virtualprivatenode/vpn/internal/release"
	"github.com/virtualprivatenode/vpn/internal/system"
)

// Self completes an accepted VPN binary update in the root helper. The helper
// admits the target against its running version before calling this operation.
// Observer disconnection does not cancel accepted work. This workflow has no
// durable recovery record or automatic rollback; failures retain the workspace.
func Self(version string, progress func(int)) error {
	if os.Geteuid() != 0 {
		return errors.New("self-update requires the root helper")
	}
	archive, err := release.ArchiveName(version)
	if err != nil {
		return err
	}
	workDir, err := os.MkdirTemp("", "vpn-update-")
	if err != nil {
		return fmt.Errorf("create work dir: %w", err)
	}
	if err := runSelfUpdate(version, archive, workDir, selfUpdateOps{
		download: system.DownloadRequireTor,
		verifySignature: func(workDir string) error {
			_, err := release.VerifySignature(workDir)
			return err
		},
		verifyChecksum: release.VerifyChecksum,
		install:        host.InstallVPNUpdate,
	}, progress); err != nil {
		return err
	}
	os.RemoveAll(workDir)
	return nil
}

// The private dependencies permit failure injection at the trust and mutation
// boundaries without downloading a release or replacing the running binary.
type selfUpdateOps struct {
	download        func(url, dest string) error
	verifySignature func(workDir string) error
	verifyChecksum  func(version, workDir string) error
	install         func(version, workDir string) error
}

func runSelfUpdate(version, archive, workDir string, ops selfUpdateOps, progress func(int)) error {
	baseURL := "https://github.com/virtualprivatenode/vpn/releases/download/v" + version
	for _, name := range []string{archive, "SHA256SUMS", "SHA256SUMS.asc"} {
		if err := ops.download(baseURL+"/"+name, filepath.Join(workDir, name)); err != nil {
			return fmt.Errorf("downloading v%s: %w", version, err)
		}
	}
	progress(0)
	if err := ops.verifySignature(workDir); err != nil {
		return fmt.Errorf("verifying signature: %w", err)
	}
	progress(1)
	if err := ops.verifyChecksum(version, workDir); err != nil {
		return fmt.Errorf("verifying checksum: %w", err)
	}
	progress(2)
	if err := ops.install(version, workDir); err != nil {
		return fmt.Errorf("installing new binary: %w", err)
	}
	progress(3)
	return nil
}
