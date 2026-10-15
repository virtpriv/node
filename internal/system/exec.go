// internal/system/exec.go

package system

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/virtpriv/node/internal/logger"
)

// Run executes a command and returns an error with output on failure.
func Run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s %s: %s: %s", name, strings.Join(args, " "), err, output)
	}
	return nil
}

// requireRoot checks the process's effective UID. It never elevates privileges.
// Root installer, helper and credential-staging commands can use these wrappers.
// Unprivileged callers must use the helper's fixed operations for root work.
func requireRoot(name string) error {
	if os.Geteuid() == 0 {
		return nil
	}
	return fmt.Errorf(
		"%s requires root: this operation must go through "+
			"the node's root helper (vpn helperd), not run "+
			"directly. This is a bug worth reporting", name)
}

// RunRoot requires the process to be root, then executes the command directly.
func RunRoot(name string, args ...string) error {
	if err := requireRoot(name); err != nil {
		return err
	}
	return Run(name, args...)
}

// RunOutput returns trimmed stdout on success and discards output on error.
func RunOutput(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Stderr = nil
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(output)), nil
}

// RunRootOutput requires root and returns trimmed stdout on success.
// Output is discarded on error.
func RunRootOutput(name string, args ...string) (string, error) {
	if err := requireRoot(name); err != nil {
		return "", err
	}
	return RunOutput(name, args...)
}

// commandWaitDelay bounds how long a command's output is still read after
// its timeout, or after it exits, when a child process it left behind keeps
// the output open.
const commandWaitDelay = 2 * time.Second

// RunOutputWithTimeout returns trimmed stdout on success using its own timeout.
// It does not accept a caller context. Output is discarded on error.
// Expiry kills the command. A child process that still holds the output then
// delays the return by at most commandWaitDelay. A command that exits but
// leaves a child holding its output or error stream for longer than that
// fails, and its output is discarded.
func RunOutputWithTimeout(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stderr = nil
	cmd.WaitDelay = commandWaitDelay
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(output)), nil
}

// RunRootOutputWithTimeout requires root and uses RunOutputWithTimeout's
// output and timeout behavior, including its bounded wait for output.
func RunRootOutputWithTimeout(timeout time.Duration, name string, args ...string) (string, error) {
	if err := requireRoot(name); err != nil {
		return "", err
	}
	return RunOutputWithTimeout(timeout, name, args...)
}

// RunCombinedOutput executes a command and returns combined stdout+stderr.
// Used for GPG commands which write status to stderr.
func RunCombinedOutput(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(output)), nil
}

// RunRootCombinedOutput requires root and returns combined stdout and stderr,
// trimmed on success and untrimmed on command failure.
func RunRootCombinedOutput(name string, args ...string) (string, error) {
	if err := requireRoot(name); err != nil {
		return "", err
	}
	return RunCombinedOutput(name, args...)
}

// RunSilent executes a command and discards all output.
func RunSilent(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}

// RunRootSilent requires root and executes a command, discarding all output.
func RunRootSilent(name string, args ...string) error {
	if err := requireRoot(name); err != nil {
		return err
	}
	return RunSilent(name, args...)
}

// WriteFileRoot requires root and replaces path with content and mode perm,
// so that a power cut leaves either the old file or the complete new one.
// The new file is root owned. Callers must ensure the destination is suitable.
// Failures are logged centrally.
func WriteFileRoot(path string, content []byte, perm os.FileMode) error {
	return writeFileRootLogged(path, func() error {
		return writeFile(path, content, perm, keepOwner, keepOwner)
	})
}

// WriteFileRootOwned is WriteFileRoot for a file a service account reads. The
// file reaches its name already owned by owner and group, so the service
// never finds a new file it cannot read. If the owner or the group cannot be
// found, or the owner cannot be set, the old file is left as it was.
func WriteFileRootOwned(path string, content []byte, perm os.FileMode, owner, group string) error {
	return writeFileRootLogged(path, func() error {
		uid, gid, err := lookupOwner(owner, group)
		if err != nil {
			return fmt.Errorf("write %s: look up owner: %w", path, err)
		}
		return writeFile(path, content, perm, uid, gid)
	})
}

func writeFileRootLogged(path string, write func() error) error {
	err := requireRoot("write " + path)
	if err == nil {
		err = write()
	}
	if err != nil {
		logger.System("%v", err)
	}
	return err
}

// lookupOwner returns the numeric ids of an account and a group.
func lookupOwner(owner, group string) (uid, gid int, err error) {
	u, err := user.Lookup(owner)
	if err != nil {
		return 0, 0, err
	}
	g, err := user.LookupGroup(group)
	if err != nil {
		return 0, 0, err
	}
	if uid, err = strconv.Atoi(u.Uid); err != nil {
		return 0, 0, fmt.Errorf("account %s has id %q", owner, u.Uid)
	}
	if gid, err = strconv.Atoi(g.Gid); err != nil {
		return 0, 0, fmt.Errorf("group %s has id %q", group, g.Gid)
	}
	return uid, gid, nil
}

// keepOwner leaves the owner or the group as the new file gets it.
const keepOwner = -1

// writeFile writes a private temporary file with an unused random name in the
// target's folder, sets the owner and then the final mode, saves it to disk,
// renames it over the target and then saves the folder. The owner is set on
// the open file, which never follows a link. The rename replaces a link at the
// target instead of writing through it. If a step before the rename fails, the
// target is left as it was and the temporary file is removed. A failure to
// save the folder is returned after the target has been replaced.
func writeFile(path string, content []byte, perm os.FileMode, uid, gid int) error {
	fail := func(step string, err error) error {
		return fmt.Errorf("write %s: %s: %w", path, step, err)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return fail("create temporary", err)
	}
	tmpPath := tmp.Name()
	published := false
	defer func() {
		if !published {
			tmp.Close()
			os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(content); err != nil {
		return fail("write temporary", err)
	}
	// Before the mode, since changing the owner can clear mode bits.
	if uid != keepOwner || gid != keepOwner {
		if err := tmp.Chown(uid, gid); err != nil {
			return fail("set owner", err)
		}
	}
	if err := tmp.Chmod(perm); err != nil {
		return fail("set mode", err)
	}
	if err := tmp.Sync(); err != nil {
		return fail("sync temporary", err)
	}
	if err := tmp.Close(); err != nil {
		return fail("close temporary", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fail("replace", err)
	}
	published = true

	folder, err := os.Open(dir)
	if err != nil {
		return fail("open folder", err)
	}
	defer folder.Close()
	if err := folder.Sync(); err != nil {
		return fail("sync folder", err)
	}
	return nil
}

// DownloadRequireTor fetches a URL and fails if torsocks is unavailable.
// It makes up to three attempts, with two-second gaps. Tool-specific limits
// apply within each attempt. The wget path has no overall deadline.
func DownloadRequireTor(url, dest string) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		lastErr = doDownload(url, dest)
		if lastErr == nil {
			return nil
		}
		if attempt < 2 {
			time.Sleep(2 * time.Second)
		}
	}
	return lastErr
}

// doDownload runs one fetch using wget when available, otherwise curl.
// It always runs through torsocks and refuses to start without it.
// Wget has 60-second DNS, connect and read-idle timeouts and three tries.
// These do not bound the whole transfer. Curl has a 60-second connect timeout
// and an 1800-second transfer limit. Helper socket deadlines do not bound
// this synchronous subprocess work.
func doDownload(url, dest string) error {
	wrapper := torWrapper()
	if wrapper == "" {
		return fmt.Errorf("torsocks not available, cannot download over Tor")
	}
	if _, err := exec.LookPath("wget"); err == nil {
		return Run(wrapper, "wget", "--timeout=60", "--tries=3",
			"-q", "-O", dest, url)
	}
	return Run(wrapper, "curl", "-sL", "--connect-timeout", "60",
		"--max-time", "1800", "-o", dest, url)
}

// torWrapper returns "torsocks" if available, empty string otherwise.
func torWrapper() string {
	if _, err := exec.LookPath("torsocks"); err == nil {
		return "torsocks"
	}
	return ""
}

// ReadFileRoot requires the process to be root and returns os.ReadFile's result,
// preserving errors for os.IsNotExist checks. Ordinary unprivileged TUI reads use
// staged files. Separate helper operations can refresh privileged evidence.
func ReadFileRoot(path string) ([]byte, error) {
	if os.Geteuid() == 0 {
		return os.ReadFile(path)
	}
	return nil, requireRoot("read " + path)
}
