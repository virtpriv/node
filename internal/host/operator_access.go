package host

import (
	"errors"
	"fmt"
	"os"
	"os/user"

	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/system"
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
//
// Bash reads its login file without job control, so without set -m the TUI
// would share the login session's process group: ctrl+z would stop it with
// no shell able to bring it back. The color reset is for a TUI that ended
// without restoring the window, after a crash Bubble Tea did not see. It
// changes nothing when the colors are already back.
func ConfigureOperatorAutoLaunch() error {
	profile := `# Virtual Private Node opens when you log in over SSH.
if [ -n "$SSH_CONNECTION" ] && [ -t 0 ]; then
    # Run it as a job, so ctrl+z suspends it and fg brings it back.
    set -m
    ` + paths.BinaryPath + `
    # Give the window its own colors back if vpn stopped without doing it.
    printf '\033]110\a\033]111\a'
fi

# The bitcoin-cli and lncli shortcuts are in .bashrc.
[ -f ~/.bashrc ] && source ~/.bashrc
`
	if err := system.WriteFileRootOwned(paths.AdminBashProfile, []byte(profile), 0644, paths.AdminUser, paths.AdminUser); err != nil {
		return fmt.Errorf("write .bash_profile: %w", err)
	}
	return nil
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
