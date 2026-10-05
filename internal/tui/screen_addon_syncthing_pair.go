package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/theme"
)

// ── SyncthingPairScreen steps ──────────────────────────

type syncPairStep int

const (
	syncPairStepInput    syncPairStep = iota // device ID entry
	syncPairStepPairing                      // waiting for pair
	syncPairStepPostPair                     // success + instructions
	syncPairStepResult
)

// ── Focus zones for input step ─────────────────────────

const (
	syncPairZoneInput   = 0
	syncPairZoneButtons = 1
)

// ── SyncthingPairScreen ────────────────────────────────

type SyncthingPairScreen struct {
	attempt   uint64
	result    app.SyncthingResult
	ctx       *ScreenContext
	step      syncPairStep
	input     textinput.Model
	focusZone int // 0=input, 1=buttons
	btnIdx    int
	pairError string
	display   qrCopyDisplayState
}

func NewSyncthingPairScreen(
	ctx *ScreenContext,
) *SyncthingPairScreen {
	return &SyncthingPairScreen{
		ctx:   ctx,
		step:  syncPairStepInput,
		input: newSyncthingIDInput(),
	}
}

// ── Screen interface ────────────────────────────────────

func (s *SyncthingPairScreen) Init() tea.Cmd {
	return nil
}

func (s *SyncthingPairScreen) HandleKey(
	keyStr string, msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
	switch s.step {
	case syncPairStepInput:
		return s.handleInputKey(keyStr, msg)
	case syncPairStepPairing:
		return s.handlePairingKey(keyStr)
	case syncPairStepPostPair:
		return s.handlePostPairKey(keyStr)
	case syncPairStepResult:
		if keyStr == "enter" {
			s.step = syncPairStepInput
			s.focusZone = syncPairZoneInput
			s.input.Focus()
			return s, nil
		}
		if keyStr == "backspace" {
			return s, closeSyncthingCmd(s, s.attempt)
		}
		if keyStr == "left" {
			return s, emitFocusSidebar
		}
		if keyStr == "ctrl+c" {
			return s, tea.Quit
		}
		return s, nil
	}
	return s, nil
}

func (s *SyncthingPairScreen) HandleMsg(
	msg tea.Msg,
) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.PasteMsg:
		if s.step == syncPairStepInput &&
			s.focusZone == syncPairZoneInput {
			var cmd tea.Cmd
			s.input, cmd = s.input.Update(msg)
			return s, cmd
		}
	case syncthingPairedMsg:
		if msg.owner != s || msg.attempt != s.attempt || s.step != syncPairStepPairing {
			return s, nil
		}
		s.result = msg.result
		s.pairError = ""
		if msg.result.Err != nil {
			s.pairError = msg.result.Err.Error()
		}
		s.step = syncPairStepResult
		if msg.result.Outcome == app.SyncthingComplete {
			s.step = syncPairStepPostPair
		}
		s.btnIdx = 0

	}
	return s, nil
}

func (s *SyncthingPairScreen) View(
	w, h int,
) string {
	switch s.step {
	case syncPairStepResult:
		p := newPane(w)
		title := "Pairing Not Completed"
		if s.result.Outcome == app.SyncthingPartial {
			title = "Device Configured; Sharing Incomplete"
		}
		if s.result.Outcome == app.SyncthingUnknown {
			title = "Pairing Outcome Unconfirmed"
		}
		p.title(theme.Warning, title)
		id := s.result.DeviceID
		if id == "" {
			id = syncthingIDValue(s.input)
		}
		p.monoWrap(id)
		p.warnWrapWords(s.pairError)
		p.wrappedLines("Review the current device/share state before retrying.", theme.Dim)
		return p.renderWithBottomButtons([]string{"Review Device ID"}, 0, s.ctx.ContentFocused, h)
	case syncPairStepInput:
		return s.viewInput(w, h)
	case syncPairStepPairing:
		return s.viewPairing(w, h)
	case syncPairStepPostPair:
		return s.viewPostPair(w, h)
	}
	return ""
}

func (s *SyncthingPairScreen) HelpBindings() []key.Binding {
	switch s.step {
	case syncPairStepInput:
		return s.inputBindings()
	case syncPairStepPairing:
		binds := []key.Binding{kSidebar}
		if s.ctx.HasTabs {
			binds = append(binds, kShiftTabBar)
		}
		binds = append(binds, kQuit)
		return binds
	case syncPairStepPostPair, syncPairStepResult:
		return tabButtonBindings(s.ctx.HasTabs)
	}
	return nil
}

func (s *SyncthingPairScreen) inputBindings() []key.Binding {
	var binds []key.Binding
	if s.focusZone == syncPairZoneInput {
		binds = append(binds,
			bind("enter", "pair", "enter"),
			kTabButtons, kSidebar)
		if s.ctx.HasTabs {
			binds = append(binds, kShiftTabBar)
		}
	} else {
		binds = append(binds, kEnter)
		binds = append(binds, buttonNav(s.btnIdx)...)
		binds = append(binds, kShiftTabInput,
			kBack)
	}
	binds = append(binds, kQuit)
	return binds
}

func (s *SyncthingPairScreen) handleInputKey(
	keyStr string, msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit
	case "left":
		if s.focusZone == syncPairZoneButtons {
			if s.btnIdx > 0 {
				s.btnIdx--
				return s, nil
			}
			return s, emitFocusSidebar
		}
		if s.input.Value() != "" {
			var cmd tea.Cmd
			s.input, cmd = s.input.Update(
				tea.Msg(msg))
			return s, cmd
		}
		return s, emitFocusSidebar
	case "right":
		if s.focusZone == syncPairZoneButtons {
			if s.btnIdx < 1 {
				s.btnIdx++
			}
			return s, nil
		}
		if s.input.Value() != "" {
			var cmd tea.Cmd
			s.input, cmd = s.input.Update(
				tea.Msg(msg))
			return s, cmd
		}
		return s, nil
	case "up":
		if s.focusZone == syncPairZoneButtons {
			s.focusZone = syncPairZoneInput
			s.input.Focus()
			return s, nil
		}
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
		return s, nil
	case "shift+tab":
		if s.focusZone == syncPairZoneButtons {
			s.focusZone = syncPairZoneInput
			s.input.Focus()
			return s, nil
		}
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
		return s, nil
	case "down", "tab":
		if s.focusZone == syncPairZoneInput {
			s.focusZone = syncPairZoneButtons
			s.btnIdx = 1 // default to Pair
			s.input.Blur()
		}
		return s, nil
	case "backspace":
		if s.focusZone == syncPairZoneInput {
			var cmd tea.Cmd
			s.input, cmd = s.input.Update(
				tea.Msg(msg))
			return s, cmd
		}
		return s, emitFocusParent
	case "enter":
		if s.focusZone == syncPairZoneButtons {
			switch s.btnIdx {
			case 0: // Clear
				s.input = newSyncthingIDInput()
				s.pairError = ""
				s.focusZone = syncPairZoneInput
				return s, nil
			case 1: // Pair
				return s.submitPair()
			}
			return s, nil
		}
		// Enter in input → move to buttons
		s.focusZone = syncPairZoneButtons
		s.btnIdx = 1 // focus on Pair
		s.input.Blur()
		return s, nil
	default:
		if s.focusZone == syncPairZoneInput {
			var cmd tea.Cmd
			s.input, cmd = s.input.Update(
				tea.Msg(msg))
			return s, cmd
		}
	}
	return s, nil
}

func (s *SyncthingPairScreen) submitPair() (
	Screen, tea.Cmd,
) {
	deviceID := syncthingIDValue(s.input)
	if deviceID == "" {
		s.pairError = "Paste a Device ID"
		return s, nil
	}
	if s.step != syncPairStepInput {
		return s, nil
	}
	s.attempt++
	s.pairError = ""
	s.step = syncPairStepPairing
	return s, pairSyncthingDeviceCmd(s, deviceID)
}

// ── Pairing (in-flight) step ────────────────────────────

func (s *SyncthingPairScreen) handlePairingKey(
	keyStr string,
) (Screen, tea.Cmd) {
	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit
	case "left":
		return s, emitFocusSidebar
	case "up", "shift+tab":
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
	case "backspace", "down", "tab":
		return s, nil
	}
	return s, nil
}

// ── Post-pair step ──────────────────────────────────────

func (s *SyncthingPairScreen) handlePostPairKey(
	keyStr string,
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
		switch s.btnIdx {
		case 0: // Show QR
			vpsDeviceID := s.result.LocalID
			if vpsDeviceID == "" {
				return s, nil
			}
			return s, s.display.command(&qrCopyDisplayRequest{
				owner: s, text: vpsDeviceID, label: "Syncthing Device ID", pair: s.attempt,
			})
		case 1: // Done
			return s, closeSyncthingCmd(s, s.attempt)
		}
		return s, nil
	}
	return s, nil
}

// ── Views ───────────────────────────────────────────────

func (s *SyncthingPairScreen) viewInput(
	w, h int,
) string {
	isFocused := s.ctx.ContentFocused

	var lines []string

	// Title
	lines = append(lines, "")
	lines = append(lines,
		centerPad(
			theme.Header.Render(
				"Pair Device"), w))
	lines = append(lines, "")

	// Instructions
	lines = append(lines,
		" "+theme.Dim.Render(
			"Set up Syncthing on your local machine:"))
	lines = append(lines, "")
	lines = append(lines,
		" "+theme.Dim.Render(
			"1. Download & verify Syncthing"))
	lines = append(lines,
		"    "+theme.Mono.Render("syncthing.net"))
	lines = append(lines,
		" "+theme.Dim.Render(
			"2. \u2699 Actions \u2192"+
				" \u2699 Settings \u2192"+
				" Connections \u2192"+
				" UNCHECK ALL:"))
	lines = append(lines,
		"    "+theme.Dim.Render("☐ ")+
			theme.Value.Render("Enable NAT traversal"))
	lines = append(lines,
		"    "+theme.Dim.Render("☐ ")+
			theme.Value.Render("Global Discovery"))
	lines = append(lines,
		"    "+theme.Dim.Render("☐ ")+
			theme.Value.Render("Local Discovery"))
	lines = append(lines,
		"    "+theme.Dim.Render("☐ ")+
			theme.Value.Render("Enable Relaying"))
	lines = append(lines,
		" "+theme.Dim.Render(
			"3. \u2713 Save"))
	lines = append(lines,
		" "+theme.Dim.Render(
			"4. \u2699 Actions \u2192"+
				" Show ID \u2192 Copy"))
	lines = append(lines,
		" "+theme.Dim.Render("5. Paste below"))
	lines = append(lines, "")

	// Input
	inputFocused := isFocused &&
		s.focusZone == syncPairZoneInput
	labelStyle := theme.Header
	marker := " "
	if inputFocused {
		marker = theme.NavActive.Render("▸")
	}
	lines = append(lines,
		" "+labelStyle.Render("Your Device ID:"))
	lines = append(lines,
		marker+s.input.View())

	if s.pairError != "" {
		lines = append(lines, "")
		lines = append(lines,
			" "+theme.Warning.Render(
				s.pairError))
	}

	// Pad to push buttons to bottom
	targetH := h - 1
	for len(lines) < targetH {
		lines = append(lines, "")
	}

	// Buttons at bottom
	btnFocused := isFocused &&
		s.focusZone == syncPairZoneButtons
	lines = append(lines,
		renderButtons(
			[]string{"Clear", "Pair"},
			s.btnIdx, btnFocused, w))

	if len(lines) > h {
		lines = lines[:h]
	}

	return strings.Join(lines, "\n")
}

func (s *SyncthingPairScreen) viewPairing(
	w, h int,
) string {
	var lines []string
	lines = append(lines, "")
	lines = append(lines,
		centerPad(
			theme.Header.Render(
				"Pairing Device..."), w))
	lines = append(lines, "")
	lines = append(lines,
		" "+theme.Value.Render(
			"Adding device to Syncthing."))
	lines = append(lines, "")
	lines = append(lines,
		" "+theme.Dim.Render(
			"This may take a moment."))

	// Pad to push button to bottom
	targetH := h - 1
	for len(lines) < targetH {
		lines = append(lines, "")
	}

	lines = append(lines,
		renderButtons(
			[]string{"Pairing..."}, 0, false, w))

	if len(lines) > h {
		lines = lines[:h]
	}

	return strings.Join(lines, "\n")
}

func (s *SyncthingPairScreen) viewPostPair(
	w, h int,
) string {
	isFocused := s.ctx.ContentFocused

	var lines []string

	// Title
	lines = append(lines, "")
	lines = append(lines,
		centerPad(
			theme.Header.Render(
				"Complete Pairing"), w))
	lines = append(lines, "")

	vpsDeviceID := s.result.LocalID
	if vpsDeviceID != "" {
		lines = append(lines,
			" "+theme.Dim.Render(
				"Backup sharing was confirmed on this node."))
		lines = append(lines,
			" "+theme.Dim.Render(
				"Now add this node to your local"+
					" Syncthing:"))
		lines = append(lines, "")
		lines = append(lines,
			" "+theme.Label.Render("Node ID:"))

		// monoWrap inline
		lineW := w - 2
		if lineW < 16 {
			lineW = 16
		}
		text := vpsDeviceID
		for len(text) > 0 {
			end := lineW
			if end > len(text) {
				end = len(text)
			}
			lines = append(lines,
				" "+theme.Mono.Render(text[:end]))
			text = text[end:]
		}

		lines = append(lines, "")
		lines = append(lines,
			" "+theme.Dim.Render(
				"1. Add Remote Device"))
		lines = append(lines,
			"    "+theme.Dim.Render(
				"General \u2192 Device ID:"))
		lines = append(lines,
			"    "+theme.Dim.Render(
				"paste Node ID above"))
		lines = append(lines,
			"    "+theme.Dim.Render(
				"or press Show QR to scan"))
		lines = append(lines,
			" "+theme.Dim.Render(
				"2. Advanced \u2192"+
					" Addresses \u2192"))
		lines = append(lines,
			"    "+theme.Dim.Render(
				"replace \"dynamic\" with:"))
		lines = append(lines,
			"    "+theme.Mono.Render(
				"tcp://<your-server-ip>:22000"))
		lines = append(lines,
			" "+theme.Dim.Render(
				"3. Save \u2192"+
					" wait for connection"))
		lines = append(lines,
			" "+theme.Dim.Render(
				"4. Accept the lnd-backup"+
					" folder share"))
		lines = append(lines,
			"    "+theme.Dim.Render(
				"General \u2192"+
					" set custom Folder Path"))
		lines = append(lines,
			"    "+theme.Dim.Render(
				"Advanced \u2192"+
					" Folder Type \u2192"+
					" Receive Only"))
		lines = append(lines,
			"    "+theme.Dim.Render(
				"\u2713 Save"))
	}

	// Pad to push buttons to bottom
	targetH := h - 1
	for len(lines) < targetH {
		lines = append(lines, "")
	}

	lines = append(lines,
		renderButtons(
			[]string{"Show QR", "Done"},
			s.btnIdx, isFocused, w))

	if len(lines) > h {
		lines = lines[:h]
	}

	return strings.Join(lines, "\n")
}
