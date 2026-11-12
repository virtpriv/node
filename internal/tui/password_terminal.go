package tui

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
)

type passwordExecution struct {
	started, changed bool
	err              error
}

// Store the child outcome separately. A later failure to restore the terminal
// must not turn a confirmed password change into an unconfirmed one.
type passwordTerminal struct {
	cmd       *exec.Cmd
	execution passwordExecution
}

func newPasswordTerminal() *passwordTerminal {
	return &passwordTerminal{cmd: exec.Command("/usr/bin/passwd")}
}
func (t *passwordTerminal) SetStdin(r io.Reader)  { t.cmd.Stdin = r }
func (t *passwordTerminal) SetStdout(w io.Writer) { t.cmd.Stdout = w }
func (t *passwordTerminal) SetStderr(w io.Writer) { t.cmd.Stderr = w }
func (t *passwordTerminal) Run() (err error) {
	defer func() { t.execution.err = err }()
	return keepTerminalMode(t.cmd.Stdin, t.run)
}
func (t *passwordTerminal) run() error {
	out := t.cmd.Stdout
	if out == nil {
		out = io.Discard
	}
	if _, err := fmt.Fprint(out, "To cancel at a password prompt: Ctrl+U, then Ctrl+D.\nCtrl+U clears your input. Ctrl+D ends input.\n\n"); err != nil {
		return fmt.Errorf("show password instructions: %w", err)
	}
	if err := t.cmd.Start(); err != nil {
		return fmt.Errorf("start passwd: %w", err)
	}
	t.execution.started = true
	if err := t.cmd.Wait(); err != nil {
		return fmt.Errorf("passwd ended without confirmed success: %w", err)
	}
	t.execution.changed = true
	return nil
}
func (t *passwordTerminal) result(terminalErr error) passwordExecution {
	result := t.execution
	if terminalErr != nil && !errors.Is(terminalErr, result.err) {
		result.err = errors.Join(result.err, fmt.Errorf("terminal session: %w", terminalErr))
	}
	return result
}
