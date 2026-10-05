package tui

import (
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/lndrpc"
	"github.com/virtpriv/node/internal/theme"
)

// ── Send screen steps ────────────────────────────────────

type sendStep int

const (
	sendStepInput    sendStep = iota // paste/type pay req
	sendStepConfirm                  // decoded details, Go Back + Confirm
	sendStepInFlight                 // payment routing
	sendStepResult                   // success or error
)

// ── Input focus zones ───────────────────────────────────

const (
	sendZoneInput   = 0
	sendZoneButtons = 1
)

// ── SendScreen ───────────────────────────────────────────

// Each submission has its own identity, including across closed/reopened tabs.
// The request makes this a non-zero-sized object with a distinct address.
type paymentAttempt struct {
	request app.PaymentRequest
}

type SendScreen struct {
	ctx      *ScreenContext
	step     sendStep
	payments app.LightningPaymentClient
	attempt  *paymentAttempt
	prepared app.PreparedPayment

	// Input state
	sendInput   textinput.Model
	inputBtnIdx int // 0=Clear, 1=Send
	focusZone   int // 0=input field, 1=buttons
	inputError  string

	// Confirm state
	confirmBtnIdx int // 0=Go Back, 1=Confirm

	// Result state
	result lndrpc.SendPaymentResult
}

func NewSendScreen(
	ctx *ScreenContext,
) *SendScreen {
	var payments app.LightningPaymentClient
	if ctx.LndClient != nil {
		payments = ctx.LndClient
	}
	return &SendScreen{
		payments:    payments,
		ctx:         ctx,
		step:        sendStepInput,
		sendInput:   newSendPayReqInput(ctx.Cfg.Network),
		inputBtnIdx: 1, // default to Send
	}
}

// ── Screen interface ────────────────────────────────────

func (s *SendScreen) Init() tea.Cmd {
	return nil
}

func (s *SendScreen) HandleKey(
	keyStr string, msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
	switch s.step {
	case sendStepInput:
		return s.handleInputKey(keyStr, msg)
	case sendStepConfirm:
		return s.handleConfirmKey(keyStr)
	case sendStepInFlight:
		return s.handleInFlightKey(keyStr)
	case sendStepResult:
		return s.handleResultKey(keyStr)
	}
	return s, nil
}

func (s *SendScreen) HandleMsg(
	msg tea.Msg,
) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.PasteMsg:
		return s.handlePaste(msg)
	case payReqDecodedMsg:
		return s.handlePayReqDecoded(msg)
	case sendPaymentResultMsg:
		return s.handleSendResult(msg)
	}
	return s, nil
}

func (s *SendScreen) View(w, h int) string {
	switch s.step {
	case sendStepInput:
		return s.viewInput(w, h)
	case sendStepConfirm:
		return s.viewConfirm(w, h)
	case sendStepInFlight:
		return s.viewInFlight(w, h)
	case sendStepResult:
		return s.viewResult(w, h)
	}
	return ""
}

func (s *SendScreen) HelpBindings() []key.Binding {
	switch s.step {
	case sendStepInput:
		return s.inputBindings()
	case sendStepConfirm:
		return actionButtonBindings(
			s.confirmBtnIdx, s.ctx.HasTabs)
	case sendStepInFlight:
		return inFlightBindings()
	case sendStepResult:
		return resultBindings(s.ctx.HasTabs)
	}
	return nil
}

// ── Input step ──────────────────────────────────────────

func (s *SendScreen) handleInputKey(
	keyStr string, msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit

	case "left":
		// Buttons: move left
		if s.focusZone == sendZoneButtons &&
			s.inputBtnIdx > 0 {
			s.inputBtnIdx--
			return s, nil
		}
		// Text input: pass through for cursor
		if s.focusZone == sendZoneInput {
			if s.sendInput.Value() != "" {
				var cmd tea.Cmd
				s.sendInput, cmd =
					s.sendInput.Update(
						tea.Msg(msg))
				return s, cmd
			}
		}
		return s, emitFocusSidebar

	case "right":
		// Buttons: move right
		if s.focusZone == sendZoneButtons &&
			s.inputBtnIdx < 1 {
			s.inputBtnIdx++
			return s, nil
		}
		// Text input: pass through for cursor
		if s.focusZone == sendZoneInput {
			var cmd tea.Cmd
			s.sendInput, cmd =
				s.sendInput.Update(tea.Msg(msg))
			return s, cmd
		}
		return s, nil

	case "backspace":
		if s.focusZone == sendZoneInput {
			var cmd tea.Cmd
			s.sendInput, cmd =
				s.sendInput.Update(tea.Msg(msg))
			return s, cmd
		}
		return s, emitFocusParent

	case "tab":
		// Express forward jump between zones
		if s.focusZone < sendZoneButtons {
			s.focusZone = sendZoneButtons
			s.sendInput.Blur()
			s.inputBtnIdx = 1 // default to Send
		}
		return s, nil

	case "shift+tab":
		// Express backward jump between zones
		if s.focusZone > sendZoneInput {
			s.focusZone = sendZoneInput
			s.sendInput.Focus()
		} else if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
		return s, nil

	case "down":
		// Move to buttons at boundary
		if s.focusZone < sendZoneButtons {
			s.focusZone = sendZoneButtons
			s.sendInput.Blur()
			s.inputBtnIdx = 1
		}
		return s, nil

	case "up":
		// Move to input at boundary
		if s.focusZone > sendZoneInput {
			s.focusZone = sendZoneInput
			s.sendInput.Focus()
			return s, nil
		}
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
		return s, nil

	case "enter":
		// Buttons
		if s.focusZone == sendZoneButtons {
			switch s.inputBtnIdx {
			case 0: // Clear
				s.sendInput = newSendPayReqInput(s.ctx.Cfg.Network)
				s.inputError = ""
				s.focusZone = sendZoneInput
				return s, nil
			case 1: // Send
				return s.preparePaymentConfirmation()
			}
			return s, nil
		}
		// Enter in input field → advance to buttons
		s.focusZone = sendZoneButtons
		s.sendInput.Blur()
		s.inputBtnIdx = 1 // default to Send
		return s, nil

	default:
		if s.focusZone == sendZoneInput {
			var cmd tea.Cmd
			s.sendInput, cmd =
				s.sendInput.Update(tea.Msg(msg))
			return s, cmd
		}
	}
	return s, nil
}

// preparePaymentConfirmation parses input and schedules invoice decoding for
// review. Only the later confirmation schedules payment submission.
func (s *SendScreen) preparePaymentConfirmation() (Screen, tea.Cmd) {
	if !s.ctx.walletExists() {
		return s, nil
	}
	s.attempt = nil
	request, err := app.ParseLightningPayment(s.ctx.Cfg.Network, s.sendInput.Value())
	if err != nil {
		s.inputError = err.Error()
		return s, nil
	}
	s.sendInput.SetValue(request.Invoice())
	s.inputError = ""
	s.attempt = &paymentAttempt{request: request}
	return s, preparePaymentCmd(s.payments, s.attempt)
}

// ── Confirm step ────────────────────────────────────────

func (s *SendScreen) handleConfirmKey(
	keyStr string,
) (Screen, tea.Cmd) {
	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit
	case "left":
		if s.confirmBtnIdx > 0 {
			s.confirmBtnIdx--
		} else {
			return s, emitFocusSidebar
		}
		return s, nil
	case "right":
		if s.confirmBtnIdx < 1 {
			s.confirmBtnIdx++
		}
		return s, nil
	case "up":
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
		return s, nil
	case "down", "tab", "shift+tab":
		return s, nil
	case "backspace":
		s.backToInput()
		return s, nil
	case "enter":
		switch s.confirmBtnIdx {
		case 0: // Go Back
			s.backToInput()
			return s, nil
		case 1: // Confirm
			if !s.ctx.walletExists() {
				return s, nil
			}
			s.step = sendStepInFlight
			return s, sendPaymentCmd(
				s.payments, s.attempt, s.prepared)
		}
	}
	return s, nil
}

// backToInput returns to the input step, clearing
// decoded state and error.
func (s *SendScreen) backToInput() {
	s.attempt = nil
	s.prepared = app.PreparedPayment{}
	s.step = sendStepInput
	s.inputError = ""
	s.confirmBtnIdx = 0
	s.focusZone = sendZoneInput
	s.sendInput.Focus()
}

// ── InFlight step ───────────────────────────────────────

func (s *SendScreen) handleInFlightKey(
	keyStr string,
) (Screen, tea.Cmd) {
	return s, nil
}

// ── Result step ─────────────────────────────────────────

func (s *SendScreen) handleResultKey(
	keyStr string,
) (Screen, tea.Cmd) {
	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit
	case "enter":
		return s, tea.Batch(
			emitCloseTab,
			emitRefreshStatus)
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

// ── Paste handling ──────────────────────────────────────

func (s *SendScreen) handlePaste(
	msg tea.PasteMsg,
) (Screen, tea.Cmd) {
	if s.step != sendStepInput {
		return s, nil
	}
	if s.focusZone != sendZoneInput {
		return s, nil
	}
	var cmd tea.Cmd
	s.sendInput, cmd =
		s.sendInput.Update(msg)
	return s, cmd
}

// ── Async message handlers ──────────────────────────────

func (s *SendScreen) handlePayReqDecoded(
	msg payReqDecodedMsg,
) (Screen, tea.Cmd) {
	// The attempt must still own this screen, and its invoice must still be
	// the one in the form. This also covers edits made while decoding.
	if s.step != sendStepInput || s.attempt == nil || msg.attempt != s.attempt ||
		s.sendInput.Value() != s.attempt.request.Invoice() {
		return s, nil
	}
	if msg.err != nil {
		s.inputError = msg.err.Error()
		return s, nil
	}
	s.prepared = msg.payment
	s.confirmBtnIdx = 1 // default to Confirm
	s.step = sendStepConfirm
	return s, nil
}

func (s *SendScreen) handleSendResult(
	msg sendPaymentResultMsg,
) (Screen, tea.Cmd) {
	if s.step != sendStepInFlight || s.attempt == nil || msg.attempt != s.attempt {
		return s, nil
	}
	s.result = lndrpc.SendPaymentResult{Status: "UNKNOWN"}
	if msg.err != nil {
		s.result.Error = msg.err.Error()
	} else if msg.result != nil {
		s.result = *msg.result
	}
	if s.result.Status != "SUCCEEDED" && s.result.Error == "" {
		s.result.Error = "Check Payment History before retrying."
	}
	s.step = sendStepResult
	return s, paymentHistoryChangedCmd
}

// ── Views ───────────────────────────────────────────────

func (s *SendScreen) viewInput(w, h int) string {
	p := newPane(w)
	p.title(theme.Header, "⚡ Send Payment")

	var totalLocal int64
	if s.ctx.Status != nil {
		for _, ch := range s.ctx.Status.Channels.Value.Channels {
			totalLocal += ch.LocalBalance
		}
	}
	p.field("Spendable: ",
		channelAmountText(s.ctx.Status, totalLocal))
	p.blank()

	isFocused := s.ctx.ContentFocused
	inputFocused := isFocused &&
		s.focusZone == sendZoneInput
	p.input("Payment Request:",
		s.sendInput.View(), inputFocused)
	p.blank()
	p.dim("Paste a bolt11 invoice")

	p.appendError(s.inputError)

	// ── Buttons pinned to bottom ──
	btnFocused := isFocused &&
		s.focusZone == sendZoneButtons
	return p.renderWithBottomButtons(
		[]string{"Clear", "Send"},
		s.inputBtnIdx, btnFocused, h, s.ctx.disabledWalletButtons(1)...)
}

func (s *SendScreen) viewConfirm(
	w, h int,
) string {
	p := newPane(w)
	p.title(theme.Warning, "Confirm Payment")
	details := s.prepared.Details()

	p.field("Amount:      ",
		formatSats(details.AmountSats)+" sats")
	if details.Description != "" {
		p.field("Description: ", details.Description)
	}
	p.labelLine("Destination:")
	p.mono(details.Destination)
	p.blank()
	p.warn("Send " +
		formatSats(details.AmountSats) + " sats?")

	// ── Buttons pinned to bottom ──
	btnFocused := s.ctx.ContentFocused
	return p.renderWithBottomButtons(
		[]string{"Go Back", "Confirm"},
		s.confirmBtnIdx, btnFocused, h, s.ctx.disabledWalletButtons(1)...)
}

func (s *SendScreen) viewInFlight(
	w, h int,
) string {
	p := newPane(w)
	p.title(theme.Header, "Sending Payment...")
	p.line(" " + theme.Value.Render(
		"Routing "+formatSats(s.prepared.Details().AmountSats)+
			" sats"))
	p.blank()
	p.dim("May take up to 60 seconds over Tor.")
	return p.renderWithBottomButtons(
		[]string{"Sending..."}, 0, false, h)
}

func (s *SendScreen) viewResult(
	w, h int,
) string {
	p := newPane(w)

	if s.result.Status != "SUCCEEDED" {
		title := "Payment Status Unknown"
		switch s.result.Status {
		case "FAILED":
			title = "Payment Failed"
		case "IN_FLIGHT":
			title = "Payment Still In Flight"
		}
		p.title(theme.Warning, title)
		p.warnWrap(s.result.Error)
	} else {
		p.title(theme.Success, "Payment Sent")
		p.field("Amount: ",
			formatSats(s.prepared.Details().AmountSats)+" sats")
		p.field("Fee:    ",
			formatSats(s.result.FeeSats)+" sats")
		if s.result.Preimage != "" {
			p.blank()
			p.labelLine("Preimage:")
			p.mono(s.result.Preimage)
		}
		if len(s.result.Hops) > 0 {
			p.blank()
			p.labelLine("Route:")
			p.line(renderRouteDiagram(
				s.result.Hops, w))
		}
	}

	return p.renderWithBottomButtons(
		[]string{"Done"}, 0,
		s.ctx.ContentFocused, h)
}

// ── Helpbar bindings ────────────────────────────────────

func (s *SendScreen) inputBindings() []key.Binding {
	var binds []key.Binding
	switch s.focusZone {
	case sendZoneInput:
		binds = append(binds,
			kLeftRightCursor,
			kTabNext,
			bind("enter", "send", "enter"),
			kSidebar)
		if s.ctx.HasTabs {
			binds = append(binds, kShiftTabBar)
		}
	case sendZoneButtons:
		binds = append(binds,
			kLeftRightButtons, kEnter, kShiftTabInput,
			kBack)
	}
	binds = append(binds, kQuit)
	return binds
}
