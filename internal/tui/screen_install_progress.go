package tui

import (
	"fmt"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/logger"
	"github.com/virtpriv/node/internal/theme"
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
}

func NewInstallProgressScreen(ctx *ScreenContext, operation *app.HelperOperation, onDone, onFail func() tea.Cmd) *InstallProgressScreen {
	names := operation.Steps()
	steps := make([]progressStep, len(names))
	for i, name := range names {
		steps[i].Name = name
	}
	return &InstallProgressScreen{ctx: ctx, operation: operation, steps: steps, onDone: onDone, onFail: onFail}
}

func (s *InstallProgressScreen) Init() tea.Cmd {
	if s.started {
		return nil
	}
	s.started = true
	if len(s.steps) > 0 {
		s.steps[0].Status = progressRunning
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
		if result.Config != nil {
			// Publish only the setting this operation owns. A delayed reload must not
			// overwrite another workflow's newer setting, such as auto-unlock.
			switch s.operation.Kind() {
			case app.SyncthingInstall:
				s.ctx.Cfg.SyncthingEnabled = result.Config.SyncthingEnabled
			case app.P2PUpgrade:
				s.ctx.Cfg.P2PMode = result.Config.P2PMode
			}
		}
		if s.failed {
			if s.current < len(s.steps) {
				s.steps[s.current].Status = progressFailed
				s.steps[s.current].Err = result.Err
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
	s.current++
	if s.current < len(s.steps) {
		s.steps[s.current].Status = progressRunning
	}
	return s, s.waitProgress()
}

// ── View ────────────────────────────────────────────────

func (s *InstallProgressScreen) View(
	w, h int,
) string {
	p := newPane(w)

	for i, step := range s.steps {
		var ind string
		var sty = theme.Dim
		switch step.Status {
		case progressDone:
			ind = "[done]"
			sty = theme.Good
		case progressRunning:
			ind = "[....]"
			sty = theme.Value
		case progressFailed:
			ind = "[FAIL]"
			sty = theme.Warn
		default:
			ind = "[wait]"
		}

		p.line(" " + sty.Render(fmt.Sprintf(
			"%s [%d/%d] %s",
			ind, i+1, len(s.steps), step.Name)))

		if step.Status == progressFailed &&
			step.Err != nil {
			p.warnWrap(fmt.Sprintf(
				"    Error: %v", step.Err))
		}
	}

	p.blank()

	if s.done && !s.failed {
		p.line(" " + theme.Good.Render("Complete."))
		return p.renderWithBottomButtons(
			[]string{"Done"}, 0,
			s.ctx.ContentFocused, h)
	} else if s.failed {
		if s.helperSucceeded {
			p.warnWrap("The helper completed the change, but local configuration could not be refreshed.")
		} else {
			p.warnWrap("The workflow did not complete successfully. Changes may have been applied; check the node before trying again.")
		}
		return p.renderWithBottomButtons(
			[]string{"Done"}, 0,
			s.ctx.ContentFocused, h)
	}

	p.dim("Do not close the terminal.")
	return p.renderWithBottomButtons(
		[]string{"Installing..."}, 0, false, h)
}

// ── HelpBindings ────────────────────────────────────────

func (s *InstallProgressScreen) HelpBindings() []key.Binding {
	if s.done {
		return resultBindings(s.ctx.HasTabs)
	}
	return inFlightBindings()
}
