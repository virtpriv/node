package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/theme"
)

type recvStep int

const (
	recvStepInput    recvStep = iota // amount + memo entry
	recvStepCreating                 // invoice creation in progress
	recvStepWaiting                  // invoice created, waiting for payment
	recvStepPaid                     // payment received
	recvStepExpired                  // invoice expired
	recvStepCanceled                 // LND retained the canceled invoice
	recvStepMissing                  // LND no longer has the invoice
)

const (
	recvZoneAmount  = 0
	recvZoneMemo    = 1
	recvZoneBlind   = 2
	recvZoneButtons = 3
)

// Each creation attempt owns its results, even after a tab is closed and reopened.
type invoiceAttempt struct {
	request app.InvoiceRequest
	wallet  walletObservationScope
}

type ReceiveScreen struct {
	ctx         *ScreenContext
	step        recvStep
	invoices    app.LightningInvoiceClient
	attempt     *invoiceAttempt
	invoice     app.LightningInvoice
	checking    bool
	lookupError string
	display     qrCopyDisplayState

	// Input state
	amountInput AmountInput
	memoInput   textinput.Model
	blindPaths  bool // blinded paths on invoice (privacy)
	focusZone   int  // 0=amount, 1=memo, 2=blind, 3=buttons
	btnIdx      int  // 0=Clear, 1=Create Invoice
	inputError  string

	buttonIdx int // 0=Show QR, 1=Copyable Invoice
}

func NewReceiveScreen(
	ctx *ScreenContext,
) *ReceiveScreen {
	var invoices app.LightningInvoiceClient
	if ctx.LndClient != nil {
		invoices = ctx.LndClient
	}
	amt := NewAmountInput()
	amt.Focus() // amount is the initial focus zone
	return &ReceiveScreen{
		ctx:         ctx,
		invoices:    invoices,
		step:        recvStepInput,
		amountInput: amt,
		memoInput:   newRecvMemoInput(),
		blindPaths:  true,
		focusZone:   recvZoneAmount,
		btnIdx:      1, // default to Create Invoice
	}
}

func (s *ReceiveScreen) Init() tea.Cmd {
	return nil
}

func (s *ReceiveScreen) HandleKey(
	keyStr string, msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
	switch s.step {
	case recvStepInput:
		return s.handleInputKey(keyStr, msg)
	case recvStepCreating:
		switch keyStr {
		case "ctrl+c":
			return s, tea.Quit
		case "left":
			return s, emitFocusSidebar
		case "backspace":
			return s, emitFocusParent
		}
		return s, nil
	case recvStepWaiting:
		return s.handleWaitingKey(keyStr)
	case recvStepPaid, recvStepExpired, recvStepCanceled, recvStepMissing:
		return s.handleResultKey(keyStr)
	}
	return s, nil
}

func (s *ReceiveScreen) HandleMsg(
	msg tea.Msg,
) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.PasteMsg:
		return s.handlePaste(msg)
	case invoiceCreatedMsg:
		return s.handleInvoiceCreated(msg)
	case invoiceCheckMsg:
		if s.step == recvStepWaiting && s.attempt != nil && msg.attempt == s.attempt && !s.checking {
			s.checking = true
			return s, checkInvoiceCmd(s.invoices, s.attempt, s.invoice)
		}
	case invoiceStatusMsg:
		return s.handleInvoiceStatus(msg)
	}
	return s, nil
}

func (s *ReceiveScreen) View(w, h int) string {
	switch s.step {
	case recvStepInput:
		return s.viewInput(w, h)
	case recvStepCreating:
		p := newPane(w)
		p.title(theme.Header, "Creating Invoice")
		p.dim("Waiting for LND...")
		return p.render()
	case recvStepWaiting:
		return s.viewWaiting(w, h)
	case recvStepPaid:
		return s.viewPaid(w, h)
	case recvStepExpired, recvStepCanceled, recvStepMissing:
		return s.viewInvoiceResult(w, h)
	}
	return ""
}

func (s *ReceiveScreen) HelpBindings() []key.Binding {
	switch s.step {
	case recvStepInput:
		return s.inputBindings()
	case recvStepCreating:
		return []key.Binding{kSidebar, kBack, kQuit}
	case recvStepWaiting:
		return actionButtonBindings(
			s.buttonIdx, s.ctx.HasTabs)
	case recvStepPaid, recvStepExpired, recvStepCanceled, recvStepMissing:
		return resultBindings(s.ctx.HasTabs)
	}
	return nil
}

func (s *ReceiveScreen) inputBindings() []key.Binding {
	var binds []key.Binding
	switch s.focusZone {
	case recvZoneAmount:
		binds = append(binds,
			kUpDownFields, kTabNext, kEnterCreate,
			kSidebar)
		if s.ctx.HasTabs {
			binds = append(binds, kShiftTabBar)
		}
	case recvZoneMemo:
		binds = append(binds,
			kUpDownFields, kTabNext, kEnterCreate,
			bind("⇧tab", "amount", "shift+tab"),
			kSidebar)
	case recvZoneBlind:
		binds = append(binds,
			kUpDownFields,
			bind("space", "toggle", "space"),
			kEnterNext, kTabNext,
			bind("⇧tab", "memo", "shift+tab"),
			kSidebar)
	case recvZoneButtons:
		binds = append(binds,
			kLeftRightButtons, kEnter,
			bind("⇧tab", "toggle", "shift+tab"),
			kBack)
	}
	binds = append(binds, kQuit)
	return binds
}

func (s *ReceiveScreen) focusInputZone() {
	s.amountInput.Blur()
	s.memoInput.Blur()
	switch s.focusZone {
	case recvZoneAmount:
		s.amountInput.Focus()
	case recvZoneMemo:
		s.memoInput.Focus()
	case recvZoneBlind:
		// no text input to focus
	}
}

func (s *ReceiveScreen) handleInputKey(
	keyStr string, msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
	switch s.focusZone {
	case recvZoneAmount:
		return s.handleAmountKey(keyStr, msg)
	case recvZoneMemo:
		return s.handleMemoKey(keyStr, msg)
	case recvZoneBlind:
		return s.handleBlindKey(keyStr)
	case recvZoneButtons:
		return s.handleButtonKey(keyStr)
	}
	return s, nil
}

// advanceToButtons blurs text inputs and moves focus to
// the buttons zone with Create Invoice as the default.
func (s *ReceiveScreen) advanceToButtons() {
	s.amountInput.Blur()
	s.memoInput.Blur()
	s.focusZone = recvZoneButtons
	s.btnIdx = 1 // default to Create Invoice
}

func (s *ReceiveScreen) handleAmountKey(
	keyStr string, msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit
	case "left":
		if !s.amountInput.Empty() {
			cmd := s.amountInput.Update(
				tea.Msg(msg))
			return s, cmd
		}
		return s, emitFocusSidebar
	case "right":
		cmd := s.amountInput.Update(tea.Msg(msg))
		return s, cmd
	case "backspace":
		cmd := s.amountInput.Update(tea.Msg(msg))
		return s, cmd
	case "up", "shift+tab":
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
		return s, nil
	case "down", "tab":
		s.focusZone = recvZoneMemo
		s.focusInputZone()
		return s, nil
	case "enter":
		s.advanceToButtons()
		return s, nil
	default:
		cmd := s.amountInput.Update(tea.Msg(msg))
		return s, cmd
	}
}

func (s *ReceiveScreen) handleMemoKey(
	keyStr string, msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit
	case "left":
		if s.memoInput.Value() != "" {
			var cmd tea.Cmd
			s.memoInput, cmd =
				s.memoInput.Update(tea.Msg(msg))
			return s, cmd
		}
		return s, emitFocusSidebar
	case "right":
		var cmd tea.Cmd
		s.memoInput, cmd =
			s.memoInput.Update(tea.Msg(msg))
		return s, cmd
	case "backspace":
		var cmd tea.Cmd
		s.memoInput, cmd =
			s.memoInput.Update(tea.Msg(msg))
		return s, cmd
	case "up", "shift+tab":
		s.focusZone = recvZoneAmount
		s.focusInputZone()
		return s, nil
	case "down", "tab":
		s.focusZone = recvZoneBlind
		s.focusInputZone()
		return s, nil
	case "enter":
		s.advanceToButtons()
		return s, nil
	default:
		var cmd tea.Cmd
		s.memoInput, cmd =
			s.memoInput.Update(tea.Msg(msg))
		return s, cmd
	}
}

func (s *ReceiveScreen) handleBlindKey(
	keyStr string,
) (Screen, tea.Cmd) {
	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit
	case "left":
		return s, emitFocusSidebar
	case "backspace":
		return s, emitFocusParent
	case "up", "shift+tab":
		s.focusZone = recvZoneMemo
		s.focusInputZone()
		return s, nil
	case "down", "tab", "enter":
		s.advanceToButtons()
		return s, nil
	case "space":
		s.blindPaths = !s.blindPaths
		return s, nil
	}
	return s, nil
}

func (s *ReceiveScreen) handleButtonKey(
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
		s.focusZone = recvZoneBlind
		s.focusInputZone()
		return s, nil
	case "backspace":
		return s, emitFocusParent
	case "enter":
		switch s.btnIdx {
		case 0: // Clear
			s.amountInput.Clear()
			s.memoInput = newRecvMemoInput()
			s.blindPaths = true
			s.inputError = ""
			s.focusZone = recvZoneAmount
			s.focusInputZone()
			return s, nil
		case 1: // Create Invoice
			return s.submitInvoice()
		}
	}
	return s, nil
}

func (s *ReceiveScreen) submitInvoice() (Screen, tea.Cmd) {
	if !s.ctx.walletExists() {
		return s, nil
	}
	if s.step != recvStepInput {
		return s, nil
	}
	if s.amountInput.Empty() {
		s.inputError = "Enter an amount"
		return s, nil
	}
	s.attempt = &invoiceAttempt{wallet: s.ctx.walletObservationScope(), request: app.InvoiceRequest{
		AmountSats: s.amountInput.Sats(),
		Memo:       s.memoInput.Value(),
		Blinded:    s.blindPaths,
	}}
	s.step = recvStepCreating
	s.inputError = ""
	return s, createInvoiceCmd(s.invoices, s.attempt)
}

func (s *ReceiveScreen) handleWaitingKey(
	keyStr string,
) (Screen, tea.Cmd) {
	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit
	case "left":
		if s.buttonIdx > 0 {
			s.buttonIdx--
		} else {
			return s, emitFocusSidebar
		}
		return s, nil
	case "up":
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
		return s, nil
	case "down", "tab":
		return s, nil
	case "backspace":
		return s, emitFocusParent
	case "right":
		if s.buttonIdx < 1 {
			s.buttonIdx++
		}
		return s, nil
	case "enter":
		if s.attempt == nil {
			return s, nil
		}
		return s, s.display.command(&qrCopyDisplayRequest{
			owner: s, text: s.invoice.PaymentRequest(),
			label: "Invoice - " + formatSats(s.invoice.AmountSats()) + " sats",
			copy:  s.buttonIdx == 1, wallet: s.attempt.wallet, invoice: s.attempt,
		})
	}
	return s, nil
}

func (s *ReceiveScreen) handleResultKey(keyStr string) (Screen, tea.Cmd) {
	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit
	case "enter":
		if s.step == recvStepPaid {
			return s, tea.Batch(emitCloseTab, emitRefreshStatus)
		}
		return s, emitCloseTab
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

func (s *ReceiveScreen) handlePaste(
	msg tea.PasteMsg,
) (Screen, tea.Cmd) {
	if s.step != recvStepInput {
		return s, nil
	}
	var cmd tea.Cmd
	if s.focusZone == recvZoneAmount {
		cmd = s.amountInput.Update(msg)
	} else if s.focusZone == recvZoneMemo {
		s.memoInput, cmd =
			s.memoInput.Update(msg)
	}
	return s, cmd
}

func (s *ReceiveScreen) handleInvoiceCreated(msg invoiceCreatedMsg) (Screen, tea.Cmd) {
	if s.step != recvStepCreating || s.attempt == nil || msg.attempt != s.attempt {
		return s, nil
	}
	if msg.err != nil {
		s.inputError = msg.err.Error()
		if s.attempt.request.Blinded && (strings.Contains(s.inputError, "blinded") || strings.Contains(s.inputError, "routes to self")) {
			s.inputError += " - try turning off blinded paths"
		}
		s.attempt = nil
		s.step = recvStepInput
		return s, nil
	}
	s.invoice = msg.invoice
	s.step = recvStepWaiting
	s.checking = true
	return s, tea.Batch(checkInvoiceCmd(s.invoices, s.attempt, s.invoice), paymentHistoryChangedCmd)
}

func (s *ReceiveScreen) handleInvoiceStatus(msg invoiceStatusMsg) (Screen, tea.Cmd) {
	if s.step != recvStepWaiting || s.attempt == nil || msg.attempt != s.attempt || !s.checking {
		return s, nil
	}
	s.checking = false
	s.lookupError = ""
	if msg.err != nil {
		s.lookupError = msg.err.Error()
	} else {
		switch msg.state {
		case app.InvoicePaid:
			s.step = recvStepPaid
			return s, paymentHistoryChangedCmd
		case app.InvoiceExpired:
			s.step = recvStepExpired
			return s, paymentHistoryChangedCmd
		case app.InvoiceCanceled:
			s.step = recvStepCanceled
			return s, paymentHistoryChangedCmd
		case app.InvoiceMissing:
			s.step = recvStepMissing
			return s, paymentHistoryChangedCmd
		}
	}
	// Only this screen schedules the next lookup. Closing its tab drops the
	// pending result or timer, so no further lookups are started.
	return s, scheduleInvoiceCheck(s.attempt)
}

func (s *ReceiveScreen) viewInput(w, h int) string {
	p := newPane(w)
	p.title(theme.Header, "⚡ Receive Payment")

	isFocused := s.ctx.ContentFocused
	amtFocused := isFocused &&
		s.focusZone == recvZoneAmount
	memoFocused := isFocused &&
		s.focusZone == recvZoneMemo

	p.input("Amount (sats):",
		s.amountInput.View(), amtFocused)
	p.blank()
	p.input("Memo (optional):",
		s.memoInput.View(), memoFocused)
	p.dim("Visible to the sender.")
	p.blank()

	blindFocused := isFocused &&
		s.focusZone == recvZoneBlind
	blindLabel := theme.Header
	blindMarker := " "
	if blindFocused {
		blindMarker = theme.NavActive.Render("▸")
	}
	blindValue := theme.Good.Render("● on")
	if !s.blindPaths {
		blindValue = theme.Dim.Render("○ off")
	}
	p.line(" " + blindLabel.Render("Blinded paths:"))
	p.line(blindMarker + " " + blindValue)
	p.dim("Hides your node identity on invoices.")

	p.appendError(s.inputError)

	btnFocused := isFocused &&
		s.focusZone == recvZoneButtons
	return p.renderWithBottomButtons(
		[]string{"Clear", "Create Invoice"},
		s.btnIdx, btnFocused, h, s.ctx.disabledWalletButtons(1)...)
}

func (s *ReceiveScreen) viewWaiting(
	w, h int,
) string {
	p := newPane(w)
	p.title(theme.Header, "Waiting for Payment")

	p.field("Amount: ",
		formatSats(s.invoice.AmountSats())+" sats")
	p.blank()

	if s.invoice.PaymentRequest() != "" {
		p.labelLine("Invoice:")
		display := s.invoice.PaymentRequest()
		maxChars := (w - 2) * 4 // ~4 wrapped lines
		if len(display) > maxChars {
			display = display[:maxChars] + "..."
		}
		p.monoWrap(display)
		p.blank()

		if s.lookupError != "" {
			p.warnWrap("Unable to check payment: " + s.lookupError)
			p.dim("Retrying while this tab remains open.")
		} else {
			p.dim("Waiting for payment...")
		}
	}

	if s.display.failed {
		p.warn("Copy display did not complete. Try again.")
	}
	btnFocused := s.ctx.ContentFocused
	return p.renderWithBottomButtons(
		[]string{"Show QR", "Copyable Invoice"},
		s.buttonIdx, btnFocused, h)
}

func (s *ReceiveScreen) viewPaid(
	w, h int,
) string {
	p := newPane(w)
	p.title(theme.Success, "Payment Received")
	p.field("Amount: ",
		formatSats(s.invoice.AmountSats())+" sats")
	return p.renderWithBottomButtons(
		[]string{"Done"}, 0,
		s.ctx.ContentFocused, h)
}

func (s *ReceiveScreen) viewInvoiceResult(
	w, h int,
) string {
	p := newPane(w)
	switch s.step {
	case recvStepCanceled:
		p.title(theme.Warning, "Invoice Canceled")
		p.dim("This invoice was canceled.")
	case recvStepMissing:
		p.title(theme.Warning, "Invoice unavailable")
		p.dim("LND no longer has this invoice.")
		p.dim("Payment status could not be determined.")
	default:
		p.title(theme.Warning, "Invoice Expired")
		p.dim("Create a new invoice to try again.")
	}
	return p.renderWithBottomButtons(
		[]string{"Done"}, 0,
		s.ctx.ContentFocused, h)
}
