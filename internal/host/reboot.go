package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// RequestReboot asks systemd to enqueue a reboot, independently of the terminal.
// Success confirms acceptance, not shutdown or a subsequent successful boot.
func RequestReboot() error {
	if os.Geteuid() != 0 {
		return errors.New("reboot requires root")
	}
	return requestReboot(15*time.Second, exec.CommandContext)
}

func requestReboot(timeout time.Duration, command func(context.Context, string, ...string) *exec.Cmd) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	// systemctl reboot returns after enqueueing. Killing this observer cannot
	// roll back a job already accepted by systemd.
	cmd := command(ctx, "systemctl", "reboot")
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return fmt.Errorf("reboot request was not confirmed: %w", err)
	}
	return nil
}
