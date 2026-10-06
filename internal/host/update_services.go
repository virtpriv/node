package host

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/system"
	"github.com/virtpriv/node/internal/update/files"
	"github.com/virtpriv/node/internal/update/protocol"
)

func UpdateUnit(c protocol.Component) string {
	switch c {
	case protocol.Bitcoin:
		return "bitcoind.service"
	case protocol.LND:
		return "lnd.service"
	case protocol.Syncthing:
		return "syncthing.service"
	}
	return ""
}

func updateBinary(c protocol.Component) string {
	if c == protocol.Bitcoin {
		return "/usr/local/bin/bitcoind"
	}
	return "/usr/local/bin/" + string(c)
}

func ObserveUpdateVersions(vpn string, cfg *config.AppConfig) (protocol.Versions, error) {
	v := protocol.Versions{VPN: vpn}
	if err := files.Check(paths.BinaryPath, false); err != nil {
		return v, err
	}
	installed, err := system.RunRootOutputWithTimeout(10*time.Second, paths.BinaryPath, "version")
	if err != nil {
		return v, err
	}
	if strings.TrimSpace(installed) != vpn {
		return v, errors.New("VPN executable changed, reconnect before reviewing an update")
	}
	for _, c := range protocol.Components {
		if c == protocol.Syncthing && !cfg.SyncthingEnabled {
			for _, p := range []string{paths.SyncthingBinary, paths.SyncthingConfigXML} {
				if _, err := os.Lstat(p); !os.IsNotExist(err) {
					return v, errors.New("syncthing installation disagrees with node configuration")
				}
			}
			continue
		}
		p := updateBinary(c)
		if err := files.Check(p, false); err != nil {
			return v, err
		}
		var out string
		var err error
		if c == protocol.Syncthing {
			out, err = syncthingVersion()
		} else {
			out, err = system.RunRootOutputWithTimeout(10*time.Second, p, "--version")
		}
		if err != nil {
			return v, fmt.Errorf("read %s version: %w", c, err)
		}
		version := componentVersion(c, out)
		if version == "" {
			return v, fmt.Errorf("unrecognized %s version", c)
		}
		switch c {
		case protocol.Bitcoin:
			v.Bitcoin = version
		case protocol.LND:
			v.LND = version
		case protocol.Syncthing:
			v.Syncthing = version
		}
	}
	return v, nil
}

// componentVersion finds the version in a program's own version output. A
// version in the format this build knows is preferred, so known releases read
// as they always have. Otherwise the first word that looks like a version is
// returned: after a failed update this helper must still name a program that a
// later release installed, so that its repair release can be reviewed.
func componentVersion(c protocol.Component, out string) string {
	later := ""
	for _, word := range strings.Fields(out) {
		word = strings.TrimPrefix(word, "v")
		known := word
		if c == protocol.Bitcoin && strings.Count(word, ".") == 2 {
			known = strings.TrimSuffix(word, ".0")
		}
		if protocol.ValidVersion(c, known) {
			return known
		}
		if later == "" && strings.Contains(word, ".") && protocol.SafeVersion(word) {
			later = word
		}
	}
	return later
}

func permitPath(c protocol.Component) string {
	return paths.RuntimeDir + "/update-allow-" + string(c)
}
func updateDropIn(c protocol.Component) string {
	return "/etc/systemd/system/" + UpdateUnit(c) + ".d/90-vpn-update.conf"
}

// GuardUpdateServices requires a permit under /run, so a reboot closes the
// gate. Conditions are evaluated only at start; removing the permit leaves a
// healthy process up.
func GuardUpdateServices(components []protocol.Component) error {
	if err := files.Directory(paths.RuntimeDir, 0755); err != nil {
		return err
	}
	for _, c := range components {
		if UpdateUnit(c) == "" {
			return errors.New("unsupported managed service")
		}
		if err := files.Remove(permitPath(c)); err != nil {
			return err
		}
		p := updateDropIn(c)
		if err := files.Directory(filepath.Dir(p), 0755); err != nil {
			return err
		}
		body := "[Unit]\nConditionPathExists=" + permitPath(c) + "\n\n[Service]\nRestart=no\n"
		if err := files.Write(p, []byte(body), 0644); err != nil {
			return err
		}
	}
	return system.RunRoot("systemctl", "daemon-reload")
}

func ReleaseUpdateGuards(components []protocol.Component) error {
	for _, c := range components {
		if err := files.Remove(permitPath(c)); err != nil {
			return err
		}
		p := updateDropIn(c)
		if err := files.Remove(p); err != nil {
			return err
		}
		// Leave independently owned drop-ins untouched. The auto-unlock
		// implementation requires its directory to disappear when empty.
		entries, err := os.ReadDir(filepath.Dir(p))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && len(entries) == 0 {
			if err := os.Remove(filepath.Dir(p)); err != nil {
				return err
			}
			if err := files.Sync("/etc/systemd/system"); err != nil {
				return err
			}
		}
	}
	return system.RunRoot("systemctl", "daemon-reload")
}

func updateServiceProperties(c protocol.Component) (map[string]string, error) {
	out, err := system.RunRootOutputWithTimeout(10*time.Second, "systemctl", "show", UpdateUnit(c), "--property=MainPID,ControlPID,ActiveState,Result,Restart,NeedDaemonReload,DropInPaths")
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if ok {
			m[k] = v
		}
	}
	return m, nil
}

func StopUpdateService(c protocol.Component) error {
	if err := files.Remove(permitPath(c)); err != nil {
		return err
	}
	before, err := updateServiceProperties(c)
	if err != nil {
		return err
	}
	if before["MainPID"] == "0" && before["ControlPID"] == "0" && (before["ActiveState"] == "inactive" || before["ActiveState"] == "failed") {
		// A previous failed invocation has already exited. Clear its systemd
		// start limit only on this explicitly admitted update/retry path.
		return system.RunRoot("systemctl", "reset-failed", UpdateUnit(c))
	}
	if _, err := system.RunRootOutputWithTimeout(26*time.Minute, "systemctl", "stop", UpdateUnit(c)); err != nil {
		return fmt.Errorf("stop %s: %w", c, err)
	}
	p, err := updateServiceProperties(c)
	if err != nil {
		return err
	}
	if p["MainPID"] != "0" || p["ControlPID"] != "0" || p["ActiveState"] != "inactive" || p["Result"] != "success" {
		return fmt.Errorf("%s did not stop cleanly, no binaries were replaced", c)
	}
	return nil
}

func StartUpdateService(c protocol.Component) error {
	p, err := updateServiceProperties(c)
	if err != nil {
		return err
	}
	if p["Restart"] != "no" || p["NeedDaemonReload"] != "no" || !strings.Contains(p["DropInPaths"], updateDropIn(c)) {
		return errors.New("update service guard is not loaded")
	}
	if err := files.Write(permitPath(c), []byte("permitted\n"), 0600); err != nil {
		return err
	}
	defer files.Remove(permitPath(c))
	_, err = system.RunRootOutputWithTimeout(26*time.Minute, "systemctl", "start", UpdateUnit(c))
	return err
}

// CheckUpdateProcess verifies the running inode, not just systemd's active flag.
func CheckUpdateProcess(c protocol.Component) error {
	p, err := updateServiceProperties(c)
	if err != nil {
		return err
	}
	pid, err := strconv.Atoi(p["MainPID"])
	if err != nil || pid <= 0 || p["ActiveState"] != "active" {
		return fmt.Errorf("%s is not active", c)
	}
	live, err := os.Stat(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return err
	}
	installed, err := os.Stat(updateBinary(c))
	if err != nil {
		return err
	}
	if !os.SameFile(live, installed) {
		return fmt.Errorf("%s is running a different executable", c)
	}
	return nil
}

func ApplyUpdateHostStep(step string, cfg *config.AppConfig) error {
	switch step {
	case "lnd-service-v1":
		// The release opts into the current reviewed template. Configuration,
		// wallet passwords, data directories and user choices are not rewritten.
		if err := files.Write(paths.LNDService, []byte(LNDServiceUnit("lnd", cfg.AutoUnlock)), 0644); err != nil {
			return err
		}
		if err := validateInstalledLNDUnit(); err != nil {
			return err
		}
		return system.RunRoot("systemctl", "daemon-reload")
	default:
		return fmt.Errorf("unknown host update step %q", step)
	}
}
