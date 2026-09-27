package host

import (
	"errors"
	"fmt"
	"os"
	"os/user"

	"github.com/virtualprivatenode/vpn/internal/paths"
	"github.com/virtualprivatenode/vpn/internal/system"
)

// CreateOperatorAccount creates the owner during an admitted base installation.
// SSH keys are added explicitly by the owner after installation.
func CreateOperatorAccount() error {
	if os.Geteuid() != 0 {
		return errors.New("initial operator access requires root")
	}
	return ensureOperatorAccount(user.Lookup, system.RunRoot)
}

// Keep lookup and creation together: failure to observe an account is not proof
// of absence. The narrow dependencies let refusal tests detect creation attempts.
func ensureOperatorAccount(lookup func(string) (*user.User, error), run func(string, ...string) error) error {
	if _, err := lookup(paths.AdminUser); err == nil {
		return nil
	} else {
		var unknown user.UnknownUserError
		if !errors.As(err, &unknown) {
			return fmt.Errorf("look up operator account %s: %w", paths.AdminUser, err)
		}
	}
	if err := run("adduser", "--disabled-password", "--gecos", "Virtual Private Node", paths.AdminUser); err != nil {
		return fmt.Errorf("create admin user: %w", err)
	}
	return nil
}

// ConfigureOperatorAutoLaunch opens the node TUI for interactive SSH logins.
// Installer calls it after recording successful password application.
func ConfigureOperatorAutoLaunch() error {
	profile := `# Virtual Private Node — auto-launch
if [ -n "$SSH_CONNECTION" ] && [ -t 0 ]; then
    ` + paths.BinaryPath + `
fi

# Source .bashrc after the TUI exits (cli wrappers live there)
[ -f ~/.bashrc ] && source ~/.bashrc
`
	if err := system.WriteFileRoot(paths.AdminBashProfile, []byte(profile), 0644); err != nil {
		return fmt.Errorf("write .bash_profile: %w", err)
	}
	return system.RunRoot("chown", paths.AdminUser+":"+paths.AdminUser, paths.AdminBashProfile)
}

// SetOperatorLogOwnership gives the operator its application log. System
// configuration and its parent remain root-owned and are never made writable.
func SetOperatorLogOwnership() error {
	owner := paths.AdminUser + ":" + paths.AdminUser
	if err := system.RunRoot("chown", owner, paths.LogFile); err != nil {
		return fmt.Errorf("chown %s to %s: %w", paths.LogFile, owner, err)
	}
	return nil
}
