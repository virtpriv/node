// Package steplist draws the list of steps that the installer, Node Updates
// and the Syncthing and P2P progress screen show, with the spinner on the
// step that runs.
package steplist

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/virtpriv/node/internal/theme"
)

// State is what a step shows.
type State int

const (
	// Ahead has not begun: a faint dot.
	Ahead State = iota
	// Running shows the spinner, a bold name and how long it runs.
	Running
	// Done shows a green check and how long it took.
	Done
	// Failed shows a red cross and the error under the name.
	Failed
	// Earlier was done in an earlier run whose times are unknown: a gray
	// check and no time.
	Earlier
	// Stopped waits for the owner: a gray mark and no error.
	Stopped
)

// Step is one row of the list. Started and Ended are when the step began
// and finished or failed, the zero time when unknown.
type Step struct {
	Name    string
	State   State
	Err     string
	Started time.Time
	Ended   time.Time
}

// Start notes that the step began at now.
func (s *Step) Start(now time.Time) { s.Started, s.Ended = now, time.Time{} }

// End notes that the step finished or failed at now.
func (s *Step) End(now time.Time) { s.Ended = now }

// Clock writes a duration as minutes and seconds, such as 1:42.
func Clock(d time.Duration) string {
	d = d.Round(time.Second)
	return fmt.Sprintf("%d:%02d", int(d.Minutes()), int(d.Seconds())%60)
}

// Rows draws every step w cells wide, with spin as the running step's mark.
// from and to are the first and last rows of the running, failed or stopped
// step, its error included. errRows caps an error's rows, 0 for all. Names
// and errors come from other programs, so their control characters become
// spaces and cannot move the cursor or clear the screen.
func Rows(steps []Step, w int, spin string, now time.Time, errRows int) (rows []string, from, to int) {
	done := lipgloss.NewStyle().Foreground(theme.ColorSuccess).Render("✓")
	faint := lipgloss.NewStyle().Foreground(theme.ColorFaint)
	for _, s := range steps {
		name := theme.PlainText(s.Name)
		var mark, took string
		switch s.State {
		case Done:
			mark, name = done, theme.Label.Render(name)
			if !s.Started.IsZero() && !s.Ended.Before(s.Started) && !s.Ended.IsZero() {
				took = faint.Render(Clock(s.Ended.Sub(s.Started)))
			}
		case Running:
			mark = spin
			name = lipgloss.NewStyle().Bold(true).Foreground(theme.ColorPrimary).Render(name)
			if !s.Started.IsZero() {
				took = theme.Dim.Render(Clock(max(0, now.Sub(s.Started))))
			}
		case Failed:
			mark, name = theme.Warning.Render("✗"), theme.Warning.Render(name)
		case Earlier:
			mark, name = theme.Grayed.Render("✓"), theme.Dim.Render(name)
		case Stopped:
			mark, name = theme.Dim.Render("›"), theme.Label.Render(name)
		default:
			mark, name = faint.Render("·"), faint.Render(name)
		}
		start := len(rows)
		rows = append(rows, theme.Spread(" "+mark+" "+name, took, w))
		if s.State == Failed && s.Err != "" {
			lines := theme.Wrap("Error: "+theme.PlainText(s.Err), w-6)
			if errRows > 0 && len(lines) > errRows {
				lines = lines[:errRows]
				lines[errRows-1] = ansi.Truncate(lines[errRows-1]+" …", w-6, "…")
			}
			for _, l := range lines {
				rows = append(rows, "   "+theme.Warning.Render(l))
			}
		}
		if s.State == Running || s.State == Failed || s.State == Stopped {
			from, to = start, len(rows)-1
		}
	}
	return rows, from, to
}

// Window shows the rows that fit in h and keeps rows from to to in view,
// the running or failed step and its error, with a few steps after it. An
// error longer than the window keeps its step's name. end shows the list's
// end, for a finished list.
func Window(rows []string, from, to, h int, end bool) []string {
	fits := max(1, h)
	first := min(max(0, to-fits+4), max(0, len(rows)-fits), from)
	if end {
		first = max(0, len(rows)-fits)
	}
	return rows[first:min(len(rows), first+fits)]
}

// Spinner is Charm's spinner in the running step's color. It ticks only
// while it is on. Charm's spinner drops a tick of another spinner, but not
// its own first tick, so a spinner started again before that tick arrived
// would run two timers and spin twice as fast. Each start therefore makes a
// fresh spinner, and a tick of the stopped one is dropped.
type Spinner struct {
	m  spinner.Model
	on bool
}

func NewSpinner() Spinner {
	return Spinner{m: spinner.New(spinner.WithSpinner(spinner.Dot))}
}

// Set turns the spinner on or off and returns the command that starts it.
func (s *Spinner) Set(on bool) tea.Cmd {
	if on == s.on {
		return nil
	}
	s.on = on
	if !on {
		return nil
	}
	s.m = spinner.New(spinner.WithSpinner(spinner.Dot))
	return s.m.Tick
}

// Owns reports whether msg is this spinner's tick.
func (s Spinner) Owns(msg tea.Msg) bool {
	t, ok := msg.(spinner.TickMsg)
	return ok && t.ID == s.m.ID()
}

// Update advances the spinner on its own tick and returns the next tick.
// A tick that arrives while the spinner is off is dropped, which stops it.
func (s *Spinner) Update(msg tea.Msg) tea.Cmd {
	if !s.Owns(msg) || !s.on {
		return nil
	}
	var cmd tea.Cmd
	s.m, cmd = s.m.Update(msg)
	return cmd
}

// View is the current frame. Charm's frames end in a space, which would
// push the step's name one column right of the other rows.
func (s Spinner) View() string {
	return lipgloss.NewStyle().Foreground(theme.ColorBitcoin).Render(strings.TrimSpace(s.m.View()))
}
