package host

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/system"
	"github.com/virtpriv/node/internal/update/files"
)

// CheckTorSettings is the install check "Tor's settings are Debian's own".
// It writes nothing. vpn uses Tor as Debian's package sets it up and writes
// no torrc, so a torrc that already sets something is refused before the
// install changes anything. A missing file passes, since the base packages
// step installs Tor.
func CheckTorSettings() error {
	return torSettings(true)
}

// CheckTorSettingsInstalled is the same fact once Tor is installed, when a
// missing file cannot be read as Debian's own.
func CheckTorSettingsInstalled() error {
	return torSettings(false)
}

func torSettings(missingPasses bool) error {
	data, err := os.ReadFile(paths.Torrc)
	if missingPasses && errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cannot read %s: %w", paths.Torrc, err)
	}
	return torSettingsVerdict(string(data))
}

// torSettingsVerdict reads a torrc's content. Every line that is not empty
// and not a comment counts as a setting, an include line too. The refusal
// names the first one's line and never its text, which can hold a bridge
// address or a password hash.
func torSettingsVerdict(content string) error {
	for i, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return fmt.Errorf("found settings in %s, the first on line %d. "+
			"vpn uses Tor as Debian sets it up, so this file must hold only comments. "+
			"Use a fresh machine, or turn the settings into comments and run the installer again",
			paths.Torrc, i+1)
	}
	return nil
}

var (
	runTorServiceAction = func(action string) error {
		return system.RunRoot("systemctl", action, "tor")
	}
	writeTorRestartRule = writeTorRestartDropIn
	reloadSystemdForTor = func() error {
		return system.RunRoot("systemctl", "daemon-reload")
	}
)

// torRestartRule replaces systemd's default restart wait of 100 ms for the
// real Tor. With that wait systemd's default start limit, five starts in ten
// seconds, stops the retries and Tor stays off until a reboot or a manual
// start. Thirty seconds apart, systemd
// never reaches that limit and keeps trying after a crash. A clean stop is
// not restarted.
const torRestartRule = "# Restart Tor 30 seconds after a crash, without giving up\n" +
	"[Service]\nRestartSec=30\n"

// writeTorRestartDropIn writes the rule for fresh installs only. Nodes
// installed earlier keep Debian's rule.
func writeTorRestartDropIn() error {
	dir := filepath.Dir(paths.TorRestartDropIn)
	if err := os.Mkdir(dir, 0o755); err == nil {
		if err := os.Chmod(dir, 0o755); err != nil {
			return fmt.Errorf("set Tor drop-in directory mode: %w", err)
		}
	} else if !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("create Tor drop-in directory: %w", err)
	}
	if info, err := os.Lstat(dir); err != nil {
		return err
	} else if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	// Saved every time, so a resume after a failed save still saves it.
	if err := files.Sync(filepath.Dir(dir)); err != nil {
		return err
	}
	return system.WriteFileRoot(paths.TorRestartDropIn,
		[]byte(torRestartRule), 0o644)
}

// EnableAndRestartTor belongs to initial installation. That operation owns
// establishing Tor's boot persistence as part of the base node. systemd
// loads the restart rule before Tor is restarted, so the rule applies from
// the first crash.
func EnableAndRestartTor() error {
	if err := writeTorRestartRule(); err != nil {
		return err
	}
	if err := reloadSystemdForTor(); err != nil {
		return err
	}
	if err := runTorServiceAction("enable"); err != nil {
		return err
	}
	return runTorServiceAction("restart")
}
