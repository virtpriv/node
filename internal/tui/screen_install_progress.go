package tui

import (
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/logger"
	"github.com/virtpriv/node/internal/theme"
	"github.com/virtpriv/node/internal/tui/steplist"
)

// helperProgressMsg identifies one operation and its ordered application event.
type helperProgressMsg struct {
	operation *app.HelperOperation
	event     app.HelperProgress
}

type closeHelperProgressMsg struct{ screen *InstallProgressScreen }

type progressStep struct {
	Name   string
	Status int
	Err    error
	// times holds when the step began and ended. The helper's events carry
	// no times, so the screen notes them as the events arrive.
	times steplist.Step
}

const (
	_ = iota
	progressRunning
	progressDone
	progressFailed
)

// InstallProgressScreen renders one application-owned helper operation. Only
// the event loop mutates its presentation state and publishes configuration.
type InstallProgressScreen struct {
	ctx             *ScreenContext
	operation       *app.HelperOperation
	steps           []progressStep
	onDone          func() tea.Cmd
	onFail          func() tea.Cmd
	current         int
	started         bool
	done            bool
	failed          bool
	helperSucceeded bool
	nothingChanged  bool
	spin            steplist.Spinner
}

func NewInstallProgressScreen(ctx *ScreenContext, operation *app.HelperOperation, onDone, onFail func() tea.Cmd) *InstallProgressScreen {
	names := operation.Steps()
	steps := make([]progressStep, len(names))
	for i, name := range names {
		steps[i].Name = name
	}
	return &InstallProgressScreen{ctx: ctx, operation: operation, steps: steps, onDone: onDone, onFail: onFail, spin: steplist.NewSpinner()}
}

func (s *InstallProgressScreen) Init() tea.Cmd {
	if s.started {
		return nil
	}
	s.started = true
	if len(s.steps) > 0 {
		s.steps[0].Status = progressRunning
		s.steps[0].times.Start(time.Now())
	}
	return s.waitProgress()
}

func (s *InstallProgressScreen) waitProgress() tea.Cmd {
	operation := s.operation
	return func() tea.Msg {
		event, ok := <-operation.Events()
		if !ok {
			return nil
		}
		return helperProgressMsg{operation: operation, event: event}
	}
}

func (s *InstallProgressScreen) HandleKey(
	keyStr string, msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
	if s.done {
		switch keyStr {
		case "ctrl+c":
			return s, tea.Quit
		case "enter":
			return s, func() tea.Msg { return closeHelperProgressMsg{screen: s} }
		case "left":
			return s, emitFocusSidebar
		case "up", "shift+tab":
			if s.ctx.HasTabs {
				return s, emitFocusTabBar
			}
		case "backspace":
			return s, emitFocusParent
		}
	}
	// During active install, all keys are ignored.
	return s, nil
}

func (s *InstallProgressScreen) HandleMsg(msg tea.Msg) (Screen, tea.Cmd) {
	m, ok := msg.(helperProgressMsg)
	if !ok || m.operation != s.operation || !s.started || s.done || m.event.Index != s.current {
		return s, nil
	}
	if result := m.event.Result; result != nil {
		if result.Err == nil && (s.current != len(s.steps) || !result.HelperSucceeded) {
			return s, nil
		}
		s.done = true
		s.failed = result.Err != nil
		s.helperSucceeded = result.HelperSucceeded
		s.nothingChanged = result.NothingChanged
		if result.Config != nil {
			// Publish only the setting this operation owns. A delayed reload must not
			// overwrite another workflow's newer setting, such as auto-unlock.
			switch s.operation.Kind() {
			case app.SyncthingInstall:
				s.ctx.Cfg.SyncthingEnabled = result.Config.SyncthingEnabled
			case app.P2PUpgrade:
				s.ctx.Cfg.P2PMode = result.Config.P2PMode
				s.ctx.Cfg.P2PAddress = result.Config.P2PAddress
			}
		}
		if s.failed {
			if s.current < len(s.steps) {
				s.steps[s.current].Status = progressFailed
				s.steps[s.current].Err = result.Err
				s.steps[s.current].times.End(time.Now())
			}
			logger.Install("helper workflow: %v", result.Err)
			if s.onFail != nil {
				return s, s.onFail()
			}
		} else if s.onDone != nil {
			return s, s.onDone()
		}
		return s, nil
	}
	if s.current >= len(s.steps) {
		return s, nil
	}
	s.steps[s.current].Status = progressDone
	s.steps[s.current].times.End(time.Now())
	s.current++
	if s.current < len(s.steps) {
		s.steps[s.current].Status = progressRunning
		s.steps[s.current].times.Start(time.Now())
	}
	return s, s.waitProgress()
}

// ── View ────────────────────────────────────────────────

func (s *InstallProgressScreen) View(
	w, h int,
) string {
	p := newPane(w)

	steps := make([]steplist.Step, len(s.steps))
	for i, step := range s.steps {
		steps[i] = step.times
		steps[i].Name = step.Name
		switch step.Status {
		case progressDone:
			steps[i].State = steplist.Done
		case progressRunning:
			steps[i].State = steplist.Running
		case progressFailed:
			steps[i].State = steplist.Failed
			if step.Err != nil {
				steps[i].Err = step.Err.Error()
			}
		}
	}
	rows, from, to := steplist.Rows(steps, max(p.w-1, 16), s.spin.View(), time.Now(), 0)
	// The outcome below the list and the button row stay in view.
	p.lines = append(p.lines, steplist.Window(rows, from, to, max(1, h-progressBelowRows), s.done && !s.failed)...)

	p.blank()

	if s.done && !s.failed {
		p.line(" " + theme.Good.Render("Complete."))
		return p.renderWithBottomButtons(
			[]string{"Done"}, 0,
			s.ctx.ContentFocused, h)
	} else if s.failed {
		switch {
		case s.helperSucceeded:
			p.warnWrap("The helper completed the change, but local configuration could not be refreshed.")
		case s.nothingChanged:
			p.fitText(lipgloss.NewStyle(), "The workflow stopped before changing anything.", 0)
		default:
			p.warnWrap("The workflow did not complete successfully. Changes may have been applied. Check the node before trying again.")
		}
		return p.renderWithBottomButtons(
			[]string{"Done"}, 0,
			s.ctx.ContentFocused, h)
	}

	p.dim("Do not close the terminal.")
	return p.renderWithBottomButtons(
		[]string{"Installing..."}, 0, false, h)
}

// progressBelowRows is what the screen shows under its list at most: an
// empty row, the outcome in up to three rows, and the button row.
const progressBelowRows = 6

// spinner and stepRuns let the model run the spinner while a step runs and
// the screen shows.
func (s *InstallProgressScreen) spinner() *steplist.Spinner { return &s.spin }
func (s *InstallProgressScreen) stepRuns() bool {
	return s.started && !s.done && s.current < len(s.steps)
}

// ── HelpBindings ────────────────────────────────────────

func (s *InstallProgressScreen) HelpBindings() []key.Binding {
	if s.done {
		return resultBindings(s.ctx.HasTabs)
	}
	return inFlightBindings()
}
