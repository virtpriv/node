package tui

import (
	"fmt"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/theme"
)

type closeStep int

const (
	closeStepType    closeStep = iota // cooperative / force
	closeStepFee                      // fee input + buttons
	closeStepReview                   // immutable approval
	closeStepClosing                  // checking and submitting
	closeStepResult                   // submission outcome
)

const (
	closeTypeZoneOptions = 0
	closeTypeZoneButtons = 1
)

const (
	closeZoneFee     = 0
	closeZoneButtons = 1
)

type ChannelCloseScreen struct {
	ctx    *ScreenContext
	client app.ChannelCloseClient
	step   closeStep

	// Channel info (snapshot at creation)
	chanPoint string
	peerAlias string
	capacity  int64
	localBal  int64

	// Type step
	typeIdx    int // 0=cooperative, 1=force
	typeBtnIdx int // 0=Go Back, 1=Confirm

	// Fee form and final approval
	force         bool
	feeInput      AmountInput
	fees          feeSuggestions
	focusZone     int // type step: 0=options, 1=buttons; fee step: 0=fee, 1=buttons
	confirmBtnIdx int // 0=Go Back, 1=Review or close action
	attempt       *channelCloseAttempt
	result        app.ChannelCloseResult

	// Input validation error
	error string

	// Cancelled is set when the user presses Cancel
	// on the type step. The embedding screen checks
	// this after delegation to dismiss the close flow.
	Cancelled bool
}

func NewChannelCloseScreen(
	ctx *ScreenContext,
	chanPoint string,
	peerAlias string,
	capacity int64,
	localBal int64,
) *ChannelCloseScreen {
	return &ChannelCloseScreen{
		ctx:       ctx,
		client:    ctx.LndClient,
		step:      closeStepType,
		chanPoint: chanPoint,
		peerAlias: peerAlias,
		capacity:  capacity,
		localBal:  localBal,
	}
}

func (s *ChannelCloseScreen) Init() tea.Cmd {
	return s.fees.refresh(s.ctx)
}

func (s *ChannelCloseScreen) HandleKey(
	keyStr string, msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
	switch s.step {
	case closeStepType:
		return s.handleTypeKey(keyStr)
	case closeStepFee:
		return s.handleFeeKey(keyStr, msg)
	case closeStepReview:
		return s.handleReviewKey(keyStr)
	case closeStepClosing:
		return s.handleClosingKey(keyStr)
	case closeStepResult:
		return s.handleResultKey(keyStr)
	}
	return s, nil
}

func (s *ChannelCloseScreen) HandleMsg(
	msg tea.Msg,
) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case channelCloseResultMsg:
		return s.handleCloseResult(msg)
	case tabActivatedMsg:
		if s.step < closeStepReview {
			return s, s.fees.refresh(s.ctx)
		}
	case feeSuggestionsMsg:
		s.fees.complete(msg, s.ctx.Cfg.Network)
		return s, nil
	case tea.PasteMsg:
		if s.step == closeStepFee &&
			!s.force &&
			s.focusZone == closeZoneFee {
			cmd := s.feeInput.Update(msg)
			return s, cmd
		}
		return s, nil
	}
	return s, nil
}

func (s *ChannelCloseScreen) View(
	w, h int,
) string {
	switch s.step {
	case closeStepType:
		return s.viewType(w, h)
	case closeStepFee:
		return s.viewFee(w, h)
	case closeStepReview:
		return s.viewReview(w, h)
	case closeStepClosing:
		return s.viewClosing(w, h)
	case closeStepResult:
		return s.viewResult(w, h)
	}
	return ""
}

func (s *ChannelCloseScreen) HelpBindings() []key.Binding {
	switch s.step {
	case closeStepType:
		return s.typeBindings()
	case closeStepFee:
		return s.feeBindings()
	case closeStepReview:
		return actionButtonBindings(s.confirmBtnIdx, s.ctx.HasTabs)
	case closeStepClosing:
		return []key.Binding{kSidebar, kUpShiftTabBar}
	case closeStepResult:
		return resultBindings(s.ctx.HasTabs)
	}
	return nil
}

func (s *ChannelCloseScreen) handleTypeKey(
	keyStr string,
) (Screen, tea.Cmd) {
	// Buttons zone
	if s.focusZone == closeTypeZoneButtons {
		return s.handleTypeBtnKey(keyStr)
	}

	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit

	case "left":
		return s, emitFocusSidebar

	case "up":
		if s.typeIdx > 0 {
			s.typeIdx--
			return s, nil
		}
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
		return s, nil

	case "down", "tab":
		if s.typeIdx < 1 {
			s.typeIdx++
			return s, nil
		}
		// Move from the last option to the buttons.
		s.focusZone = closeTypeZoneButtons
		s.typeBtnIdx = 1 // default to Confirm
		return s, nil

	case "shift+tab":
		if s.typeIdx > 0 {
			s.typeIdx--
			return s, nil
		}
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
		return s, nil

	case "backspace":
		s.Cancelled = true
		return s, nil

	case "enter":
		// Select type and move to buttons with
		// Confirm focused
		s.focusZone = closeTypeZoneButtons
		s.typeBtnIdx = 1
		return s, nil
	}
	return s, nil
}

func (s *ChannelCloseScreen) handleTypeBtnKey(
	keyStr string,
) (Screen, tea.Cmd) {
	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit
	case "left":
		if s.typeBtnIdx > 0 {
			s.typeBtnIdx--
			return s, nil
		}
		return s, emitFocusSidebar
	case "right":
		if s.typeBtnIdx < 1 {
			s.typeBtnIdx++
		}
		return s, nil
	case "up", "shift+tab":
		s.focusZone = closeTypeZoneOptions
		return s, nil
	case "down", "tab":
		return s, nil
	case "backspace":
		s.Cancelled = true
		return s, nil
	case "enter":
		if s.typeBtnIdx == 0 { // Go Back
			s.Cancelled = true
			return s, nil
		}
		// Force close has no editable fee policy.
		s.force = s.typeIdx == 1
		s.confirmBtnIdx = 0
		s.error = ""

		if !s.force {
			s.feeInput = NewFeeInput()
			if rate := s.fees.defaultRate(s.ctx.Cfg.Network); rate > 0 {
				s.feeInput.SetSats(rate)
			}
			s.feeInput.Focus()
			s.focusZone = closeZoneFee
		} else {
			s.focusZone = closeZoneButtons
			return s.prepareClose()
		}

		s.step = closeStepFee
		return s, nil
	}
	return s, nil
}

func (s *ChannelCloseScreen) handleFeeKey(
	keyStr string, msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
	if s.focusZone == closeZoneFee {
		return s.handleFeeInputKey(keyStr, msg)
	}

	return s.handleFeeBtnKey(keyStr)
}

func (s *ChannelCloseScreen) handleFeeInputKey(
	keyStr string, msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit

	case "left":
		if !s.feeInput.Empty() {
			cmd := s.feeInput.Update(tea.Msg(msg))
			return s, cmd
		}
		return s, emitFocusSidebar

	case "right":
		cmd := s.feeInput.Update(tea.Msg(msg))
		return s, cmd

	case "backspace":
		cmd := s.feeInput.Update(tea.Msg(msg))
		return s, cmd

	case "down", "tab", "enter":
		s.feeInput.Blur()
		s.focusZone = closeZoneButtons
		s.confirmBtnIdx = 1 // default to action
		return s, nil

	case "up", "shift+tab":
		if s.ctx.HasTabs {
			s.feeInput.Blur()
			return s, emitFocusTabBar
		}
		return s, nil

	default:
		cmd := s.feeInput.Update(tea.Msg(msg))
		return s, cmd
	}
}

func (s *ChannelCloseScreen) handleFeeBtnKey(
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

	case "up", "shift+tab":
		s.focusZone = closeZoneFee
		s.feeInput.Focus()
		return s, nil

	case "down", "tab":
		return s, nil

	case "backspace":
		s.step = closeStepType
		s.focusZone = closeTypeZoneOptions
		s.error = ""
		return s, nil

	case "enter":
		switch s.confirmBtnIdx {
		case 0: // Go Back
			s.step = closeStepType
			s.focusZone = closeTypeZoneOptions
			s.error = ""
			return s, nil
		case 1: // Review
			return s.prepareClose()
		}
	}
	return s, nil
}

func (s *ChannelCloseScreen) handleClosingKey(
	keyStr string,
) (Screen, tea.Cmd) {
	switch keyStr {
	case "left":
		return s, emitFocusSidebar
	case "up", "shift+tab":
		return s, emitFocusTabBar
	}
	return s, nil
}

func (s *ChannelCloseScreen) handleResultKey(
	keyStr string,
) (Screen, tea.Cmd) {
	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit
	case "enter":
		return s, func() tea.Msg { return closeChannelDoneMsg{screen: s} }
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

func (s *ChannelCloseScreen) handleCloseResult(msg channelCloseResultMsg) (Screen, tea.Cmd) {
	if s.step != closeStepClosing || s.attempt == nil || msg.attempt != s.attempt {
		return s, nil
	}
	s.result = msg.result
	s.step = closeStepResult
	return s, channelHistoryChangedCmd
}

func (s *ChannelCloseScreen) prepareClose() (Screen, tea.Cmd) {
	rate := s.feeInput.Sats()
	if s.force {
		rate = 0
	}
	prepared, err := app.PrepareChannelClose(app.ChannelCloseInput{ChannelPoint: s.chanPoint, Force: s.force, SatPerVbyte: rate})
	if err != nil {
		s.error = err.Error()
		return s, nil
	}
	s.attempt = &channelCloseAttempt{prepared: prepared, alias: s.peerAlias, capacity: s.capacity, localBalance: s.localBal}
	s.step = closeStepReview
	s.confirmBtnIdx = 0
	s.error = ""
	return s, nil
}

func (s *ChannelCloseScreen) handleReviewKey(key string) (Screen, tea.Cmd) {
	back := func() {
		s.attempt = nil
		s.confirmBtnIdx = 0
		if s.force {
			s.step = closeStepType
			s.focusZone = closeTypeZoneOptions
		} else {
			s.step = closeStepFee
			s.focusZone = closeZoneButtons
		}
	}
	switch key {
	case "ctrl+c":
		return s, tea.Quit
	case "left":
		if s.confirmBtnIdx > 0 {
			s.confirmBtnIdx--
		} else {
			return s, emitFocusSidebar
		}
	case "right":
		s.confirmBtnIdx = 1
	case "up", "shift+tab":
		return s, emitFocusTabBar
	case "backspace":
		back()
	case "enter":
		if s.confirmBtnIdx == 0 {
			back()
			return s, nil
		}
		if s.attempt == nil || !s.ctx.walletExists() {
			return s, nil
		}
		s.step = closeStepClosing
		return s, closeChannelCmd(s.client, s.attempt)
	}
	return s, nil
}

type closeChannelDoneMsg struct{ screen *ChannelCloseScreen }

func channelCloseBusy(screen Screen) bool {
	detail, ok := screen.(*ChannelDetailScreen)
	return ok && detail.closeScreen != nil && detail.closeScreen.step == closeStepClosing
}

func (s *ChannelCloseScreen) viewReview(w, h int) string {
	p := newPane(w)
	req := s.attempt.prepared.Request()
	p.title(theme.Warning, "Confirm Channel Close")
	p.field("Peer: ", s.attempt.alias)
	p.labelLine("Funding outpoint:")
	p.monoWrap(req.ChannelPoint)
	p.line("Capacity: " + formatSats(s.attempt.capacity) + " sats")
	p.line("Local balance snapshot: " + formatSats(s.attempt.localBalance) + " sats")
	if req.Force {
		p.warn("Type: Force close (unilateral)")
		p.line("Commitment fee is predefined; sweeps add fees.")
		p.warnWrapWords("Recovery depends on channel timelocks, HTLCs and confirmations. Funds are not immediately available.")
	} else {
		p.line("Type: Cooperative close")
		if req.SatPerVbyte == 0 {
			p.line("Fee rate: auto (LND default)")
		} else {
			p.line(fmt.Sprintf("Requested fee rate: %d sat/vB", req.SatPerVbyte))
		}
		p.line("Total fee and returned amount: unknown before close")
		p.line("Requires peer cooperation; confirmation is still needed.")
	}
	p.dim("The local balance is not a payout quote.")
	p.warn("Closing a channel cannot be undone.")
	action := "Close Channel"
	if req.Force {
		action = "Force Close"
	}
	return p.renderWithBottomButtons([]string{"Go Back", action}, s.confirmBtnIdx, s.ctx.ContentFocused, h, s.ctx.disabledWalletButtons(1)...)
}

func (s *ChannelCloseScreen) viewType(
	w, h int,
) string {
	p := newPane(w)
	p.title(theme.Header, "Close Channel")

	p.field("Peer:     ", s.peerAlias)
	p.field("Capacity: ",
		formatSats(s.capacity)+" sats")
	p.field("Local:    ",
		formatSats(s.localBal)+" sats")
	p.blank()

	isFocused := s.ctx.ContentFocused
	onOptions := isFocused &&
		s.focusZone == closeTypeZoneOptions
	onButtons := isFocused &&
		s.focusZone == closeTypeZoneButtons

	p.line(" " +
		theme.Header.Render("Close type:"))
	p.blank()

	coopPrefix := " "
	coopStyle := theme.Value
	if onOptions && s.typeIdx == 0 {
		coopPrefix = theme.NavActive.Render("▸")
		coopStyle = theme.Action
	} else if onButtons && s.typeIdx == 0 {
		coopPrefix = "●"
		coopStyle = theme.Action
	}
	p.line(fmt.Sprintf("%s %s",
		coopPrefix,
		coopStyle.Render("Cooperative close")))
	p.line("  " + theme.Dim.Render(
		"Requires peer cooperation and confirmation."))
	p.blank()

	forcePrefix := " "
	forceStyle := theme.Value
	if onOptions && s.typeIdx == 1 {
		forcePrefix = theme.NavActive.Render("▸")
		forceStyle = theme.Warning
	} else if onButtons && s.typeIdx == 1 {
		forcePrefix = "●"
		forceStyle = theme.Warning
	}
	p.line(fmt.Sprintf("%s %s",
		forcePrefix,
		forceStyle.Render("Force close")))
	p.line("  " + theme.Dim.Render(
		"Unilateral. Timelocks and recovery fees apply."))

	p.appendError(s.error)
	return p.renderWithBottomButtons(
		[]string{"Go Back", "Confirm"},
		s.typeBtnIdx, onButtons, h)
}

func (s *ChannelCloseScreen) viewFee(w, h int) string {
	p := newPane(w)
	p.title(theme.Header, "Cooperative Close Fee")
	p.field("Peer: ", s.peerAlias)
	p.labelLine("Funding outpoint:")
	p.monoWrap(s.chanPoint)
	p.blank()
	p.input("Fee rate (sat/vB):", s.feeInput.View(), s.ctx.ContentFocused && s.focusZone == closeZoneFee)
	p.dim("Blank or zero uses LND's automatic fee policy.")
	p.dim("This is a requested rate, not a maximum total fee.")
	p.dim("Total fee and returned amount are not yet known.")
	if hints := s.fees.hints(s.ctx.Cfg.Network); hints != "" {
		p.blank()
		p.dim(hints)
	}
	p.appendError(s.error)
	return p.renderWithBottomButtons([]string{"Go Back", "Review"}, s.confirmBtnIdx, s.ctx.ContentFocused && s.focusZone == closeZoneButtons, h)
}

func (s *ChannelCloseScreen) viewClosing(
	w, h int,
) string {
	p := newPane(w)
	if s.attempt.prepared.Request().Force {
		p.title(theme.Warning,
			"Force Closing Channel...")
	} else {
		p.title(theme.Header,
			"Closing Channel...")
	}
	p.line(" " + theme.Value.Render(
		"Checking channel state and requesting close."))
	p.blank()
	p.dim("This may take several minutes over Tor.")
	p.dim("Do not close the terminal.")

	return p.renderWithBottomButtons(
		[]string{"Closing..."}, 0, false, h)
}

func (s *ChannelCloseScreen) viewResult(w, h int) string {
	p := newPane(w)
	switch s.result.State {
	case app.ClosePending:
		p.title(theme.Header, "Channel Closing")
		p.line("LND reported a closing transaction; confirmation is pending.")
		p.labelLine("Candidate closing TX:")
		p.monoWrap(s.result.Txid)
		p.dim("The candidate may change. Check pending channels and history.")
	case app.CloseConfirmed:
		p.title(theme.Success, "Close Transaction Confirmed")
		p.labelLine("Confirmed closing TX:")
		p.monoWrap(s.result.Txid)
	case app.CloseOutcomeUnknown:
		p.title(theme.Warning, "Channel Close Outcome Unknown")
		p.warnWrapWords("The close may still complete. Check pending channels and history before another request; repeating a close can request a fee bump.")
	default:
		p.title(theme.Warning, "Channel Close Not Submitted")
	}
	if s.attempt != nil {
		req := s.attempt.prepared.Request()
		p.blank()
		p.labelLine("Funding outpoint:")
		p.monoWrap(req.ChannelPoint)
		if req.Force && s.result.State != app.CloseNotSubmitted {
			p.warnWrapWords("Force-close outputs may still need timelocks, HTLC resolution and sweeps. Transaction confirmation is not full fund recovery.")
		}
	}
	if s.result.Err != nil {
		p.warnWrap(s.result.Err.Error())
	}
	return p.renderWithBottomButtons([]string{"Done"}, 0, s.ctx.ContentFocused, h)
}

func (s *ChannelCloseScreen) typeBindings() []key.Binding {
	var binds []key.Binding
	if s.focusZone == closeTypeZoneButtons {
		binds = append(binds,
			kLeftRightButtons, kEnter,
			bind("↑", "back", "up"),
			kSidebar)
	} else {
		binds = append(binds,
			bind("↑↓", "close type", "up", "down"),
			kEnter, kSidebar)
		if s.ctx.HasTabs {
			binds = append(binds, kShiftTabBar)
		}
	}
	binds = append(binds, kBack, kQuit)
	return binds
}

func (s *ChannelCloseScreen) feeBindings() []key.Binding {
	if s.focusZone == closeZoneFee {
		binds := []key.Binding{kTabNext, kEnterNext, kSidebar}
		if s.ctx.HasTabs {
			binds = append(binds, kShiftTabBar)
		}
		return append(binds, kQuit)
	}
	return []key.Binding{kLeftRightButtons, kEnter, bind("⇧tab", "fee", "shift+tab"), kSidebar, kBack, kQuit}
}
