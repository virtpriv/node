package tui

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/virtpriv/node/internal/theme"
)

type changePwStep int

const (
	changePwStepInput changePwStep = iota
	changePwStepWorking
	changePwStepResult
)

type changePwDoneMsg struct {
	owner   *ChangePasswordScreen
	attempt uint64
	result  passwordExecution
}
type closeLoginPasswordMsg struct {
	owner   *ChangePasswordScreen
	attempt uint64
}

// ChangePasswordScreen hands the terminal to native passwd without collecting
// credentials. The process runs as the same unprivileged owner as the TUI.
type ChangePasswordScreen struct {
	ctx     *ScreenContext
	step    changePwStep
	attempt uint64
	btnIdx  int
	result  passwordExecution
}

func NewChangePasswordScreen(ctx *ScreenContext) *ChangePasswordScreen {
	return &ChangePasswordScreen{ctx: ctx, btnIdx: 1}
}
func (s *ChangePasswordScreen) Init() tea.Cmd { return nil }
func (s *ChangePasswordScreen) closeCommand() tea.Cmd {
	attempt := s.attempt
	return func() tea.Msg { return closeLoginPasswordMsg{owner: s, attempt: attempt} }
}
func (s *ChangePasswordScreen) HandleKey(k string, _ tea.KeyPressMsg) (Screen, tea.Cmd) {
	if k == "ctrl+c" {
		return s, tea.Quit
	}
	if s.step == changePwStepWorking {
		return s, nil
	}
	switch k {
	case "up", "shift+tab":
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
	case "backspace":
		return s, emitFocusParent
	case "left":
		if s.step == changePwStepInput && s.btnIdx > 0 {
			s.btnIdx--
			return s, nil
		}
		return s, emitFocusSidebar
	case "right":
		if s.step == changePwStepInput {
			s.btnIdx = 1
		}
	case "enter":
		if s.step == changePwStepResult || s.btnIdx == 0 {
			return s, s.closeCommand()
		}
		return s, s.changeCommand()
	}
	return s, nil
}
func (s *ChangePasswordScreen) changeCommand() tea.Cmd {
	if s.step != changePwStepInput {
		return nil
	}
	s.step = changePwStepWorking
	s.attempt++
	attempt := s.attempt
	terminal := newPasswordTerminal()
	return tea.Exec(terminal, func(err error) tea.Msg {
		return changePwDoneMsg{owner: s, attempt: attempt, result: terminal.result(err)}
	})
}
func (s *ChangePasswordScreen) HandleMsg(msg tea.Msg) (Screen, tea.Cmd) {
	if m, ok := msg.(changePwDoneMsg); ok && m.owner == s && m.attempt == s.attempt && s.step == changePwStepWorking {
		s.result = m.result
		s.step = changePwStepResult
		s.btnIdx = 0
	}
	return s, nil
}
func (s *ChangePasswordScreen) View(w, h int) string {
	p := newPane(w)
	if s.step == changePwStepResult {
		switch {
		case s.result.changed:
			p.title(theme.Success, "Password changed successfully")
			p.dim("Save the new password in your password manager.")
		case !s.result.started:
			p.title(theme.Warning, "Password not changed")
		default:
			p.title(theme.Warning, "Password change not confirmed")
			p.dim("Keep this session open. If the command was interrupted,")
			p.dim("check which password works before closing your session.")
		}
		if s.result.err != nil {
			p.warnWrap(s.result.err.Error())
		}
		return p.renderWithBottomButtons([]string{"Done"}, 0, s.ctx.ContentFocused, h)
	}
	p.title(theme.Header, "Change Login Password")
	p.blank()
	p.dim("Debian will ask for your current account password,")
	p.dim("then your new password twice. Typing is invisible.")
	p.blank()
	p.dim("Choose a strong password and save it in your")
	p.dim("password manager. Debian's password policy applies.")
	p.dim("This changes your SSH and sudo password, not your")
	p.dim("Lightning wallet password.")
	p.blank()
	p.dim("To cancel at a password prompt: Ctrl+U, then Ctrl+D.")
	p.dim("Ctrl+U clears your input; Ctrl+D ends input.")
	p.dim("The TUI resumes when the command finishes.")
	return p.renderWithBottomButtons([]string{"Cancel", "Continue"}, s.btnIdx, s.ctx.ContentFocused, h)
}
func (s *ChangePasswordScreen) HelpBindings() []key.Binding {
	if s.step == changePwStepResult {
		return resultBindings(s.ctx.HasTabs)
	}
	return tabButtonBindings(s.ctx.HasTabs)
}
