package jobs

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/update"
)

// StartUnit starts a job's own systemd service through systemd-run, and
// returns once its program runs. The service has no file on disk and is
// removed when it ends, failed or not. Its sandbox is the one the root helper
// had (ADR-0016's lists amendment, decision 5), tightened later kind by kind.
// credential is a root-only file systemd copies into the job's private
// credential folder, or empty.
func StartUnit(id, title, credential string) error {
	args := []string{
		"--unit=" + UnitName(id), "--collect", "--quiet",
		"--description=vpn job: " + title,
		"--property=Type=exec",
		"--property=PrivateTmp=yes",
		"--property=ProtectHome=read-only",
		"--property=SyslogIdentifier=vpnd",
	}
	if credential != "" {
		args = append(args, "--property=LoadCredential="+credentialName+":"+credential)
	}
	args = append(args, "--", paths.BinaryPath, "run-job", id)
	// The error names only the cause. The command line holds nothing beyond
	// the job ID and fixed options, and the refusal reaches the owner's screen.
	out, err := exec.Command("systemd-run", args...).CombinedOutput()
	if err == nil {
		return nil
	}
	if msg := strings.TrimSpace(string(out)); msg != "" {
		return fmt.Errorf("systemd-run: %s", msg)
	}
	if e, ok := errors.AsType[*exec.Error](err); ok {
		err = e.Err
	}
	return fmt.Errorf("systemd-run: %w", err)
}

// unitAlive reports whether a job's service still runs. A service that ended
// is gone, since systemd-run removes it.
func unitAlive(id string) (bool, error) {
	out, err := exec.Command("systemctl", "show", "--property=ActiveState", "--value", UnitName(id)).Output()
	if err != nil {
		return false, fmt.Errorf("read %s: %w", UnitName(id), err)
	}
	switch state := strings.TrimSpace(string(out)); state {
	case "active", "activating", "deactivating", "reloading", "refreshing":
		return true, nil
	case "inactive", "failed":
		return false, nil
	default:
		return false, fmt.Errorf("%s is %q", UnitName(id), state)
	}
}

// unfinishedNodeUpdate reports an unfinished Node Update for the busy rule.
func unfinishedNodeUpdate() (bool, string, error) { return update.Unfinished() }
