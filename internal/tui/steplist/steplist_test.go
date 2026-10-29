package steplist

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Step names and errors come from other programs: apt, curl, the update
// program of a later release, the root helper. None of them may move the
// cursor, clear the screen or set the window title, and their words stay
// readable.
func TestStepTextCannotControlTheTerminal(t *testing.T) {
	codes := []string{"\x1b[2J", "\x1b[H", "\x1b]0;title\a", "\r", "\b"}
	bad := strings.Join(codes, "")
	for _, state := range []State{Ahead, Running, Done, Failed, Earlier, Stopped} {
		step := Step{Name: "Check" + bad + "node", State: state, Err: "write" + bad + "failed"}
		rows, _, _ := Rows([]Step{step}, 70, "*", time.Now(), 0)
		out := strings.Join(rows, "\n")
		for _, code := range codes {
			if strings.Contains(out, code) {
				t.Fatalf("state %d: %q reached the terminal", state, code)
			}
		}
		plain := ansi.Strip(out)
		if !strings.Contains(plain, "Check") || !strings.Contains(plain, "node") {
			t.Fatalf("state %d: the step's name is not readable: %q", state, plain)
		}
		if state == Failed && (!strings.Contains(plain, "write") || !strings.Contains(plain, "failed")) {
			t.Fatalf("the error is not readable: %q", plain)
		}
	}
}

// A failed step's name and its whole error stay in view wherever the step
// is in the list, at the heights the three screens give the list. An error
// longer than the window keeps its step's name.
func TestFailedStepAndErrorStayInView(t *testing.T) {
	for _, h := range []int{8, 12, 27} {
		for _, words := range []int{8, 60, 400} {
			for at := range 28 {
				steps := make([]Step, 28)
				for i := range steps {
					steps[i].Name = fmt.Sprintf("Step %d of the install", i+1)
					if i < at {
						steps[i].State = Done
					}
				}
				steps[at].State = Failed
				steps[at].Err = errors.New(strings.Repeat("cause ", words) + "LASTWORD").Error()
				rows, from, to := Rows(steps, 65, "*", time.Now(), 0)
				view := ansi.Strip(strings.Join(Window(rows, from, to, h, false), "\n"))
				if !strings.Contains(view, steps[at].Name) {
					t.Fatalf("%d rows, %d words at step %d: the step's name is not in view", h, words, at+1)
				}
				if to-from < h && !strings.Contains(view, "LASTWORD") {
					t.Fatalf("%d rows, %d words at step %d: the end of the error is not in view", h, words, at+1)
				}
			}
		}
	}
}

// A step can start, end and the next one start before the spinner's first
// tick arrives. The spinner must then still run one timer, or it spins
// twice as fast. A tick after the spinner was turned off ends its timer.
func TestSpinnerRestartRunsOneTimer(t *testing.T) {
	s := NewSpinner()
	first := s.Set(true)()
	s.Set(false)
	second := s.Set(true)()
	timers := 0
	for _, msg := range []tea.Msg{first, second} {
		if s.Update(msg) != nil {
			timers++
		}
	}
	if timers != 1 {
		t.Fatalf("%d timers run after a quick restart, want 1", timers)
	}

	s = NewSpinner()
	tick := s.Set(true)()
	s.Set(false)
	if s.Update(tick) != nil {
		t.Fatal("a stopped spinner started its next tick")
	}
}
