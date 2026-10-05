package tui

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/tree"

	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/lndrpc"
	"github.com/virtpriv/node/internal/theme"
)

// ── On-chain send screen steps ──────────────────────────

type ocSendStep int

const (
	ocStepAddr      ocSendStep = iota // address input
	ocStepAmount                      // amount + Max button
	ocStepLabel                       // label input
	ocStepFee                         // fee rate input
	ocStepButtons                     // Clear / Create Transaction
	ocStepConfirm                     // Go Back / Confirm & Broadcast
	ocStepBroadcast                   // in-flight
	ocStepResult                      // success or error
)

// ── OnChainSendScreen ───────────────────────────────────

type OnChainSendScreen struct {
	ctx   *ScreenContext
	ocCtx *OnChainContext
	step  ocSendStep

	// Inputs (steps 0–3)
	addrInput  textinput.Model
	amtInput   AmountInput
	labelInput textinput.Model
	feeInput   AmountInput
	sendAll    bool
	maxFocused bool // Max button highlighted on amount step

	// Buttons (step 4)
	sendBtnIdx int // 0=Clear, 1=Create Transaction

	fees          feeSuggestions
	client        app.OnChainSendClient
	attempt       *onChainSendAttempt
	confirmBtnIdx int
	result        app.OnChainSendResult
	error         string
}

func NewOnChainSendScreen(
	ctx *ScreenContext,
	ocCtx *OnChainContext,
) *OnChainSendScreen {
	s := &OnChainSendScreen{
		ctx:        ctx,
		ocCtx:      ocCtx,
		step:       ocStepAddr,
		addrInput:  newOnChainAddrInput(ctx.Cfg.Network),
		amtInput:   NewAmountInput(),
		labelInput: newOCSendLabelInput(),
		feeInput:   NewFeeInput(),
		sendBtnIdx: 1, // default to Create Transaction
	}
	if ctx.LndClient != nil {
		s.client = ctx.LndClient
	}
	return s
}

// ── Screen interface ────────────────────────────────────

func (s *OnChainSendScreen) Init() tea.Cmd {
	return s.fees.refresh(s.ctx)
}

func (s *OnChainSendScreen) HandleKey(
	keyStr string, msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
	switch s.step {
	case ocStepAddr, ocStepAmount, ocStepLabel,
		ocStepFee, ocStepButtons:
		return s.handleInputKey(keyStr, msg)
	case ocStepConfirm:
		return s.handleConfirmKey(keyStr)
	case ocStepBroadcast:
		return s.handleBroadcastKey(keyStr)
	case ocStepResult:
		return s.handleResultKey(keyStr)
	}
	return s, nil
}

func (s *OnChainSendScreen) HandleMsg(
	msg tea.Msg,
) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.PasteMsg:
		return s.handlePaste(msg)
	case sendCoinsResultMsg:
		return s.handleSendCoinsResult(msg)
	case tabActivatedMsg:
		if s.step <= ocStepButtons {
			return s, s.fees.refresh(s.ctx)
		}
	case feeSuggestionsMsg:
		return s.handleFeeSuggestions(msg)
	}
	return s, nil
}

func (s *OnChainSendScreen) View(
	w, h int,
) string {
	switch s.step {
	case ocStepAddr, ocStepAmount, ocStepLabel,
		ocStepFee, ocStepButtons:
		return s.viewInput(w, h)
	case ocStepConfirm:
		return s.viewConfirm(w, h)
	case ocStepBroadcast:
		return s.viewBroadcast(w, h)
	case ocStepResult:
		return s.viewResult(w, h)
	}
	return ""
}

func (s *OnChainSendScreen) HelpBindings() []key.Binding {
	switch s.step {
	case ocStepAddr, ocStepAmount, ocStepLabel,
		ocStepFee:
		return s.inputFieldBindings()
	case ocStepButtons:
		return s.inputButtonBindings()
	case ocStepConfirm:
		return actionButtonBindings(
			s.confirmBtnIdx, s.ctx.HasTabs)
	case ocStepBroadcast:
		return []key.Binding{kSidebar, kUpShiftTabBar}
	case ocStepResult:
		return resultBindings(s.ctx.HasTabs)
	}
	return nil
}

// ── Input steps (0–4) key handling ──────────────────────

func (s *OnChainSendScreen) handleInputKey(
	keyStr string, msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit

	case "left":
		return s.handleInputLeft(msg)

	case "right":
		return s.handleInputRight(msg)

	case "backspace":
		return s.handleInputBackspace(msg)

	case "up":
		return s.handleInputUp()

	case "down":
		return s.handleInputDown()

	case "tab":
		return s.handleInputTab()

	case "shift+tab":
		return s.handleInputShiftTab()

	case "enter":
		return s.handleInputEnter()

	default:
		return s.handleInputDefault(msg)
	}
}

func (s *OnChainSendScreen) handleInputLeft(
	msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
	// Buttons: move left
	if s.step == ocStepButtons &&
		s.sendBtnIdx > 0 {
		s.sendBtnIdx--
		return s, nil
	}
	// Addr input: pass through for cursor
	if s.step == ocStepAddr {
		if s.addrInput.Value() != "" {
			var cmd tea.Cmd
			s.addrInput, cmd =
				s.addrInput.Update(tea.Msg(msg))
			return s, cmd
		}
	}
	// Amount step: Max focused → back to input
	if s.step == ocStepAmount && s.maxFocused {
		s.maxFocused = false
		s.amtInput.Focus()
		return s, nil
	}
	// Amount input: pass through for cursor
	if s.step == ocStepAmount {
		if !s.amtInput.Empty() {
			cmd := s.amtInput.Update(tea.Msg(msg))
			return s, cmd
		}
	}
	// Label input: pass through for cursor
	if s.step == ocStepLabel {
		if s.labelInput.Value() != "" {
			var cmd tea.Cmd
			s.labelInput, cmd =
				s.labelInput.Update(tea.Msg(msg))
			return s, cmd
		}
	}
	// Fee input: pass through for cursor
	if s.step == ocStepFee {
		if !s.feeInput.Empty() {
			cmd := s.fees.edit(&s.feeInput, tea.Msg(msg))
			return s, cmd
		}
	}
	return s, emitFocusSidebar
}

func (s *OnChainSendScreen) handleInputRight(
	msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
	// Buttons: move right
	if s.step == ocStepButtons &&
		s.sendBtnIdx < 1 {
		s.sendBtnIdx++
		return s, nil
	}
	// Addr input: pass through for cursor
	if s.step == ocStepAddr {
		var cmd tea.Cmd
		s.addrInput, cmd =
			s.addrInput.Update(tea.Msg(msg))
		return s, cmd
	}
	// Amount step: cursor inside value passes
	// through. At end of value (or empty), jump
	// to Max button (two-step preserved).
	if s.step == ocStepAmount && !s.maxFocused {
		if s.amtInput.Empty() ||
			s.amtInput.CursorAtEnd() {
			s.maxFocused = true
			s.amtInput.Blur()
			return s, nil
		}
		cmd := s.amtInput.Update(tea.Msg(msg))
		return s, cmd
	}
	// Label input: pass through for cursor
	if s.step == ocStepLabel {
		var cmd tea.Cmd
		s.labelInput, cmd =
			s.labelInput.Update(tea.Msg(msg))
		return s, cmd
	}
	// Fee input: pass through for cursor
	if s.step == ocStepFee {
		cmd := s.fees.edit(&s.feeInput, tea.Msg(msg))
		return s, cmd
	}
	return s, nil
}

func (s *OnChainSendScreen) handleInputBackspace(
	msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
	switch s.step {
	case ocStepAddr:
		var cmd tea.Cmd
		s.addrInput, cmd =
			s.addrInput.Update(tea.Msg(msg))
		return s, cmd
	case ocStepAmount:
		if !s.maxFocused {
			s.disengageMax()
			cmd := s.amtInput.Update(tea.Msg(msg))
			return s, cmd
		}
		// Backspace on the Max button returns to the parent.
		return s, emitFocusParent
	case ocStepLabel:
		var cmd tea.Cmd
		s.labelInput, cmd =
			s.labelInput.Update(tea.Msg(msg))
		return s, cmd
	case ocStepFee:
		cmd := s.fees.edit(&s.feeInput, tea.Msg(msg))
		return s, cmd
	case ocStepButtons:
		return s, emitFocusParent
	}
	return s, nil
}

func (s *OnChainSendScreen) handleInputUp() (
	Screen, tea.Cmd,
) {
	if s.step > ocStepAddr {
		s.step--
		s.maxFocused = false
		s.focusStep()
	} else if s.ctx.HasTabs {
		return s, emitFocusTabBar
	}
	return s, nil
}

func (s *OnChainSendScreen) handleInputDown() (
	Screen, tea.Cmd,
) {
	next := s.step + 1
	if next > ocStepButtons {
		return s, nil
	}
	s.step = next
	s.maxFocused = false
	s.focusStep()
	return s, nil
}

func (s *OnChainSendScreen) handleInputTab() (
	Screen, tea.Cmd,
) {
	return s.handleInputDown()
}

func (s *OnChainSendScreen) handleInputShiftTab() (
	Screen, tea.Cmd,
) {
	// Express backward jump one step
	if s.step > ocStepAddr {
		s.step--
		s.maxFocused = false
		s.focusStep()
	} else if s.ctx.HasTabs {
		return s, emitFocusTabBar
	}
	return s, nil
}

func (s *OnChainSendScreen) handleInputEnter() (
	Screen, tea.Cmd,
) {
	// Amount step: Max focused → engage Max
	if s.step == ocStepAmount && s.maxFocused {
		s.applyMax()
		return s, nil
	}
	// Bottom buttons
	if s.step == ocStepButtons {
		switch s.sendBtnIdx {
		case 0: // Clear
			s.resetInputs()
			return s, nil
		case 1: // Create Transaction
			return s.validateAndConfirm()
		}
	}
	// Enter on any other step: advance to next
	next := s.step + 1
	if next > ocStepButtons {
		next = ocStepButtons
	}
	s.step = next
	s.maxFocused = false
	s.focusStep()
	return s, nil
}

func (s *OnChainSendScreen) handleInputDefault(
	msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
	switch s.step {
	case ocStepAddr:
		var cmd tea.Cmd
		s.addrInput, cmd =
			s.addrInput.Update(tea.Msg(msg))
		return s, cmd
	case ocStepAmount:
		if !s.maxFocused {
			s.disengageMax()
			cmd := s.amtInput.Update(tea.Msg(msg))
			return s, cmd
		}
	case ocStepLabel:
		var cmd tea.Cmd
		s.labelInput, cmd =
			s.labelInput.Update(tea.Msg(msg))
		return s, cmd
	case ocStepFee:
		cmd := s.fees.edit(&s.feeInput, tea.Msg(msg))
		return s, cmd
	}
	return s, nil
}

// ── Confirm step (step 5) ──────────────────────────────

func (s *OnChainSendScreen) handleConfirmKey(
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
		case 1: // Confirm & Broadcast
			if s.attempt == nil || !s.ctx.walletExists() {
				return s, nil
			}
			req := s.attempt.prepared.Request()
			if !slices.Equal(req.Outpoints, s.ocCtx.Selection.Outpoints()) {
				s.backToInput()
				s.error = "Coin selection changed. Review the transaction again"
				return s, nil
			}
			s.error = ""
			s.step = ocStepBroadcast
			return s, sendCoinsCmd(s.client, s.attempt)
		}
	}
	return s, nil
}

// ── Broadcast step (step 6) ────────────────────────────

func (s *OnChainSendScreen) handleBroadcastKey(keyStr string) (Screen, tea.Cmd) {
	switch keyStr {
	case "left":
		return s, emitFocusSidebar
	case "up", "shift+tab":
		return s, emitFocusTabBar
	}
	return s, nil
}

func onChainSendBusy(screen Screen) bool {
	s, ok := screen.(*OnChainSendScreen)
	return ok && s.step == ocStepBroadcast
}

type closeOnChainSendMsg struct{ screen *OnChainSendScreen }

// ── Result step (step 7) ───────────────────────────────

func (s *OnChainSendScreen) handleResultKey(
	keyStr string,
) (Screen, tea.Cmd) {
	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit
	case "enter":
		return s, func() tea.Msg { return closeOnChainSendMsg{screen: s} }
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

// ── Paste handling ─────────────────────────────────────

func (s *OnChainSendScreen) handlePaste(
	msg tea.PasteMsg,
) (Screen, tea.Cmd) {
	switch s.step {
	case ocStepAddr:
		var cmd tea.Cmd
		s.addrInput, cmd =
			s.addrInput.Update(msg)
		return s, cmd
	case ocStepAmount:
		if !s.maxFocused {
			s.disengageMax()
			cmd := s.amtInput.Update(msg)
			return s, cmd
		}
	case ocStepLabel:
		var cmd tea.Cmd
		s.labelInput, cmd =
			s.labelInput.Update(msg)
		return s, cmd
	case ocStepFee:
		cmd := s.fees.edit(&s.feeInput, msg)
		return s, cmd
	}
	return s, nil
}

// ── Async message handlers ─────────────────────────────

func (s *OnChainSendScreen) handleSendCoinsResult(msg sendCoinsResultMsg) (Screen, tea.Cmd) {
	if s.step != ocStepBroadcast || s.attempt == nil || msg.attempt != s.attempt {
		return s, nil
	}
	s.result = msg.result
	s.step = ocStepResult
	if msg.result.State == app.OnChainBroadcast && slices.Equal(
		s.ocCtx.Selection.Outpoints(), s.attempt.prepared.Request().Outpoints) {
		s.ocCtx.Selection.Clear()
	}
	// Refresh wallet facts even if the RPC outcome is unknown. Never retry here.
	return s, tea.Batch(requestOnChainCmd,
		requestStatusCmd)
}

func (s *OnChainSendScreen) handleFeeSuggestions(msg feeSuggestionsMsg) (Screen, tea.Cmd) {
	if s.fees.complete(msg, s.ctx.Cfg.Network) && s.step <= ocStepButtons && !s.fees.edited && s.feeInput.Empty() {
		if rate := s.fees.defaultRate(s.ctx.Cfg.Network); rate > 0 {
			s.feeInput.SetSats(rate)
		}
	}
	return s, nil
}

// ── Internal helpers ───────────────────────────────────

// focusStep manages text input focus for the current
// step. Blurs all inputs, then focuses the active one.
func (s *OnChainSendScreen) focusStep() {
	s.addrInput.Blur()
	s.amtInput.Blur()
	s.labelInput.Blur()
	s.feeInput.Blur()
	s.maxFocused = false
	switch s.step {
	case ocStepAddr:
		s.addrInput.Focus()
	case ocStepAmount:
		s.amtInput.Focus()
	case ocStepLabel:
		s.labelInput.Focus()
	case ocStepFee:
		s.feeInput.Focus()
	}
}

// Max is an intent, not a locally computed net amount. LND accounts for
// fees and any required reserve change when constructing the transaction.
func (s *OnChainSendScreen) applyMax() {
	s.sendAll = true
	s.amtInput.Clear()
}

func (s *OnChainSendScreen) disengageMax() { s.sendAll = false }

func (s *OnChainSendScreen) validateAndConfirm() (Screen, tea.Cmd) {
	if !s.ctx.walletExists() {
		return s, nil
	}
	prepared, err := app.PrepareOnChainSend(s.ctx.Cfg.Network, app.OnChainSendInput{
		Address: s.addrInput.Value(), AmountSats: s.amtInput.Sats(), SendAll: s.sendAll,
		SatPerVbyte: s.feeInput.Sats(), Label: s.labelInput.Value(),
		Outpoints: s.ocCtx.Selection.Outpoints(),
	}, s.ocCtx.Utxos.Value)
	if err != nil {
		s.error = err.Error()
		return s, nil
	}
	s.attempt = &onChainSendAttempt{prepared: prepared}
	s.error = ""
	s.confirmBtnIdx = 0
	s.step = ocStepConfirm
	return s, nil
}

func (s *OnChainSendScreen) backToInput() {
	s.attempt = nil
	s.step = ocStepButtons
	s.error = ""
	s.confirmBtnIdx = 0
}

// resetInputs creates fresh inputs and resets all
// input-phase state, including unavailable selected coins.
func (s *OnChainSendScreen) resetInputs() {
	s.addrInput = newOnChainAddrInput(s.ctx.Cfg.Network)
	s.amtInput = NewAmountInput()
	s.labelInput = newOCSendLabelInput()
	s.feeInput = NewFeeInput()
	s.fees.edited = false
	s.sendAll = false
	s.maxFocused = false
	s.step = ocStepAddr
	s.sendBtnIdx = 1
	s.confirmBtnIdx = 0
	s.attempt = nil
	s.ocCtx.Selection.Clear()
	s.error = ""
	if rate := s.fees.defaultRate(s.ctx.Cfg.Network); rate > 0 {
		s.feeInput.SetSats(rate)
	}
}

// ── Views ──────────────────────────────────────────────

func (s *OnChainSendScreen) viewInput(
	w, h int,
) string {
	isFocused := s.ctx.ContentFocused

	var lines []string
	lines = append(lines, "")
	lines = append(lines, centerPad(
		theme.Header.Render("⛓ Send On-Chain"), w))
	lines = append(lines, "")

	// Balance
	lines = append(lines,
		" "+theme.Label.Render("Balance:  ")+
			theme.Value.Render(
				onChainBalanceText(s.ctx.Status)))
	lines = append(lines, "")

	// ── Address input (step 0) ──────────────────
	addrActive := isFocused &&
		s.step == ocStepAddr
	addrLabel := theme.Header
	addrMarker := " "
	if addrActive {
		addrMarker = theme.NavActive.Render("▸")
	}
	lines = append(lines,
		" "+addrLabel.Render("To:"))
	lines = append(lines,
		addrMarker+" "+s.addrInput.View())
	lines = append(lines, "")

	// ── Amount input (step 1) ───────────────────
	amtActive := isFocused &&
		s.step == ocStepAmount
	amtLabel := theme.Header
	amtMarker := " "
	if amtActive {
		amtMarker = theme.NavActive.Render("▸")
	}

	lines = append(lines,
		" "+amtLabel.Render("Amount (sats):"))
	maxStyle := theme.BtnNormal
	if amtActive && s.maxFocused {
		maxStyle = theme.BtnFocused
	}
	maxLabel := "Max"
	if n := s.ocCtx.Selection.Len(); n == 1 {
		maxLabel = "Max (1 UTXO selected)"
	} else if n > 1 {
		maxLabel = fmt.Sprintf(
			"Max (%d UTXOs selected)", n)
	}
	renderedMax := maxStyle.Render(maxLabel)
	amountView := s.amtInput.View()
	if s.sendAll {
		amountView = theme.Value.Render("Max (net amount set by LND)")
	}
	leftPart := amtMarker + " " + amountView
	gap := w - lipgloss.Width(leftPart) -
		lipgloss.Width(renderedMax) - 2
	if gap < 2 {
		gap = 2
	}
	lines = append(lines,
		leftPart+strings.Repeat(" ", gap)+renderedMax)
	lines = append(lines, "")

	// ── Label input (step 2) ────────────────────
	lblActive := isFocused &&
		s.step == ocStepLabel
	lblLabel := theme.Header
	lblMarker := " "
	if lblActive {
		lblMarker = theme.NavActive.Render("▸")
	}
	lines = append(lines,
		" "+lblLabel.Render("Label:"))
	lines = append(lines,
		lblMarker+" "+s.labelInput.View())
	lines = append(lines, "")

	// ── Fee rate input (step 3) ─────────────────
	feeActive := isFocused &&
		s.step == ocStepFee
	feeLabelStyle := theme.Header
	feeMarker := " "
	if feeActive {
		feeMarker = theme.NavActive.Render("▸")
	}
	lines = append(lines,
		" "+feeLabelStyle.Render("Fee Rate (sat/vB):"))
	lines = append(lines,
		feeMarker+" "+s.feeInput.View())

	// Friendly fee reference hints
	hints := s.fees.hints(s.ctx.Cfg.Network)
	if hints != "" {
		lines = append(lines,
			"  "+theme.Dim.Render(hints))
	}
	lines = append(lines, "")

	// Preview expresses intent only; a manual fee rate has no supported total quote.
	var diagLines []string
	if s.sendAll || s.amtInput.Sats() > 0 {
		amount := formatSats(s.amtInput.Sats())
		if s.sendAll {
			amount = "unknown"
		}
		diagLines = renderTxDiagram(buildDiagramInputs(s.ocCtx.Selection.Outpoints(), s.ocCtx.Utxos.Value, s.ocCtx.OnChainTxs.Value),
			strings.TrimSpace(s.addrInput.Value()), amount, "unknown", "unknown", s.sendAll, w)
		diagLines = append(diagLines, " "+theme.Dim.Render("Total fee unavailable for this manual rate; LND determines change."))
		if s.sendAll {
			diagLines = append(diagLines, " "+theme.Dim.Render("Max deducts fees and may leave reserve change."))
		}
	}

	// Error
	var errLines []string
	if s.error != "" {
		errLines = append(errLines, "")
		lineW := w - 4
		if lineW < 16 {
			lineW = 16
		}
		errText := s.error
		for len(errText) > 0 {
			end := lineW
			if end > len(errText) {
				end = len(errText)
			}
			errLines = append(errLines,
				" "+theme.Warning.Render(
					errText[:end]))
			errText = errText[end:]
		}
	}

	// ── Bottom buttons (step 4) ─────────────────
	btnFocused := isFocused &&
		s.step == ocStepButtons
	btnLine := renderButtons(
		[]string{"Clear", "Create Transaction"},
		s.sendBtnIdx, btnFocused, w, s.ctx.disabledWalletButtons(1)...)

	// ── Layout: form top, diagram centered in
	// remaining space, buttons pinned at bottom ──
	formH := len(lines)
	diagH := len(diagLines) + len(errLines)
	totalPad := h - formH - diagH - 1 // -1 for btn
	totalPad = max(totalPad, 2)

	padAbove := totalPad / 2
	padBelow := totalPad - padAbove
	padAbove = max(padAbove, 1)
	padBelow = max(padBelow, 0)

	for i := 0; i < padAbove; i++ {
		lines = append(lines, "")
	}
	lines = append(lines, diagLines...)
	lines = append(lines, errLines...)
	for i := 0; i < padBelow; i++ {
		lines = append(lines, "")
	}
	lines = append(lines, btnLine)

	return strings.Join(lines, "\n")
}

func (s *OnChainSendScreen) viewConfirm(w, h int) string {
	p := newPane(w)
	p.title(theme.Warning, "Confirm On-Chain Send")
	req := s.attempt.prepared.Request()
	p.labelLine("To:")
	p.monoWrap(req.Address)
	if req.SendAll {
		p.line("Amount: Max (net amount determined by LND)")
	} else {
		p.line("Amount: " + formatSats(req.AmountSats) + " sats")
	}
	if len(req.Outpoints) > 0 {
		p.line(fmt.Sprintf("Inputs: %d selected UTXOs", len(req.Outpoints)))
	} else {
		p.line("Inputs: LND selects eligible wallet coins")
	}
	p.line("Unconfirmed inputs: allowed, subject to LND checks")
	if req.Label != "" {
		p.valueWrap("Label: " + req.Label)
	}
	p.line(fmt.Sprintf("Fee rate: %d sat/vB", req.SatPerVbyte))
	p.line("Total fee: unavailable for this manual rate")
	if req.SendAll {
		p.line("Max deducts fees and may leave reserve change.")
	}
	amount := formatSats(req.AmountSats)
	if req.SendAll {
		amount = "unknown"
	}
	p.blank()
	for _, line := range renderTxDiagram(
		buildDiagramInputs(req.Outpoints, s.attempt.prepared.Coins(), nil),
		req.Address, amount, "unknown", "unknown", req.SendAll, w) {
		p.line(line)
	}
	if s.error != "" {
		p.warnWrap(s.error)
	}
	return p.renderWithBottomButtons([]string{"Go Back", "Confirm & Broadcast"}, s.confirmBtnIdx, s.ctx.ContentFocused, h, s.ctx.disabledWalletButtons(1)...)
}

func (s *OnChainSendScreen) viewBroadcast(
	w, h int,
) string {
	p := newPane(w)
	p.title(theme.Header, "Broadcasting...")
	p.line(" " + theme.Value.Render(
		"Sending transaction to the network."))
	p.blank()
	p.dim("Do not close the terminal.")
	return p.renderWithBottomButtons(
		[]string{"Broadcasting..."}, 0, false, h)
}

func (s *OnChainSendScreen) viewResult(w, h int) string {
	p := newPane(w)
	switch s.result.State {
	case app.OnChainBroadcast:
		p.title(theme.Success, "Transaction Broadcast")
		p.labelLine("TX ID:")
		p.monoWrap(s.result.Txid)
	case app.OnChainOutcomeUnknown:
		p.title(theme.Warning, "Broadcast Outcome Unknown")
		p.warnWrap("Check transaction history before attempting another send.")
	default:
		p.title(theme.Warning, "Transaction Not Submitted")
	}
	if s.result.Err != nil {
		p.warnWrap(s.result.Err.Error())
	}
	return p.renderWithBottomButtons([]string{"Done"}, 0, s.ctx.ContentFocused, h)
}

// ── Helpbar bindings ───────────────────────────────────

func (s *OnChainSendScreen) inputFieldBindings() []key.Binding {
	binds := []key.Binding{
		kUpDownFields, kTabNext, kLeftRightCursor,
		bind("enter", "continue", "enter"),
		kSidebar,
	}
	switch s.step {
	case ocStepAddr:
		if s.ctx.HasTabs {
			binds = append(binds, kShiftTabBar)
		}
	case ocStepAmount:
		binds = append(binds,
			bind("⇧tab", "address", "shift+tab"))
	case ocStepLabel:
		binds = append(binds,
			bind("⇧tab", "amount", "shift+tab"))
	case ocStepFee:
		binds = append(binds,
			bind("⇧tab", "label", "shift+tab"))
	}
	binds = append(binds, kQuit)
	return binds
}

func (s *OnChainSendScreen) inputButtonBindings() []key.Binding {
	binds := buttonNav(s.sendBtnIdx)
	binds = append(binds,
		kEnter,
		bind("⇧tab", "fee", "shift+tab"),
		bind("↑", "fields", "up"),
		kBack, kQuit)
	return binds
}

// The diagram shows selected input labels and explicit unknown output values.
// LND determines the transaction shape when submitting the reviewed request.
type txDiagramInput struct {
	label string // display label (tx label, address, or txid)
}

func renderTxDiagram(
	inputs []txDiagramInput,
	destAddr string,
	destAmt string,
	changeAmt string,
	feeAmt string,
	sendAll bool,
	availW int,
) []string {
	// ── Fallback if no inputs ────────────────────
	if len(inputs) == 0 {
		inputs = []txDiagramInput{
			{label: "? inputs"},
		}
	}

	// ── Build outputs tree using lipgloss/tree ───
	destLabel := destAddr
	if len(destLabel) > 12 {
		destLabel = destLabel[:6] + ".." +
			destLabel[len(destLabel)-4:]
	}
	if destLabel == "" {
		destLabel = "dest"
	}

	// Find widest output value for alignment
	outValueW := max(
		utf8.RuneCountInString(destAmt),
		utf8.RuneCountInString(feeAmt))
	if changeAmt != "" {
		outValueW = max(outValueW,
			utf8.RuneCountInString(changeAmt))
	}

	outTree := tree.New().
		Enumerator(tree.RoundedEnumerator).
		EnumeratorStyle(theme.Dim).
		ItemStyleFunc(func(
			children tree.Children, i int,
		) lipgloss.Style {
			if i == 0 {
				return theme.Value
			}
			return theme.Dim
		})

	outTree.Child(fmt.Sprintf("%-12s %*s",
		destLabel, outValueW, destAmt))
	if changeAmt != "" {
		changeLabel := "change"
		if sendAll {
			changeLabel = "reserve?"
		}
		outTree.Child(fmt.Sprintf("%-12s %*s", changeLabel, outValueW, changeAmt))
	}
	outTree.Child(fmt.Sprintf("%-12s %*s",
		"fee", outValueW, feeAmt))

	outputsRendered := outTree.String()

	// ── Build inputs column ──────────────────────
	// Input amounts remain in the UTXO table.
	inLabelW := 8
	for _, inp := range inputs {
		inLabelW = max(inLabelW,
			utf8.RuneCountInString(inp.label))
	}
	// Cap label width to keep diagram within pane
	inLabelW = min(inLabelW, 12)

	inH := len(inputs)

	var inputLines []string
	for i, inp := range inputs {
		label := inp.label
		if utf8.RuneCountInString(label) > inLabelW {
			label = label[:inLabelW-2] + ".."
		}
		padded := fmt.Sprintf("%*s", inLabelW, label)

		// Connector
		var conn string
		switch {
		case inH == 1:
			conn = " ────"
		case i == 0:
			conn = " ───╮"
		case i == inH-1:
			conn = " ───╯"
		default:
			conn = " ───┤"
		}

		inputLines = append(inputLines,
			theme.Mono.Render(padded)+
				theme.Dim.Render(conn))
	}
	inputsRendered := strings.Join(
		inputLines, "\n")

	// ── Transaction label with connecting lines ──
	txStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(theme.ColorPrimary)
	txLabel := theme.Dim.Render("── ") +
		txStyle.Render("Transaction") +
		theme.Dim.Render(" ──")

	// ── Join horizontally, centered vertically ───
	diagram := lipgloss.JoinHorizontal(
		lipgloss.Center,
		inputsRendered,
		txLabel,
		outputsRendered,
	)

	// ── Center within available width ────────────
	diagLines := strings.Split(diagram, "\n")
	maxDiagW := 0
	for _, line := range diagLines {
		maxDiagW = max(maxDiagW,
			lipgloss.Width(line))
	}
	leftPad := (availW - maxDiagW) / 2
	leftPad = max(leftPad, 1)
	padStr := strings.Repeat(" ", leftPad)

	var result []string
	for _, line := range diagLines {
		result = append(result, padStr+line)
	}

	return result
}

// buildDiagramInputs creates txDiagramInput entries from
// outpoints, cross-referencing UTXOs and tx history for
// labels.
func buildDiagramInputs(
	outpoints []string,
	utxos []lndrpc.UTXO,
	txs []lndrpc.OnChainTx,
) []txDiagramInput {
	var inputs []txDiagramInput
	for _, op := range outpoints {
		inp := txDiagramInput{label: op}

		// Parse txid from outpoint
		txid := op
		if idx := strings.Index(txid, ":"); idx > 0 {
			txid = txid[:idx]
		}

		// Look up UTXO for amount and address
		for _, u := range utxos {
			uOP := fmt.Sprintf("%s:%d", u.Txid, u.Vout)
			if uOP == op {
				if len(u.Address) > 14 {
					inp.label = u.Address[:8] + ".." +
						u.Address[len(u.Address)-4:]
				} else {
					inp.label = u.Address
				}
				break
			}
		}

		// Check tx history for a user-set label
		for _, tx := range txs {
			if tx.Txid == txid && tx.Label != "" {
				inp.label = tx.Label
				break
			}
		}

		// Fallback: truncated txid
		if inp.label == op {
			if len(txid) > 12 {
				inp.label = txid[:10] + ".."
			} else {
				inp.label = txid
			}
		}

		inputs = append(inputs, inp)
	}
	return inputs
}
