package host

import (
	"fmt"
	"os"

	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/system"
)

func createLNDDirs(username string) error {
	dirs := []struct {
		path  string
		owner string
		mode  os.FileMode
	}{
		{paths.LNDDir, "root:" + username, 0750},
		{paths.LNDDataDir, username + ":" + username, 0750},
	}
	for _, d := range dirs {
		if err := system.RunRoot("mkdir", "-p", d.path); err != nil {
			return err
		}
		if err := system.RunRoot("chown", d.owner, d.path); err != nil {
			return err
		}
		if err := system.RunRoot("chmod", fmt.Sprintf("%o", d.mode), d.path); err != nil {
			return err
		}
	}
	return nil
}

// WriteLNDService writes the LND unit without auto-unlock. The install is
// its only caller, and auto-unlock is turned on later from the TUI, which
// writes its own unit.
func WriteLNDService() error {
	return system.WriteFileRoot(paths.LNDService,
		[]byte(LNDServiceUnit(lndUser, false)), 0644)
}

// EnableAndRestartLND enables LND and restarts it with the installed configuration.
// Restart also applies changes when LND is running during an interrupted-install
// resume. The caller separately verifies and stages the TLS certificate.
func EnableAndRestartLND() error {
	if err := system.RunRoot("systemctl", "daemon-reload"); err != nil {
		return err
	}
	if err := system.RunRoot("systemctl", "enable", "lnd"); err != nil {
		return err
	}
	return system.RunRoot("systemctl", "restart", "lnd")
}
