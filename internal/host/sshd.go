// Package host owns privileged operations on the local node.
package host

import (
	"errors"
	"fmt"
	"io/fs"
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

// ErrSSHSettingNotConfirmed marks a failure after the new SSH setting may
// have been written: sshd may already use it, or will at its next start.
var ErrSSHSettingNotConfirmed = errors.New("the new SSH setting may be written but sshd did not confirm it")

// notConfirmed adds ErrSSHSettingNotConfirmed to err and keeps its words, so
// the screens that show it read as before.
type notConfirmed struct{ err error }

func (e notConfirmed) Error() string   { return e.err.Error() }
func (e notConfirmed) Unwrap() []error { return []error{e.err, ErrSSHSettingNotConfirmed} }

// ApplySSHHardening validates syntax and effective policy before restarting.
// Both the installer and runtime use the same owner-scoped configuration.
func ApplySSHHardening(passwordAuth string) error {
	if os.Geteuid() != 0 {
		return errors.New("SSH configuration requires root")
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
	// The writer can fail after the new file took the old one's place.
	if err = ops.write([]byte(buildHardeningDropIn(passwordAuth))); err != nil {
		return notConfirmed{fmt.Errorf("write sshd drop-in: %w", err)}
	}
	if out, err := ops.validate(); err != nil {
		var restoreErr error
		if existed {
			restoreErr = ops.write(prev)
		} else {
			restoreErr = ops.remove()
		}
		if restoreErr != nil {
			undo := "putting " + paths.SSHDDropIn + " back"
			if !existed {
				undo = "removing " + paths.SSHDDropIn
			}
			return notConfirmed{fmt.Errorf("sshd did not confirm vpn's SSH settings (%s), and %s failed (%v). sshd was not restarted. Inspect the file before restarting sshd", strings.TrimSpace(out), undo, restoreErr)}
		}
		if existed {
			return fmt.Errorf("sshd did not confirm vpn's SSH settings, so the earlier file is back and sshd was not restarted: %s", strings.TrimSpace(out))
		}
		return fmt.Errorf("sshd did not confirm vpn's SSH settings, so vpn's file is removed and sshd was not restarted: %s", strings.TrimSpace(out))
	}
	if err := ops.restart(); err != nil {
		return notConfirmed{err}
	}
	return nil
}

// RebuildSSHHardeningConfig checks fresh, structurally valid keys while holding
// the same lock as application edits. This cannot prove successful key login.
func RebuildSSHHardeningConfig(disabled bool) error {
	if os.Geteuid() != 0 {
		return errors.New("SSH configuration requires root")
	}
	store := sshkeys.Store{Path: paths.AuthorizedKeysFile}
	return rebuildSSHConfig(disabled, store, ApplySSHHardening, EffectiveSSHPasswordAuth)
}

// errNoKeys refuses to turn password login off with no key to log in with.
var errNoKeys = errors.New("refusing to disable password authentication with no supported SSH keys, add and test a key first")

// A refusal before the write, or one where vpn's file was put back and sshd
// was not restarted, changed nothing, and the screen may say so.
func rebuildSSHConfig(disabled bool, store sshkeys.Store, apply func(string) error, observe func() (bool, error)) error {
	change := func() error {
		auth := "yes"
		if disabled {
			data, err := store.Read()
			if err != nil {
				return nothingChanged(err)
			}
			if len(sshkeys.Keys(data)) == 0 {
				return nothingChanged(errNoKeys)
			}
			auth = "no"
		}
		if err := apply(auth); err != nil {
			if errors.Is(err, ErrSSHSettingNotConfirmed) {
				return err
			}
			return nothingChanged(err)
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
		ran := false
		err := store.WithLock(func() error { ran = true; return change() })
		switch {
		case ran:
			return err
		case errors.Is(err, fs.ErrNotExist):
			// A fresh install has no ~/.ssh until the owner adds a key.
			return nothingChanged(errNoKeys)
		default:
			// The lock failed, so the change never ran.
			return nothingChanged(err)
		}
	}
	return change()
}

// EffectiveSSHPasswordAuth samples sshd's resolved configuration for the operator
// at localhost. Address-specific Match rules can differ for an actual connection,
// so this observation is not proof that a remote password login will succeed.
func EffectiveSSHPasswordAuth() (bool, error) {
	if os.Geteuid() != 0 {
		return false, errors.New("SSH configuration requires root")
	}
	out, err := askTool(questionTimeout, "sshd", "-T", "-C", "user="+paths.AdminUser+",host=localhost,addr=127.0.0.1")
	if err != nil {
		return false, fmt.Errorf("query effective sshd config: %w", err)
	}
	return ownerPasswordLogin(out)
}

// ownerPasswordLogin reports whether the password logs in. Debian's
// keyboard-interactive login asks PAM for the same password, so it counts.
// Without PAM it has nothing to ask on Linux. A second factor set up in PAM
// would make it untrue, and vpn does not read PAM.
func ownerPasswordLogin(output string) (bool, error) {
	password, err := parsePasswordAuth(output)
	if err != nil {
		return false, err
	}
	switch sshdValue(output, "kbdinteractiveauthentication") {
	case "yes":
		return password || sshdValue(output, "usepam") == "yes", nil
	case "no":
		return password, nil
	}
	return false, errors.New("kbdinteractiveauthentication not present in sshd output")
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

// sshContexts are the logins sshd is asked about: one from the machine
// itself and, when the caller came in over SSH, the owner's own connection.
// These samples detect conflicting Match rules. Only a new login proves access.
func sshContexts() ([]string, error) {
	contexts := []string{"host=localhost,addr=127.0.0.1"}
	connection := os.Getenv("SSH_CONNECTION")
	if connection == "" {
		return contexts, nil
	}
	fields := strings.Fields(connection)
	if len(fields) != 4 {
		return nil, errors.New("invalid SSH_CONNECTION, cannot verify connection policy")
	}
	remote, e1 := netip.ParseAddr(fields[0])
	local, e2 := netip.ParseAddr(fields[2])
	port, e3 := strconv.Atoi(fields[3])
	if e1 != nil || e2 != nil || e3 != nil || port < 1 || port > 65535 {
		return nil, errors.New("invalid SSH_CONNECTION, cannot verify connection policy")
	}
	return append(contexts, "host="+remote.String()+",addr="+remote.String()+",laddr="+local.String()+",lport="+fields[3]), nil
}

// CheckSSHLetsOwnerIn is the check "sshd lets the vpn account log in". It
// asks sshd for the vpn account, before the account or vpn's file exists, for
// the settings vpn's file does not set. sshd answers for a name it does not
// know, but a Match on a group the account will join shows only once it is
// there, so step 14 asks again.
func CheckSSHLetsOwnerIn() error {
	contexts, err := sshContexts()
	if err != nil {
		return err
	}
	for _, connection := range contexts {
		owner, err := askTool(questionTimeout, "sshd", "-T", "-C", "user="+paths.AdminUser+","+connection)
		if err != nil {
			return fmt.Errorf("cannot read sshd's settings: %w", err)
		}
		if err := ownerWayIn(owner, true); err != nil {
			return err
		}
	}
	return nil
}

// validateSSHPolicy asks sshd whether vpn's SSH settings hold.
func validateSSHPolicy(passwordAuth string) (string, error) {
	if _, err := askTool(questionTimeout, "sshd", "-t"); err != nil {
		return err.Error(), err
	}
	if err := CheckSSHReadsVPNFirst(); err != nil {
		return err.Error(), err
	}
	contexts, err := sshContexts()
	if err != nil {
		return err.Error(), err
	}
	for _, connection := range contexts {
		owner, err := askTool(questionTimeout, "sshd", "-T", "-C", "user="+paths.AdminUser+","+connection)
		if err != nil {
			return err.Error(), err
		}
		root, err := askTool(questionTimeout, "sshd", "-T", "-C", "user=root,"+connection)
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
	// vpn turns both off for its account in every case, and the password
	// switch covers neither.
	if sshdValue(owner, "kbdinteractiveauthentication") != "no" {
		return errors.New("sshd would turn on keyboard-interactive login for the vpn account, which vpn turns off")
	}
	if sshdValue(owner, "x11forwarding") != "no" {
		return errors.New("sshd would forward X11 for the vpn account, which vpn turns off")
	}
	if sshdValue(root, "permitrootlogin") != "no" {
		return errors.New("effective SSH policy still permits root login")
	}
	if sshdValue(owner, "pubkeyauthentication") != "yes" {
		return errors.New("sshd would turn off key login for the vpn account, which vpn turns on")
	}
	return ownerWayIn(owner, enabled)
}

// ownerWayIn checks the settings of sshd's answer for the vpn account that
// vpn's file does not set, so any file of the machine decides them. Each
// finding names the setting and ends with its fix.
func ownerWayIn(owner string, password bool) error {
	const fix = "Remove the line from the file in /etc/ssh that holds it"
	var f findings
	if v := sshdValue(owner, "permittty"); v != "yes" {
		f.add("found PermitTTY %s in sshd's settings for the vpn account, which takes away its terminal. %s", v, fix)
	}
	if v := sshdValue(owner, "forcecommand"); v != "none" {
		f.add("found ForceCommand %s in sshd's settings for the vpn account, which runs that command instead of a shell. %s", v, fix)
	}
	if v := sshdValue(owner, "chrootdirectory"); v != "none" {
		f.add("found ChrootDirectory %s in sshd's settings for the vpn account, which shuts it in that folder. %s", v, fix)
	}
	if v := sshdValue(owner, "authenticationmethods"); password && v != "any" {
		f.add("found AuthenticationMethods %s in sshd's settings for the vpn account, which would refuse its password login. %s", v, fix)
	}
	// sshd's answer prints a list of accounts but not whether it lets vpn in.
	for _, list := range []struct{ key, name string }{
		{"allowusers", "AllowUsers"}, {"denyusers", "DenyUsers"}, {"allowgroups", "AllowGroups"}, {"denygroups", "DenyGroups"},
	} {
		if names := sshdValues(owner, list.key); len(names) > 0 {
			f.add("found %s %s in sshd's settings. vpn refuses any list of accounts in sshd's settings, since sshd's answer does not say whom it lets in. %s", list.name, strings.Join(names, " "), fix)
		}
	}
	if len(f) == 0 {
		return nil
	}
	return errors.New(strings.Join(f, "\n\n"))
}

// sshdValue is the first value sshd -T prints for key, or "".
func sshdValue(output, key string) string {
	if values := sshdValues(output, key); len(values) > 0 {
		return values[0]
	}
	return ""
}

// sshdValues is every value sshd -T prints for key, one line each.
func sshdValues(output, key string) []string {
	var values []string
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 1 && fields[0] == key {
			values = append(values, strings.Join(fields[1:], " "))
		}
	}
	return values
}

// VerifyInitialOwnerSSH rechecks the installation postcondition on completion,
// including resumes whose SSH step was already recorded. It never rewrites policy.
func VerifyInitialOwnerSSH() error {
	_, err := validateSSHPolicy("yes")
	return err
}
