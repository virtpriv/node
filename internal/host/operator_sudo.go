package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/virtpriv/node/internal/paths"
)

// Authentication uses the owner's password. Credential caching remains host
// policy: neither installation nor the running TUI writes a timeout override.
const operatorSudoPolicy = "# Virtual Private Node owner access\n" +
	"Defaults:vpn authenticate, !rootpw, !targetpw, !runaspw, !exempt_group\n" +
	"vpn ALL=(ALL:ALL) PASSWD: ALL\n"

// ConfigureOperatorSudo runs only during admitted installation, after password
// application. Conflicting policy is refused rather than overwritten on resume.
func ConfigureOperatorSudo() error {
	if os.Geteuid() != 0 {
		return errors.New("owner sudo provisioning requires root")
	}
	return provisionOperatorSudo(paths.AdminSudoers, sudoOutput, VerifyOperatorSudo)
}

func provisionOperatorSudo(path string, run func(string, ...string) (string, error), verify func() error) error {
	dir := filepath.Dir(path)
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("inspect sudoers directory: %w", err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok || st.Uid != 0 || info.Mode().Perm()&0022 != 0 {
		return errors.New("sudoers directory must be a root-owned directory without group or other write access")
	}
	if info, err := os.Lstat(path); err == nil {
		st, ok := info.Sys().(*syscall.Stat_t)
		if !info.Mode().IsRegular() || !ok || st.Uid != 0 || st.Gid != 0 || st.Nlink != 1 || info.Mode().Perm() != 0440 || info.Size() != int64(len(operatorSudoPolicy)) {
			return errors.New("existing owner sudo policy has unexpected metadata; inspect before resuming installation")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if string(data) != operatorSudoPolicy {
			return errors.New("existing owner sudo policy differs; inspect before resuming installation")
		}
		return verify()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := run("/usr/sbin/visudo", "-c"); err != nil {
		return fmt.Errorf("existing sudo policy is invalid: %w", err)
	}
	file, err := os.CreateTemp(dir, ".vpn-sudo-*.tmp")
	if err != nil {
		return err
	}
	temp := file.Name()
	defer os.Remove(temp)
	defer file.Close()
	if _, err := file.WriteString(operatorSudoPolicy); err != nil {
		return err
	}
	if err := file.Chown(0, 0); err != nil {
		return err
	}
	if err := file.Chmod(0440); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if _, err := run("/usr/sbin/visudo", "-cf", temp); err != nil {
		return fmt.Errorf("validate owner sudo rule: %w", err)
	}
	if err := os.Rename(temp, path); err != nil {
		return err
	}
	if err := verify(); err != nil {
		return errors.Join(fmt.Errorf("owner sudo policy not confirmed: %w", err), os.Remove(path))
	}
	parent, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer parent.Close()
	return parent.Sync()
}

// VerifyOperatorSudo inspects sudo's matched rules, including command defaults.
// It conservatively refuses conflicting authentication rules rather than trying
// to reproduce sudo's precedence logic or changing independent host policy.
// It does not authenticate a password or replace an actual owner login test.
func VerifyOperatorSudo() error {
	if os.Geteuid() != 0 {
		return errors.New("owner sudo verification requires root")
	}
	if _, err := sudoOutput("/usr/sbin/visudo", "-c"); err != nil {
		return fmt.Errorf("validate merged sudo policy: %w", err)
	}
	out, err := sudoOutput("/usr/bin/sudo", "-n", "-ll", "-U", paths.AdminUser)
	if err != nil {
		return fmt.Errorf("inspect owner sudo policy: %w", err)
	}
	return verifyOperatorSudoListing(out)
}

var unsafeSudoAuthentication = regexp.MustCompile(`(^|[\s,])(!authenticate|rootpw|targetpw|runaspw|exempt_group=[^,\s]+)([\s,]|$)`)

func verifyOperatorSudoListing(out string) error {
	if unsafeSudoAuthentication.MatchString(out) {
		return errors.New("host sudo policy contains conflicting authentication settings; inspect sudo -ll -U vpn")
	}
	grant := false
	for _, entry := range strings.Split(out, "Sudoers entry:")[1:] {
		users, groups, authenticate, all := false, false, false, false
		commands := false
		for _, line := range strings.Split(entry, "\n") {
			line = strings.TrimSpace(line)
			switch {
			case line == "RunAsUsers: ALL":
				users = true
			case line == "RunAsGroups: ALL":
				groups = true
			case strings.HasPrefix(line, "Options:"):
				for _, option := range strings.Split(strings.TrimPrefix(line, "Options:"), ",") {
					if strings.TrimSpace(option) == "authenticate" {
						authenticate = true
					}
				}
			case line == "Commands:":
				commands = true
			case commands && strings.HasPrefix(line, "!"):
				return errors.New("host sudo policy contains an owner command denial; inspect sudo -ll -U vpn")
			case commands && line == "ALL":
				all = true
			}
		}
		grant = grant || users && groups && authenticate && all
	}
	if !grant {
		return errors.New("password-required unrestricted owner sudo was not found in effective policy")
	}
	return nil
}

func sudoOutput(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		return "", err
	}
	return string(out), nil
}
