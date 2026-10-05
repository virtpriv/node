package tui

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/virtpriv/node/internal/theme"
)

// ── SSHPasswordAuthScreen ──────────────────────────────
// Toggle SSH password authentication on/off. Opened as
// its own tab from SSHKeysScreen. Mirrors the
// detail-+-confirm pattern used by other destructive-
// action screens.
//
// Disabling is destructive (potential lockout) and refused
// by the root helper if no supported SSH keys are configured.
// Enabling is always allowed.

type sshPwAuthStep int

const (
	sshPwAuthStepView sshPwAuthStep = iota
	sshPwAuthStepConfirm
	sshPwAuthStepWorking
	sshPwAuthStepResult
)

type sshPwAuthDoneMsg struct {
	owner    *SSHPasswordAuthScreen
	attempt  uint64
	disabled bool // the value we just applied
	err      error
}

type sshPwAuthStateMsg struct {
	owner    *ScreenContext
	revision uint64
	disabled bool
	err      error
}

func (s *SSHPasswordAuthScreen) setCommand() tea.Cmd {
	s.attempt++
	s.ctx.sshAuthRevision++
	s.ctx.State.SSHPasswordAuthKnown = false
	attempt, disabled, access := s.attempt, s.targetDisabled, s.ctx.sshAccess()
	return func() tea.Msg {
		return sshPwAuthDoneMsg{owner: s, attempt: attempt, disabled: disabled, err: access.SetPasswordAuthDisabled(disabled)}
	}
}

type refreshSSHAuthMsg struct{}

func fetchSSHPasswordAuthCmd() tea.Cmd {
	return func() tea.Msg { return refreshSSHAuthMsg{} }
}

type SSHPasswordAuthScreen struct {
	attempt        uint64
	targetDisabled bool
	ctx            *ScreenContext
	step           sshPwAuthStep
	viewBtnIdx     int // 0 = Cancel, 1 = toggle action
	confirmIdx     int // 0 = Go Back, 1 = Apply
	resultErr      string
	resultMsg      string
}

func NewSSHPasswordAuthScreen(
	ctx *ScreenContext,
) *SSHPasswordAuthScreen {
	return &SSHPasswordAuthScreen{ctx: ctx}
}

// ── Screen interface ────────────────────────────────────

func (s *SSHPasswordAuthScreen) Init() tea.Cmd {
	return fetchSSHPasswordAuthCmd()
}

func (s *SSHPasswordAuthScreen) HandleKey(
	keyStr string, msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
	switch s.step {
	case sshPwAuthStepView:
		return s.handleViewKey(keyStr)
	case sshPwAuthStepConfirm:
		return s.handleConfirmKey(keyStr)
	case sshPwAuthStepWorking:
		if keyStr == "ctrl+c" {
			return s, tea.Quit
		}
	case sshPwAuthStepResult:
		return s.handleResultKey(keyStr)
	}
	return s, nil
}

func (s *SSHPasswordAuthScreen) HandleMsg(
	msg tea.Msg,
) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tabActivatedMsg:
		return s, fetchSSHPasswordAuthCmd()
	case sshPwAuthDoneMsg:
		if msg.owner != s || msg.attempt != s.attempt || s.step != sshPwAuthStepWorking {
			return s, nil
		}
		s.ctx.sshAuthRevision++
		s.ctx.State.SSHPasswordAuthKnown = msg.err == nil
		if msg.err == nil {
			s.ctx.State.SSHPasswordAuthDisabled = msg.disabled
		}
		s.step = sshPwAuthStepResult
		if msg.err != nil {
			s.resultErr = msg.err.Error()
			s.resultMsg = ""
			return s, nil
		}
		if msg.disabled {
			s.resultMsg = "Password authentication disabled"
		} else {
			s.resultMsg = "Password authentication enabled"
		}
		s.resultErr = ""
		return s, nil
	}
	return s, nil
}

func (s *SSHPasswordAuthScreen) View(w, h int) string {
	switch s.step {
	case sshPwAuthStepView:
		return s.viewState(w, h)
	case sshPwAuthStepConfirm:
		return s.viewConfirm(w, h)
	case sshPwAuthStepWorking:
		return s.viewWorking(w, h)
	case sshPwAuthStepResult:
		return s.viewResult(w, h)
	}
	return ""
}

func (s *SSHPasswordAuthScreen) HelpBindings() []key.Binding {
	switch s.step {
	case sshPwAuthStepView:
		binds := buttonNav(s.viewBtnIdx)
		binds = append(binds, kEnter)
		if s.ctx.HasTabs {
			binds = append(binds, kShiftTabBar)
		}
		binds = append(binds, kBack, kQuit)
		return binds
	case sshPwAuthStepConfirm:
		return []key.Binding{
			kLeftRightButtons,
			kEnter,
			kBack,
			kQuit,
		}
	case sshPwAuthStepWorking:
		return []key.Binding{kQuit}
	case sshPwAuthStepResult:
		return resultBindings(s.ctx.HasTabs)
	}
	return nil
}

// ── View step ───────────────────────────────────────────

func (s *SSHPasswordAuthScreen) handleViewKey(
	keyStr string,
) (Screen, tea.Cmd) {
	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit
	case "left":
		if s.viewBtnIdx > 0 {
			s.viewBtnIdx--
			return s, nil
		}
		return s, emitFocusSidebar
	case "right":
		if s.viewBtnIdx < 1 {
			s.viewBtnIdx++
		}
		return s, nil
	case "up", "shift+tab":
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
		return s, nil
	case "down", "tab":
		return s, nil
	case "backspace":
		return s, emitFocusParent
	case "enter":
		if s.viewBtnIdx == 0 {
			return s, closeSSHScreenCmd(s)
		}
		if !s.ctx.State.SSHPasswordAuthKnown {
			return s, fetchSSHPasswordAuthCmd()
		}
		s.targetDisabled = !s.ctx.State.SSHPasswordAuthDisabled
		s.step = sshPwAuthStepConfirm
		s.confirmIdx = 0
		return s, nil
	}
	return s, nil
}

func (s *SSHPasswordAuthScreen) viewState(
	w, h int,
) string {
	p := newPane(w)
	p.title(theme.Header, "SSH Password Authentication")
	p.blank()

	if !s.ctx.State.SSHPasswordAuthKnown {
		p.warn("Cannot read effective SSH password authentication.")
		p.dim("Check: journalctl -u vpn-helperd")
		return p.renderWithBottomButtons(
			[]string{"Cancel", "Unavailable"}, 0,
			s.ctx.ContentFocused, h)
	}
	disabled := s.ctx.State.SSHPasswordAuthDisabled
	if disabled {
		p.field("Status:      ",
			theme.Warning.Render("disabled"))
	} else {
		p.field("Status:      ",
			theme.Success.Render("enabled"))
	}
	p.blank()

	if disabled {
		p.dim("Password login over SSH is currently")
		p.dim("disabled. Only SSH keys can authenticate.")
		p.blank()
		p.dim("Re-enable password auth to allow login")
		p.dim("with the operator password.")
	} else {
		p.dim("Password login over SSH is currently")
		p.dim("enabled. Either keys or the operator")
		p.dim("password can authenticate.")
		p.blank()
		p.dim("Disable password auth to require SSH keys")
		p.dim("for all logins. Add a key first if you")
		p.dim("haven't yet — the system refuses to")
		p.dim("disable when no keys are configured.")
		p.blank()
		p.warn("Before disabling: open a new SSH session")
		p.warn("and verify your key works. Disabling")
		p.warn("without a tested key risks lockout.")
	}

	label := "Disable Password Auth"
	if disabled {
		label = "Enable Password Auth"
	}
	return p.renderWithBottomButtons(
		[]string{"Cancel", label}, s.viewBtnIdx,
		s.ctx.ContentFocused, h)
}

// ── Confirm step ────────────────────────────────────────

func (s *SSHPasswordAuthScreen) handleConfirmKey(
	keyStr string,
) (Screen, tea.Cmd) {
	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit
	case "left":
		if s.confirmIdx > 0 {
			s.confirmIdx--
			return s, nil
		}
		return s, emitFocusSidebar
	case "right":
		if s.confirmIdx < 1 {
			s.confirmIdx++
		}
		return s, nil
	case "up", "shift+tab":
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
		return s, nil
	case "down", "tab":
		return s, nil
	case "backspace":
		s.step = sshPwAuthStepView
		return s, nil
	case "enter":
		switch s.confirmIdx {
		case 0: // Go Back
			s.step = sshPwAuthStepView
			return s, nil
		case 1: // Apply the exact choice shown at confirmation.
			s.step = sshPwAuthStepWorking
			return s, s.setCommand()
		}
	}
	return s, nil
}

func (s *SSHPasswordAuthScreen) viewConfirm(
	w, h int,
) string {
	p := newPane(w)

	disabling := s.targetDisabled

	if disabling {
		p.title(theme.Warning, "Disable password auth?")
		p.blank()
		p.warn("After this, only SSH keys can log in.")
		p.warn("If you lose access to all keys, you")
		p.warn("will be locked out of this node.")
		p.blank()
		p.dim("Confirm that:")
		p.dim("  • You have at least one key that works")
		p.dim("  • You have tested logging in with it")
		p.blank()
		p.warn("After applying: keep this session open")
		p.warn("and verify key login from a new")
		p.warn("terminal before disconnecting.")
	} else {
		p.title(theme.Header, "Enable password auth?")
		p.blank()
		p.dim("Password login over SSH will be allowed.")
		p.dim("SSH keys will continue to work.")
	}

	applyLabel := "Disable"
	if !disabling {
		applyLabel = "Enable"
	}
	return p.renderWithBottomButtons(
		[]string{"Go Back", applyLabel}, s.confirmIdx,
		s.ctx.ContentFocused, h)
}

// ── Working step ────────────────────────────────────────

func (s *SSHPasswordAuthScreen) viewWorking(
	w, h int,
) string {
	p := newPane(w)
	p.title(theme.Header, "Applying...")
	p.blank()
	p.line(" " + theme.Value.Render(
		"Restarting sshd..."))
	return p.renderWithBottomButtons(
		[]string{"Working..."}, 0, false, h)
}

// ── Result step ─────────────────────────────────────────

func (s *SSHPasswordAuthScreen) handleResultKey(
	keyStr string,
) (Screen, tea.Cmd) {
	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit
	case "enter":
		return s, closeSSHScreenCmd(s)
	case "left":
		return s, emitFocusSidebar
	case "up", "shift+tab":
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
	case "backspace":
		return s, emitFocusParent
	}
	return s, nil
}

func (s *SSHPasswordAuthScreen) viewResult(
	w, h int,
) string {
	p := newPane(w)

	if s.resultErr != "" {
		p.title(theme.Warning, "Error")
		p.warnWrap(s.resultErr)
	} else {
		p.title(theme.Success, s.resultMsg)
	}

	return p.renderWithBottomButtons(
		[]string{"Done"}, 0,
		s.ctx.ContentFocused, h)
}
