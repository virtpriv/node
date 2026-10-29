package tui

import (
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/virtpriv/node/internal/logger"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/theme"
)

// relaunchMsg asks the TUI to end and the installed vpn to take its place.
// After an update the running TUI is still the old program, the new one is
// already at paths.BinaryPath.
type relaunchMsg struct{}

func emitRelaunch() tea.Msg { return relaunchMsg{} }

// finishRun starts the installed vpn in place of this program when the TUI
// ended with Relaunch. start replaces the program, in the same terminal, and
// returns only when that fails.
func finishRun(final tea.Model, start func(string, []string, []string) error) error {
	m, ok := final.(Model)
	if !ok || !m.relaunch {
		return nil
	}
	return start(paths.BinaryPath, []string{"vpn"}, os.Environ())
}

// relaunchFailed tells the operator in the shell how to open the new version.
func relaunchFailed(err error) {
	logger.TUI("relaunch: %v", err)
	fmt.Fprintln(os.Stderr)
	theme.PrintShell(os.Stderr, []string{
		"Could not open the new version: " + err.Error(),
		"To try again, run:",
		"",
		"vpn",
	})
	os.Exit(1)
}
