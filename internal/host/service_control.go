package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/virtpriv/node/internal/servicecontrol"
	"github.com/virtpriv/node/internal/system"
)

// ControlService completes independently of the requesting terminal. The bound
// covers LND's five-minute stop and twenty-minute notified start. Terminating
// systemctl observation cannot roll back a job already accepted by systemd.
func ControlService(request servicecontrol.Request) (servicecontrol.Completion, error) {
	if os.Geteuid() != 0 {
		return servicecontrol.Completion{}, errors.New("service control requires the root helper")
	}
	return controlService(request, 26*time.Minute, exec.CommandContext, system.ReadServiceState)
}

func controlService(request servicecontrol.Request, timeout time.Duration,
	command func(context.Context, string, ...string) *exec.Cmd,
	state func(context.Context, string) (string, error),
) (servicecontrol.Completion, error) {
	if request.Unit() == "" {
		return servicecontrol.Completion{}, errors.New("service request was not validated")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := command(ctx, "systemctl", request.Action(), request.Unit())
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return servicecontrol.Completion{}, fmt.Errorf("%s %s was not confirmed: %w", request.Action(), request.Unit(), err)
	}
	activeState, err := state(ctx, request.Unit())
	if err != nil {
		return servicecontrol.Completion{}, fmt.Errorf("read %s after %s: %w", request.Unit(), request.Action(), err)
	}
	completion := servicecontrol.Completion{Unit: request.Unit(), Action: request.Action(), State: activeState}
	return completion, request.Verify(completion)
}
