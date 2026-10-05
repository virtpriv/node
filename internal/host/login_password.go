package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/virtpriv/node/internal/loginpassword"
	"github.com/virtpriv/node/internal/paths"
)

// SetLoginPassword provisions the initial password during root installation.
// It has no helper endpoint. Runtime changes use passwd as the owner.
// An interrupted process may already have changed the password.
func SetLoginPassword(password loginpassword.Password) error {
	if os.Geteuid() != 0 {
		return errors.New("initial password provisioning requires root")
	}
	return runLoginPassword(password, 45*time.Second, func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, "/usr/sbin/chpasswd")
	})
}

func runLoginPassword(password loginpassword.Password, timeout time.Duration, command func(context.Context) *exec.Cmd) error {
	if password.Text() == "" {
		return errors.New("password was not validated")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := command(ctx)
	cmd.Stdin = strings.NewReader(paths.AdminUser + ":" + password.Text() + "\n")
	// Do not return tool output: PAM modules may include sensitive input in it.
	// WaitDelay also bounds pipes retained by a descendant after the tool exits.
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("login password change was not confirmed: %w", ctx.Err())
		}
		return fmt.Errorf("login password change was not confirmed: %w", err)
	}
	return nil
}
