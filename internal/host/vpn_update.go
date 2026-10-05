package host

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/release"
	"github.com/virtpriv/node/internal/system"
)

// InstallVPNUpdate extracts the verified VPN archive and installs its binary
// at the fixed system path. The update coordinator owns the private workspace
// and must authenticate the manifest and archive before calling this operation.
// Replacement is not a transaction and provides no automatic rollback.
func InstallVPNUpdate(version, workDir string) error {
	if os.Geteuid() != 0 {
		return errors.New("VPN binary installation requires root")
	}
	archive, err := release.ArchiveName(version)
	if err != nil {
		return err
	}
	if err := system.Run("tar", "-xzf", filepath.Join(workDir, archive), "-C", workDir); err != nil {
		return err
	}
	return system.RunRoot("install", "-m", "755", filepath.Join(workDir, "vpn"), paths.BinaryPath)
}
