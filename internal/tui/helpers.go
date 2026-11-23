package tui

import (
	"os/exec"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/virtpriv/node/internal/theme"
)

// renderViewport creates a local viewport, sets content,
// auto-scrolls to keep cursorLine visible, and returns the
// rendered view with scroll indicators overlaid.
//
// content is the fully rendered string of all lines, w the viewport's width
// and vpH its height in visible lines. cursorLine is the line the cursor is
// on, counted from 0, and totalLines the number of lines in content. active
// says whether auto-scroll applies.
func renderViewport(
	content string, w, vpH, cursorLine int,
	totalLines int, active bool,
) string {
	vp := viewport.New(
		viewport.WithWidth(w),
		viewport.WithHeight(vpH),
	)
	vp.FillHeight = true
	vp.SetContent(content)

	// Auto-scroll to keep cursor visible
	if active && totalLines > vpH {
		offset := vp.YOffset()
		if cursorLine < offset {
			vp.SetYOffset(cursorLine)
		}
		if cursorLine >= offset+vpH {
			vp.SetYOffset(cursorLine - vpH + 1)
		}
	}

	vpView := vp.View()
	vpLines := strings.Split(vpView, "\n")

	hasAbove := vp.YOffset() > 0
	hasBelow := vp.YOffset()+vpH < totalLines

	// Overlay scroll indicators on the right edge
	// of the first/last line. We truncate the line
	// to make room so total width stays within bounds.
	if hasAbove && len(vpLines) > 0 {
		line := vpLines[0]
		lineW := lipgloss.Width(line)
		arrow := theme.Dim.Render(" ▲")
		if lineW > w-2 {
			// Truncate line to make room
			r := []rune(line)
			for lipgloss.Width(string(r)) > w-2 && len(r) > 0 {
				r = r[:len(r)-1]
			}
			line = string(r)
			lineW = lipgloss.Width(line)
		}
		pad := w - 2 - lineW
		if pad < 0 {
			pad = 0
		}
		vpLines[0] = line +
			strings.Repeat(" ", pad) + arrow
	}
	if hasBelow && len(vpLines) > 0 {
		idx := len(vpLines) - 1
		line := vpLines[idx]
		lineW := lipgloss.Width(line)
		arrow := theme.Dim.Render(" ▼")
		if lineW > w-2 {
			r := []rune(line)
			for lipgloss.Width(string(r)) > w-2 && len(r) > 0 {
				r = r[:len(r)-1]
			}
			line = string(r)
			lineW = lipgloss.Width(line)
		}
		pad := w - 2 - lineW
		if pad < 0 {
			pad = 0
		}
		vpLines[idx] = line +
			strings.Repeat(" ", pad) + arrow
	}

	return strings.Join(vpLines, "\n")
}

// showJournal opens the newest 100 lines of a unit's journal in the terminal
// and comes back to the TUI at Enter. The unit's name is handed to
// journalctl as one argument and is never part of the shell's text.
func showJournal(unit string, back tea.Msg) tea.Cmd {
	c := exec.Command("bash", "-c",
		`clear && journalctl -u "$1" -n 100 --no-pager`+
			" && echo && echo '  Press Enter to return...' && read && clear", "vpn", unit)
	return tea.ExecProcess(c, func(error) tea.Msg { return back })
}
