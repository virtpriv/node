package system

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// inhibitStartLimit bounds how long systemd-inhibit may take to take its lock.
const inhibitStartLimit = 30 * time.Second

// ShutdownInhibitor holds a systemd block inhibitor for shutdown, so a
// reboot, Debian's nightly one among them, is refused while vpn is busy.
type ShutdownInhibitor struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	once  sync.Once
	err   error
}

// InhibitShutdown takes the inhibitor through systemd-inhibit, which holds
// it while the program it runs has not ended. That program is cat, reading a
// pipe only this process writes to. Release closes the pipe. When this
// process ends any other way, the system closes it, cat ends and the lock
// goes with it. InhibitShutdown returns once cat echoes a byte back, which
// cat can do only after systemd-inhibit took the lock.
func InhibitShutdown(why string) (*ShutdownInhibitor, error) {
	cmd := exec.Command("systemd-inhibit", "--what=shutdown", "--mode=block", "--who=vpn", "--why="+why, "cat")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.WaitDelay = commandWaitDelay
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("systemd-inhibit: %w", err)
	}
	inh := &ShutdownInhibitor{cmd: cmd, stdin: stdin}

	echoed := make(chan error, 1)
	go func() {
		b := make([]byte, 1)
		_, err := io.ReadFull(stdout, b)
		echoed <- err
	}()
	_, err = stdin.Write([]byte{'\n'})
	if err == nil {
		select {
		case err = <-echoed:
		case <-time.After(inhibitStartLimit):
			err = errors.New("no answer")
		}
	}
	if err != nil {
		cmd.Process.Kill()
		inh.Release()
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("systemd-inhibit: %s", msg)
		}
		return nil, fmt.Errorf("systemd-inhibit: %w", err)
	}
	return inh, nil
}

// Release ends the inhibitor and returns once it is gone. A second call
// does nothing.
func (i *ShutdownInhibitor) Release() error {
	i.once.Do(func() {
		i.stdin.Close()
		err := i.cmd.Wait()
		if _, exited := errors.AsType[*exec.ExitError](err); err != nil && !exited {
			i.err = fmt.Errorf("systemd-inhibit: %w", err)
		}
	})
	return i.err
}
