// internal/system/exec.go

package system

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	err := requireRoot("write " + path)
	if err == nil {
		err = writeFile(path, content, perm)
	}
	if err != nil {
		logger.System("%v", err)
	}
	return err
}

// writeFile writes a private temporary file with an unused random name in the
// target's folder, sets the final mode, saves it to disk, renames it over the
// target and then saves the folder. The rename replaces a link at the target
// instead of writing through it. If a step before the rename fails, the target
// is left as it was and the temporary file is removed. A failure to save the
// folder is returned after the target has been replaced.
func writeFile(path string, content []byte, perm os.FileMode) error {
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

// Download fetches a URL to a local path using torsocks if available.
// It does not require Tor.
func Download(url, dest string) error {
	return doDownload(url, dest, false)
}

// DownloadRequireTor fetches a URL and fails if torsocks is unavailable.
// It makes up to three attempts, with two-second gaps. Tool-specific limits
// apply within each attempt. The wget path has no overall deadline.
func DownloadRequireTor(url, dest string) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		lastErr = doDownload(url, dest, true)
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
// Wget has 60-second DNS, connect and read-idle timeouts and three tries.
// These do not bound the whole transfer. Curl has a 60-second connect timeout
// and an 1800-second transfer limit. Helper socket deadlines do not bound
// this synchronous subprocess work.
func doDownload(url, dest string, requireTor bool) error {
	wrapper := torWrapper()
	if requireTor && wrapper == "" {
		return fmt.Errorf("torsocks not available, cannot download over Tor")
	}
	if _, err := exec.LookPath("wget"); err == nil {
		wgetArgs := []string{"--timeout=60", "--tries=3",
			"-q", "-O", dest, url}
		if wrapper != "" {
			return Run(wrapper, append([]string{"wget"},
				wgetArgs...)...)
		}
		return Run("wget", wgetArgs...)
	}
	curlArgs := []string{"-sL", "--connect-timeout", "60",
		"--max-time", "1800", "-o", dest, url}
	if wrapper != "" {
		return Run(wrapper, append([]string{"curl"},
			curlArgs...)...)
	}
	return Run("curl", curlArgs...)
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
