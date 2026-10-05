package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/theme"
)

// ── SSHKeyAddScreen ────────────────────────────────────
// Paste-a-key flow opened as its own tab from
// SSHKeysScreen. Three steps: input → working → result.
// On result Done, closes the tab and refreshes the
// parent list.

type sshAddStep int

const (
	sshAddStepInput sshAddStep = iota
	sshAddStepWorking
	sshAddStepResult
)

const (
	sshAddZoneButtons = 0
	sshAddZoneInput   = 2 // distinct from buttons (see decision 92)
)

type SSHKeyAddScreen struct {
	attempt   uint64
	ctx       *ScreenContext
	step      sshAddStep
	keyInput  textinput.Model
	focusZone int
	btnIdx    int
	addErr    string
	resultMsg string
	resultErr string
}

func NewSSHKeyAddScreen(
	ctx *ScreenContext,
) *SSHKeyAddScreen {
	in := newSSHKeyInput()
	in.Focus()
	return &SSHKeyAddScreen{
		ctx:       ctx,
		step:      sshAddStepInput,
		keyInput:  in,
		focusZone: sshAddZoneInput,
	}
}

// ── Screen interface ────────────────────────────────────

func (s *SSHKeyAddScreen) Init() tea.Cmd { return nil }

func (s *SSHKeyAddScreen) HandleKey(
	keyStr string, msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
	switch s.step {
	case sshAddStepInput:
		return s.handleInputKey(keyStr, msg)
	case sshAddStepWorking:
		if keyStr == "ctrl+c" {
			return s, tea.Quit
		}
	case sshAddStepResult:
		return s.handleResultKey(keyStr)
	}
	return s, nil
}

func (s *SSHKeyAddScreen) HandleMsg(
	msg tea.Msg,
) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case sshKeyAddMsg:
		if msg.owner != s || msg.attempt != s.attempt || s.step != sshAddStepWorking {
			return s, nil
		}
		s.step = sshAddStepResult
		if msg.err != nil {
			s.resultErr = msg.err.Error()
			s.resultMsg = ""
		} else {
			s.resultMsg = "Key added successfully"
			s.resultErr = ""
		}
		return s, refreshSSHKeysCmd

	case tea.PasteMsg:
		if s.step == sshAddStepInput &&
			s.focusZone == sshAddZoneInput {
			line := strings.TrimSpace(msg.Content)
			if strings.ContainsAny(line, "\r\n") {
				s.keyInput.SetValue("")
				s.addErr = "Paste one public key at a time"
				return s, nil
			}
			s.keyInput.SetValue(line)
		}
		return s, nil
	}
	return s, nil
}

func (s *SSHKeyAddScreen) View(w, h int) string {
	switch s.step {
	case sshAddStepInput:
		return s.viewInput(w, h)
	case sshAddStepWorking:
		return s.viewWorking(w, h)
	case sshAddStepResult:
		return s.viewResult(w, h)
	}
	return ""
}

func (s *SSHKeyAddScreen) HelpBindings() []key.Binding {
	switch s.step {
	case sshAddStepInput:
		return s.inputBindings()
	case sshAddStepWorking:
		return waitingBindings()
	case sshAddStepResult:
		return resultBindings(s.ctx.HasTabs)
	}
	return nil
}

// ── Input step ──────────────────────────────────────────

func (s *SSHKeyAddScreen) handleInputKey(
	keyStr string, msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit

	case "up":
		if s.focusZone == sshAddZoneButtons {
			s.focusZone = sshAddZoneInput
			s.keyInput.Focus()
			return s, nil
		}
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
		return s, nil

	case "down":
		if s.focusZone == sshAddZoneInput {
			s.focusZone = sshAddZoneButtons
			s.keyInput.Blur()
			s.btnIdx = 1 // default to Add
		}
		return s, nil

	case "tab":
		if s.focusZone == sshAddZoneInput {
			s.focusZone = sshAddZoneButtons
			s.keyInput.Blur()
			s.btnIdx = 1
		}
		return s, nil

	case "shift+tab":
		if s.focusZone == sshAddZoneButtons {
			s.focusZone = sshAddZoneInput
			s.keyInput.Focus()
		} else if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
		return s, nil

	case "left":
		if s.focusZone == sshAddZoneButtons &&
			s.btnIdx > 0 {
			s.btnIdx--
			return s, nil
		}
		if s.focusZone == sshAddZoneInput {
			if s.keyInput.Value() != "" {
				var cmd tea.Cmd
				s.keyInput, cmd =
					s.keyInput.Update(tea.Msg(msg))
				return s, cmd
			}
		}
		return s, emitFocusSidebar

	case "right":
		if s.focusZone == sshAddZoneButtons &&
			s.btnIdx < 1 {
			s.btnIdx++
			return s, nil
		}
		if s.focusZone == sshAddZoneInput {
			var cmd tea.Cmd
			s.keyInput, cmd =
				s.keyInput.Update(tea.Msg(msg))
			return s, cmd
		}
		return s, nil

	case "backspace":
		if s.focusZone == sshAddZoneInput {
			var cmd tea.Cmd
			s.keyInput, cmd =
				s.keyInput.Update(tea.Msg(msg))
			return s, cmd
		}
		return s, emitFocusParent

	case "enter":
		if s.focusZone == sshAddZoneButtons {
			switch s.btnIdx {
			case 0: // Cancel
				return s, closeSSHScreenCmd(s)
			case 1: // Add
				return s.submit()
			}
			return s, nil
		}
		// Enter in input — advance to buttons
		s.focusZone = sshAddZoneButtons
		s.keyInput.Blur()
		s.btnIdx = 1
		return s, nil

	default:
		if s.focusZone == sshAddZoneInput {
			var cmd tea.Cmd
			s.keyInput, cmd =
				s.keyInput.Update(tea.Msg(msg))
			return s, cmd
		}
	}
	return s, nil
}

func (s *SSHKeyAddScreen) submit() (Screen, tea.Cmd) {
	value := strings.TrimSpace(s.keyInput.Value())
	if value == "" {
		s.addErr = "Paste a public key"
		return s, nil
	}
	if _, err := app.ParseSSHKey(value); err != nil {
		s.addErr = "Invalid SSH key: " + err.Error()
		return s, nil
	}
	s.addErr = ""
	s.step = sshAddStepWorking
	return s, s.addCommand(value)
}

func (s *SSHKeyAddScreen) viewInput(w, h int) string {
	p := newPane(w)
	p.title(theme.Header, "Add SSH Key")
	p.blank()
	p.dim("On your local machine, run in Terminal:")
	p.blank()
	p.mono("ssh-keygen -t ed25519 -f ~/.ssh/virtual-private-node-key -C vpn")
	p.blank()
	p.dim("Generate a passphrase with a password")
	p.dim("manager. You will enter it twice.")
	p.blank()
	p.dim("Then display your new public key to copy:")
	p.blank()
	p.mono("cat ~/.ssh/virtual-private-node-key.pub")
	p.blank()
	p.dim("Paste the output below.")
	p.blank()

	isFocused := s.ctx.ContentFocused
	inputFocused := isFocused &&
		s.focusZone == sshAddZoneInput
	p.input("Public Key:", s.keyInput.View(), inputFocused)

	p.appendError(s.addErr)

	btnFocused := isFocused &&
		s.focusZone == sshAddZoneButtons
	return p.renderWithBottomButtons(
		[]string{"Cancel", "Add"}, s.btnIdx,
		btnFocused, h)
}

func (s *SSHKeyAddScreen) inputBindings() []key.Binding {
	var binds []key.Binding

	if s.focusZone == sshAddZoneButtons &&
		!s.keyInput.Focused() {
		binds = append(binds, buttonNav(s.btnIdx)...)
		binds = append(binds,
			kEnter,
			kShiftTabInput,
			kBack)
	} else {
		binds = append(binds,
			kTabButtons,
			bind("enter", "add", "enter"),
			kSidebar)
		if s.ctx.HasTabs {
			binds = append(binds, kShiftTabBar)
		}
	}

	binds = append(binds, kQuit)
	return binds
}

// ── Working step ────────────────────────────────────────

func (s *SSHKeyAddScreen) viewWorking(
	w, h int,
) string {
	p := newPane(w)
	p.title(theme.Header, "Adding key")
	p.blank()
	p.line(" " + theme.Value.Render("Working..."))
	return p.renderWithBottomButtons(
		[]string{"Working..."}, 0, false, h)
}

// ── Result step ─────────────────────────────────────────

func (s *SSHKeyAddScreen) handleResultKey(
	keyStr string,
) (Screen, tea.Cmd) {
	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit
	case "enter":
		// The parent list was refreshed when the operation completed.
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

func (s *SSHKeyAddScreen) viewResult(
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
