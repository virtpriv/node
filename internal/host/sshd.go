// Package host owns privileged operations on the local node.
package host

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"

	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/sshkeys"
	"github.com/virtpriv/node/internal/system"
)

func buildHardeningDropIn(passwordAuth string) string {
	return `# Virtual Private Node SSH policy
# Managed by VPN. Password authentication below applies only to the owner.
PermitRootLogin no
Match User root
    PermitRootLogin no
Match User vpn
    PubkeyAuthentication yes
    KbdInteractiveAuthentication no
    X11Forwarding no
    PasswordAuthentication ` + passwordAuth + `
Match all
`
}

// ApplySSHHardening validates syntax and effective policy before restarting.
// Both the installer and runtime use the same owner-scoped configuration.
func ApplySSHHardening(passwordAuth string) error {
	if os.Geteuid() != 0 {
		return errors.New("SSH configuration requires the root helper")
	}
	return applySSHHardening(passwordAuth, sshdOps{
		read:  func() ([]byte, error) { return os.ReadFile(paths.SSHDDropIn) },
		write: func(b []byte) error { return system.WriteFileRoot(paths.SSHDDropIn, b, 0644) },
		remove: func() error {
			err := os.Remove(paths.SSHDDropIn)
			if os.IsNotExist(err) {
				return nil
			}
			return err
		},
		validate: func() (string, error) { return validateSSHPolicy(passwordAuth) },
		restart:  restartSSHD,
	})
}

type sshdOps struct {
	read     func() ([]byte, error)
	write    func([]byte) error
	remove   func() error
	validate func() (string, error)
	restart  func() error
}

// Both installation and runtime changes validate before restarting and restore
// the previous file on validation failure. Restart failure leaves state uncertain.
func applySSHHardening(passwordAuth string, ops sshdOps) error {
	if passwordAuth != "yes" && passwordAuth != "no" {
		return errors.New("invalid password authentication setting")
	}
	prev, err := ops.read()
	existed := !os.IsNotExist(err)
	if err != nil && existed {
		return fmt.Errorf("read current sshd drop-in: %w", err)
	}
	if err = ops.write([]byte(buildHardeningDropIn(passwordAuth))); err != nil {
		return fmt.Errorf("write sshd drop-in: %w", err)
	}
	if out, err := ops.validate(); err != nil {
		var restoreErr error
		if existed {
			restoreErr = ops.write(prev)
		} else {
			restoreErr = ops.remove()
		}
		if restoreErr != nil {
			return fmt.Errorf("sshd rejected the config (%s); restoring %s failed (%v); sshd was not restarted; inspect the file before restarting", strings.TrimSpace(out), paths.SSHDDropIn, restoreErr)
		}
		return fmt.Errorf("sshd rejected the config; previous drop-in restored; sshd not restarted: %s", strings.TrimSpace(out))
	}
	return ops.restart()
}

// RebuildSSHHardeningConfig checks fresh, structurally valid keys while holding
// the same lock as application edits. This cannot prove successful key login.
func RebuildSSHHardeningConfig(disabled bool) error {
	if os.Geteuid() != 0 {
		return errors.New("SSH configuration requires the root helper")
	}
	store := sshkeys.Store{Path: paths.AuthorizedKeysFile}
	return rebuildSSHConfig(disabled, store, ApplySSHHardening, EffectiveSSHPasswordAuth)
}

func rebuildSSHConfig(disabled bool, store sshkeys.Store, apply func(string) error, observe func() (bool, error)) error {
	change := func() error {
		auth := "yes"
		if disabled {
			data, err := store.Read()
			if err != nil {
				return err
			}
			if len(sshkeys.Keys(data)) == 0 {
				return errors.New("refusing to disable password authentication with no supported SSH keys; add and test a key first")
			}
			auth = "no"
		}
		if err := apply(auth); err != nil {
			return err
		}
		enabled, err := observe()
		if err != nil {
			return fmt.Errorf("verify effective SSH password authentication: %w", err)
		}
		if enabled == disabled {
			return errors.New("effective SSH password authentication does not match the requested setting")
		}
		return nil
	}
	if disabled {
		return store.WithLock(change)
	}
	return change()
}

// EffectiveSSHPasswordAuth samples sshd's resolved configuration for the operator
// at localhost. Address-specific Match rules can differ for an actual connection;
// this observation is not proof that a remote password login will succeed.
func EffectiveSSHPasswordAuth() (bool, error) {
	if os.Geteuid() != 0 {
		return false, errors.New("SSH configuration requires the root helper")
	}
	out, err := system.RunRootOutput("sshd", "-T", "-C", "user="+paths.AdminUser+",host=localhost,addr=127.0.0.1")
	if err != nil {
		return false, fmt.Errorf("query effective sshd config: %w", err)
	}
	return parsePasswordAuth(out)
}

func parsePasswordAuth(output string) (bool, error) {
	found, enabled := false, false
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || !strings.EqualFold(fields[0], "passwordauthentication") {
			continue
		}
		if found || len(fields) != 2 || (!strings.EqualFold(fields[1], "yes") && !strings.EqualFold(fields[1], "no")) {
			return false, errors.New("invalid passwordauthentication in sshd output")
		}
		found = true
		enabled = strings.EqualFold(fields[1], "yes")
	}
	if !found {
		return false, errors.New("passwordauthentication not present in sshd output")
	}
	return enabled, nil
}

func restartSSHD() error {
	if err := system.RunRoot("systemctl", "restart", "sshd"); err == nil {
		return nil
	}
	return system.RunRoot("systemctl", "restart", "ssh")
}

// Check localhost and, when inherited from SSH, the current remote connection.
// These samples detect conflicting Match rules; only a new login proves access.
func validateSSHPolicy(passwordAuth string) (string, error) {
	out, err := system.RunRootCombinedOutput("sshd", "-t")
	if err != nil {
		return out, err
	}
	contexts := []string{"host=localhost,addr=127.0.0.1"}
	if connection := os.Getenv("SSH_CONNECTION"); connection != "" {
		fields := strings.Fields(connection)
		if len(fields) != 4 {
			return "invalid SSH_CONNECTION; cannot verify connection policy", errors.New("invalid SSH connection")
		}
		remote, e1 := netip.ParseAddr(fields[0])
		local, e2 := netip.ParseAddr(fields[2])
		port, e3 := strconv.Atoi(fields[3])
		if e1 != nil || e2 != nil || e3 != nil || port < 1 || port > 65535 {
			return "invalid SSH_CONNECTION; cannot verify connection policy", errors.New("invalid SSH connection")
		}
		contexts = append(contexts, "host="+remote.String()+",addr="+remote.String()+",laddr="+local.String()+",lport="+fields[3])
	}
	for _, connection := range contexts {
		owner, err := system.RunRootOutput("sshd", "-T", "-C", "user="+paths.AdminUser+","+connection)
		if err != nil {
			return err.Error(), err
		}
		root, err := system.RunRootOutput("sshd", "-T", "-C", "user=root,"+connection)
		if err != nil {
			return err.Error(), err
		}
		if err := verifySSHPolicy(passwordAuth, owner, root); err != nil {
			return err.Error(), err
		}
	}
	return "", nil
}

func verifySSHPolicy(passwordAuth, owner, root string) error {
	enabled, err := parsePasswordAuth(owner)
	if err != nil {
		return err
	}
	if enabled != (passwordAuth == "yes") {
		return errors.New("effective owner password authentication conflicts with requested policy")
	}
	value := func(out, key string) string {
		for _, line := range strings.Split(out, "\n") {
			fields := strings.Fields(line)
			if len(fields) > 1 && fields[0] == key {
				return strings.Join(fields[1:], " ")
			}
		}
		return ""
	}
	if value(root, "permitrootlogin") != "no" {
		return errors.New("effective SSH policy still permits root login")
	}
	if value(owner, "pubkeyauthentication") != "yes" || value(owner, "permittty") != "yes" || value(owner, "forcecommand") != "none" || value(owner, "chrootdirectory") != "none" {
		return errors.New("effective SSH policy does not permit the ordinary owner console")
	}
	if enabled && value(owner, "authenticationmethods") != "any" {
		return errors.New("effective SSH authentication methods do not permit password-only owner login")
	}
	return nil
}

// VerifyInitialOwnerSSH rechecks the installation postcondition on completion,
// including resumes whose SSH step was already recorded. It never rewrites policy.
func VerifyInitialOwnerSSH() error {
	_, err := validateSSHPolicy("yes")
	return err
}
