package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/autounlock"
	"github.com/virtpriv/node/internal/logger"
	"github.com/virtpriv/node/internal/theme"
)

type autoUnlockMode int

const (
	autoUnlockEnable  autoUnlockMode = iota // configure auto-unlock
	autoUnlockDisable                       // turn off existing auto-unlock
)

type autoUnlockState int

const (
	auStateForm    autoUnlockState = iota // entering passwords / confirm
	auStateRunning                        // application observation in flight
	auStateDoneOK                         // completed host result
	auStateDoneErr                        // failed or unknown result
)

const (
	auZoneInput1  = 0 // password 1 input
	auZoneInput2  = 1 // password 2 input
	auZoneButtons = 2 // Cancel/Confirm buttons
)

type autoUnlockDoneMsg struct {
	owner   *AutoUnlockScreen
	attempt uint64
	result  app.AutoUnlockResult
}

type closeAutoUnlockMsg struct {
	owner   *AutoUnlockScreen
	attempt uint64
}

// AutoUnlockScreen owns password input, confirmation and the result of one
// enable/disable attempt. The application observes the independent root change.
type AutoUnlockScreen struct {
	ctx         *ScreenContext
	mode        autoUnlockMode
	attempt     uint64
	observation app.AutoUnlockObservation

	// Form / interaction state
	state     autoUnlockState
	focusZone int
	btnIdx    int // 0 = Cancel/Skip, 1 = Confirm/Disable

	// Enable mode uses two masked password inputs.
	pw1 textinput.Model
	pw2 textinput.Model

	// Inline error string (e.g. "Passwords do not match")
	errMsg string

	// Final verified host result, available only after successful observation.
	result autounlock.Result
}

func NewAutoUnlockScreen(
	ctx *ScreenContext,
) *AutoUnlockScreen {
	mode := autoUnlockEnable
	if ctx.Cfg.AutoUnlock {
		mode = autoUnlockDisable
	}

	s := &AutoUnlockScreen{
		ctx:    ctx,
		mode:   mode,
		state:  auStateForm,
		btnIdx: 1, // default focus on Confirm
	}

	if mode == autoUnlockEnable {
		s.pw1 = newAutoUnlockPwInput()
		s.pw2 = newAutoUnlockPwInput()
		s.focusZone = auZoneInput1
		s.pw1.Focus()
	} else {
		// Disable mode has no inputs, focus goes
		// straight to the buttons.
		s.focusZone = auZoneButtons
	}

	return s
}

func newAutoUnlockPwInput() textinput.Model {
	ti := textinput.New()
	ti.Placeholder = ""
	ti.CharLimit = 256
	ti.SetWidth(40)
	ti.EchoMode = textinput.EchoPassword
	ti.EchoCharacter = '•'
	ti.Prompt = "  "
	applyInputStyles(&ti)
	return ti
}

// ── Screen interface ────────────────────────────────────

func (s *AutoUnlockScreen) Init() tea.Cmd {
	if s.mode == autoUnlockEnable {
		return s.pw1.Focus()
	}
	return nil
}

func (s *AutoUnlockScreen) HandleKey(
	keyStr string, msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
	// Navigation keeps the owning tab reachable while the operation runs.
	if s.state == auStateRunning {
		switch keyStr {
		case "ctrl+c":
			return s, tea.Quit
		case "left":
			return s, emitFocusSidebar
		case "up", "shift+tab":
			if s.ctx.HasTabs {
				return s, emitFocusTabBar
			}
		}
		return s, nil
	}

	// Done states
	if s.state == auStateDoneOK ||
		s.state == auStateDoneErr {
		if s.state == auStateDoneOK &&
			s.result.Outcome == autounlock.Disabled {
			switch keyStr {
			case "ctrl+c":
				return s, tea.Quit
			case "left":
				if s.btnIdx > 0 {
					s.btnIdx--
					return s, nil
				}
				return s, emitFocusSidebar
			case "right":
				if s.btnIdx < 1 {
					s.btnIdx++
				}
				return s, nil
			case "enter":
				if s.btnIdx == 1 {
					s.resetToEnableForm()
					return s, s.pw1.Focus()
				}
				return s, s.closeCommand()
			case "up", "shift+tab":
				if s.ctx.HasTabs {
					return s, emitFocusTabBar
				}
			case "backspace":
				return s, emitFocusParent
			}
			return s, nil
		}
		switch keyStr {
		case "ctrl+c":
			return s, tea.Quit
		case "enter":
			return s, s.closeCommand()
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

	// Form state is determined by the current setting.
	if s.mode == autoUnlockDisable {
		return s.handleDisableKey(keyStr, msg)
	}
	return s.handleEnableKey(keyStr, msg)
}

// ── Disable mode key handling ───────────────────────────
// Disable mode has no inputs. Focus stays on the buttons.

func (s *AutoUnlockScreen) handleDisableKey(
	keyStr string, msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit
	case "left":
		if s.btnIdx > 0 {
			s.btnIdx--
			return s, nil
		}
		return s, emitFocusSidebar
	case "right":
		if s.btnIdx < 1 {
			s.btnIdx++
		}
		return s, nil
	case "up":
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
		return s, nil
	case "enter":
		if s.btnIdx == 0 {
			return s, s.closeCommand()
		}
		// Disable
		return s, s.startOperation(autounlock.Password{})
	case "backspace":
		return s, emitFocusParent
	}
	return s, nil
}

// ── Enable mode key handling ────────────────────────────
// Three focus zones: pw1, pw2, buttons.

func (s *AutoUnlockScreen) handleEnableKey(
	keyStr string, msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit

	case "left":
		// On buttons: move between buttons, or
		// to sidebar from leftmost button.
		if s.focusZone == auZoneButtons {
			if s.btnIdx > 0 {
				s.btnIdx--
				return s, nil
			}
			return s, emitFocusSidebar
		}
		// On inputs: pass through for cursor movement
		return s, s.passthroughInput(msg)

	case "right":
		if s.focusZone == auZoneButtons {
			if s.btnIdx < 1 {
				s.btnIdx++
			}
			return s, nil
		}
		return s, s.passthroughInput(msg)

	case "up":
		if s.focusZone == auZoneInput2 {
			s.focusZone = auZoneInput1
			s.pw2.Blur()
			s.pw1.Focus()
			return s, nil
		}
		if s.focusZone == auZoneButtons {
			s.focusZone = auZoneInput2
			s.pw2.Focus()
			return s, nil
		}
		// On pw1: go to tab bar if available
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
		return s, nil

	case "down", "tab":
		if s.focusZone == auZoneInput1 {
			s.focusZone = auZoneInput2
			s.pw1.Blur()
			s.pw2.Focus()
			return s, nil
		}
		if s.focusZone == auZoneInput2 {
			s.focusZone = auZoneButtons
			s.pw2.Blur()
			return s, nil
		}
		return s, nil

	case "shift+tab":
		if s.focusZone == auZoneButtons {
			s.focusZone = auZoneInput2
			s.pw2.Focus()
			return s, nil
		}
		if s.focusZone == auZoneInput2 {
			s.focusZone = auZoneInput1
			s.pw2.Blur()
			s.pw1.Focus()
			return s, nil
		}
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
		return s, nil

	case "backspace":
		// On inputs: delete characters
		if s.focusZone == auZoneInput1 ||
			s.focusZone == auZoneInput2 {
			return s, s.passthroughInput(msg)
		}
		// On buttons: navigate to parent
		return s, emitFocusParent

	case "enter":
		if s.focusZone == auZoneInput1 {
			// Advance to second password
			s.focusZone = auZoneInput2
			s.pw1.Blur()
			s.pw2.Focus()
			return s, nil
		}
		if s.focusZone == auZoneInput2 {
			// Advance to buttons
			s.focusZone = auZoneButtons
			s.pw2.Blur()
			return s, nil
		}
		// Buttons zone
		if s.btnIdx == 0 {
			return s, s.closeCommand()
		}
		return s.tryConfirm()

	default:
		return s, s.passthroughInput(msg)
	}
}

// passthroughInput forwards a key press to whichever
// input currently has focus. Returns the resulting cmd.
// Clear the prior error because the user is
// editing, so the error is no longer current.
func (s *AutoUnlockScreen) passthroughInput(
	msg tea.KeyPressMsg,
) tea.Cmd {
	s.errMsg = ""
	var cmd tea.Cmd
	switch s.focusZone {
	case auZoneInput1:
		s.pw1, cmd = s.pw1.Update(msg)
	case auZoneInput2:
		s.pw2, cmd = s.pw2.Update(msg)
	}
	return cmd
}

// tryConfirm validates the two password inputs and, if
// they pass, starts the owned application operation. On
// validation failure, sets errMsg and refocuses the
// first input.
func (s *AutoUnlockScreen) tryConfirm() (
	Screen, tea.Cmd,
) {
	if s.state != auStateForm {
		return s, nil
	}
	pw1 := s.pw1.Value()
	pw2 := s.pw2.Value()

	if pw1 == "" {
		s.errMsg = "Password cannot be empty"
		s.refocusFirstInput()
		return s, nil
	}
	if pw1 != pw2 {
		s.errMsg = "Passwords do not match"
		s.pw1.SetValue("")
		s.pw2.SetValue("")
		s.refocusFirstInput()
		return s, nil
	}

	password, err := autounlock.NewPassword(pw1)
	if err != nil {
		s.errMsg = err.Error()
		s.refocusFirstInput()
		return s, nil
	}
	return s, s.startOperation(password)
}

func (s *AutoUnlockScreen) refocusFirstInput() {
	s.focusZone = auZoneInput1
	s.pw2.Blur()
	s.pw1.Focus()
}

func (s *AutoUnlockScreen) resetToEnableForm() {
	s.attempt++
	s.mode = autoUnlockEnable
	s.state = auStateForm
	s.result = autounlock.Result{}
	s.errMsg = ""
	s.pw1 = newAutoUnlockPwInput()
	s.pw2 = newAutoUnlockPwInput()
	s.focusZone = auZoneInput1
	s.btnIdx = 1
	s.pw1.Focus()
}

// ── HandleMsg ───────────────────────────────────────────

func (s *AutoUnlockScreen) HandleMsg(
	msg tea.Msg,
) (Screen, tea.Cmd) {
	switch m := msg.(type) {
	case autoUnlockDoneMsg:
		if m.owner != s || m.attempt != s.attempt || s.state != auStateRunning {
			return s, nil
		}
		s.observation = m.result.Observation
		s.result = m.result.Transition
		if m.result.Observation != app.AutoUnlockObserved {
			s.state = auStateDoneErr
			if m.result.Err != nil {
				logger.TUI("auto-unlock observation: %v", m.result.Err)
			}
			if m.result.Observation == app.AutoUnlockNotStarted && m.result.Err != nil {
				s.errMsg = m.result.Err.Error()
			}
			return s, nil
		}
		switch s.result.Outcome {
		case autounlock.Enabled, autounlock.StillEnabled:
			s.ctx.Cfg.AutoUnlock = true
			s.state = auStateDoneOK
			return s, func() tea.Msg { return refreshStatusMsg{} }
		case autounlock.Disabled:
			s.ctx.Cfg.AutoUnlock = false
			s.state = auStateDoneOK
			s.btnIdx = 0
			return s, func() tea.Msg { return refreshStatusMsg{} }
		case autounlock.VerificationFailed, autounlock.VerificationTimedOut:
			s.state = auStateForm
			s.refocusFirstInput()
			if s.result.Outcome == autounlock.VerificationTimedOut {
				s.errMsg = "LND did not become ready within 120 seconds. VPN could not determine whether the password was correct. LND has been returned to the locked state."
			} else if s.result.Detail != "" {
				s.errMsg = s.result.Detail
			} else {
				s.errMsg = "VPN could not verify that password. LND is locked. Check the password and try again."
			}
		default:
			s.state = auStateDoneErr
		}
		return s, nil
	case tea.PasteMsg:
		if s.mode != autoUnlockEnable ||
			s.state != auStateForm {
			return s, nil
		}
		// Only the password manager's one trailing LF may be removed. Compare
		// the original value with the widget result before accepting masked input.
		value := strings.TrimSuffix(m.Content, "\n")
		var input *textinput.Model
		switch s.focusZone {
		case auZoneInput1:
			input = &s.pw1
		case auZoneInput2:
			input = &s.pw2
		default:
			return s, nil
		}
		input.SetValue(value)
		if input.Value() != value {
			input.Reset()
			s.errMsg = "Paste rejected and field cleared: unsupported characters or more than 256 characters"
		} else {
			s.errMsg = ""
		}

		return s, nil
	}
	return s, nil
}

func (s *AutoUnlockScreen) startOperation(password autounlock.Password) tea.Cmd {
	s.state = auStateRunning
	s.attempt++
	s.errMsg = ""
	s.result = autounlock.Result{}
	s.pw1.Reset()
	s.pw2.Reset()
	var results <-chan app.AutoUnlockResult
	if s.mode == autoUnlockEnable {
		results = s.ctx.autoUnlock().Enable(password)
	} else {
		results = s.ctx.autoUnlock().Disable()
	}
	attempt := s.attempt
	return func() tea.Msg { return autoUnlockDoneMsg{owner: s, attempt: attempt, result: <-results} }
}

func (s *AutoUnlockScreen) closeCommand() tea.Cmd {
	attempt := s.attempt
	return func() tea.Msg { return closeAutoUnlockMsg{owner: s, attempt: attempt} }
}

func autoUnlockBusy(screen Screen) bool {
	s, ok := screen.(*AutoUnlockScreen)
	return ok && s.state == auStateRunning
}

// ── View ────────────────────────────────────────────────

func (s *AutoUnlockScreen) View(
	w, h int,
) string {
	switch s.state {
	case auStateRunning:
		return s.viewRunning(w, h)
	case auStateDoneOK:
		return s.viewDone(w, h)
	case auStateDoneErr:
		return s.viewError(w, h)
	}
	if s.mode == autoUnlockDisable {
		return s.viewDisable(w, h)
	}
	return s.viewEnable(w, h)
}

func (s *AutoUnlockScreen) viewEnable(
	w, h int,
) string {
	isFocused := s.ctx.ContentFocused
	p := newPane(w)

	p.title(theme.Header,
		"Configure Auto-Unlock")

	p.valueWrap("LND requires your wallet password to unlock the wallet on every startup.")
	p.blank()
	p.valueWrap("Auto-unlock stores your password in a permission-locked file owned by the LND service user, so LND can unlock itself automatically after a reboot.")
	p.blank()
	p.warnWrapWords("This server restarts by itself when a security update needs it. Without auto-unlock, LND then stays offline. Configure auto-unlock now by typing in your password.")
	p.blank()
	p.valueWrap("Enter the SAME password you used when creating your wallet (NOT YOUR 24 WORD SEED, not the optional seed passphrase).")
	p.blank()

	p.input("Wallet password:",
		s.pw1.View(),
		isFocused && s.focusZone == auZoneInput1)
	if len(s.pw1.Value()) > 0 {
		p.dim(fmt.Sprintf("(%d chars)", len(s.pw1.Value())))
	}
	p.blank()
	p.input("Confirm password:",
		s.pw2.View(),
		isFocused && s.focusZone == auZoneInput2)
	if len(s.pw2.Value()) > 0 {
		p.dim(fmt.Sprintf("(%d chars)", len(s.pw2.Value())))
	}

	p.appendError(s.errMsg)

	return p.renderWithBottomButtons(
		[]string{"Skip", "Confirm"},
		s.btnIdx,
		isFocused && s.focusZone == auZoneButtons, h)
}

func (s *AutoUnlockScreen) viewDisable(
	w, h int,
) string {
	isFocused := s.ctx.ContentFocused
	p := newPane(w)

	p.title(theme.Header,
		"Disable Auto-Unlock")

	p.line(" " + theme.Value.Render(
		"Auto-unlock is currently enabled."))
	p.blank()
	p.line(" " + theme.Value.Render(
		"Disabling it will:"))
	p.line(" " + theme.Value.Render(
		"  • Remove the stored wallet password"))
	p.line(" " + theme.Value.Render(
		"  • Restart LND"))
	p.line(" " + theme.Value.Render(
		"  • Require manual unlock after every"))
	p.line(" " + theme.Value.Render(
		"    reboot (run: lncli unlock)"))
	p.blank()
	p.warnWrapWords("This server restarts by itself when a security update needs it. Until you unlock LND manually after a reboot, no Lightning operations will work.")

	return p.renderWithBottomButtons(
		[]string{"Cancel", "Disable"},
		s.btnIdx, isFocused, h)
}

func (s *AutoUnlockScreen) viewRunning(
	w, h int,
) string {
	p := newPane(w)
	if s.mode == autoUnlockDisable {
		p.title(theme.Header,
			"Disabling Auto-Unlock")
		p.blank()
		p.dim("Restarting LND and proving it is locked...")
	} else {
		p.title(theme.Header,
			"Configuring Auto-Unlock")
		p.blank()
		p.dim("Restarting LND and verifying the password...")
	}
	return p.render()
}

func (s *AutoUnlockScreen) viewDone(
	w, h int,
) string {
	isFocused := s.ctx.ContentFocused
	p := newPane(w)

	if s.result.Outcome == autounlock.StillEnabled {
		p.title(theme.Header,
			"Auto-Unlock Still Enabled")
		p.line(" " + theme.Warning.Render(
			"Disabling auto-unlock failed."))
		p.line(" " + theme.Good.Render(
			"The previous enabled state was restored,"))
		p.line(" " + theme.Good.Render(
			"and LND is online."))
	} else if s.mode == autoUnlockDisable {
		p.title(theme.Header,
			"Auto-Unlock Disabled")
		p.line(" " + theme.Good.Render(
			"Auto-unlock has been turned off."))
		p.blank()
		p.line(" " + theme.Warning.Render(
			"LND is currently locked."))
		p.blank()
		p.line(" " + theme.Value.Render(
			"To bring LND back online, either"))
		p.line(" " + theme.Value.Render(
			"re-enable auto-unlock from this"))
		p.line(" " + theme.Value.Render(
			"screen, or run:"))
		p.mono("lncli unlock")
	} else {
		p.title(theme.Header,
			"Auto-Unlock Configured")
		p.line(" " + theme.Good.Render(
			"Your wallet will now unlock"))
		p.line(" " + theme.Good.Render(
			"automatically on every reboot."))
	}

	buttons := []string{"Done"}
	if s.result.Outcome == autounlock.Disabled {
		buttons = []string{"Done", "Re-enable"}
	}
	return p.renderWithBottomButtons(
		buttons, s.btnIdx, isFocused, h)
}

func (s *AutoUnlockScreen) viewError(
	w, h int,
) string {
	isFocused := s.ctx.ContentFocused
	p := newPane(w)

	switch s.observation {
	case app.AutoUnlockUnknown:
		p.title(theme.Header, "Auto-Unlock Change Not Confirmed")
		p.blank()
		p.warnWrap("The change may have completed, or the helper may still finish it. Do not assume LND is online or that auto-unlock is correctly configured. Have your administrator check the final state before retrying.")
	case app.AutoUnlockNotStarted:
		p.title(theme.Header, "Auto-Unlock Change Not Started")
		p.blank()
		p.warnWrap("The request was not sent to the helper. Reopen the TUI before trying again.")
	default:
		p.title(theme.Header, "Repair Required")
		p.blank()
		p.warnWrap("VPN could not prove the auto-unlock state due to a system failure. Do not assume LND is online or that auto-unlock is correctly configured.")
		if s.result.FailedStep != "" {
			p.blank()
			p.warnWrap("Failed step: " + s.result.FailedStep)
		}
	}

	return p.renderWithBottomButtons(
		[]string{"Done"}, 0, isFocused, h)
}

// ── HelpBindings ────────────────────────────────────────

func (s *AutoUnlockScreen) HelpBindings() []key.Binding {
	if s.state == auStateRunning {
		bindings := []key.Binding{kSidebar}
		if s.ctx.HasTabs {
			bindings = append(bindings, kShiftTabBar)
		}
		return append(bindings, kQuit)
	}
	if s.state == auStateDoneOK ||
		s.state == auStateDoneErr {
		if s.state == auStateDoneOK &&
			s.result.Outcome == autounlock.Disabled {
			return actionButtonBindings(s.btnIdx, s.ctx.HasTabs)
		}
		return resultBindings(s.ctx.HasTabs)
	}

	if s.mode == autoUnlockDisable {
		return s.disableButtonBindings()
	}
	return s.enableBindings()
}

func (s *AutoUnlockScreen) enableBindings() []key.Binding {
	if s.focusZone == auZoneInput1 {
		binds := []key.Binding{
			kEnterNext,
			kTabNextField,
		}
		if s.ctx.HasTabs {
			binds = append(binds, kShiftTabBar)
		}
		binds = append(binds, kQuit)
		return binds
	}
	if s.focusZone == auZoneInput2 {
		return []key.Binding{
			kEnterNext,
			kTabButtons,
			bind("⇧tab", "prev field", "shift+tab"),
			kQuit,
		}
	}

	// Button zone
	binds := buttonNav(s.btnIdx)
	binds = append(binds,
		kEnter,
		bind("⇧tab", "fields", "shift+tab"))
	if s.ctx.HasTabs {
		binds = append(binds, kUpTabBar)
	}
	binds = append(binds, kBack, kQuit)
	return binds
}

func (s *AutoUnlockScreen) disableButtonBindings() []key.Binding {
	return actionButtonBindings(s.btnIdx, s.ctx.HasTabs)
}
