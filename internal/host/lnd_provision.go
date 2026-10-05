package host

import (
	"fmt"
	"os"

	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/logger"
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

// WriteLNDServiceFromConfig writes the LND unit that matches the
// node's desired state: the unlock variant when the config says
// auto-unlock is enabled AND the wallet password file is actually
// present, the plain variant otherwise. Requiring the file too is deliberate: a
// unit pointing at a missing password file would keep LND from
// starting at all.
func WriteLNDServiceFromConfig(cfg *config.AppConfig) error {
	withUnlock := false
	if cfg.AutoUnlock {
		_, err := os.Stat(paths.LNDWalletPassword)
		withUnlock = err == nil
	}
	if cfg.AutoUnlock && !withUnlock {
		logger.Install(
			"auto_unlock is enabled in the config but %s is "+
				"missing — writing the LND unit without the unlock "+
				"flag; re-enable auto-unlock from the node TUI",
			paths.LNDWalletPassword)
	}
	return system.WriteFileRoot(paths.LNDService,
		[]byte(LNDServiceUnit(lndUser, withUnlock)), 0644)
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
