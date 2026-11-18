// Command factories connect screen messages to application and system operations.

package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/virtpriv/node/internal/release"
)

// ── Polling & version ────────────────────────────────────

func tickEveryCmd(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func fetchLatestVersionCmd() tea.Cmd {
	return func() tea.Msg {
		return latestVersionMsg(
			release.CheckLatestVersion())
	}
}

// latestVersionRetry is how long an open TUI waits to ask again after a
// release check found nothing.
const latestVersionRetry = 10 * time.Minute

func retryLatestVersionCmd() tea.Cmd {
	return tea.Tick(latestVersionRetry, func(time.Time) tea.Msg {
		return latestVersionMsg(release.CheckLatestVersion())
	})
}
