package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/lndrpc"
	"github.com/virtpriv/node/internal/theme"
)

// ── Channel open screen steps ──────────────────────────

type chanOpenStep int

const (
	coStepInput       chanOpenStep = iota // peer + amount + fee + toggles + buttons
	coStepCustomPeer                      // pubkey + host fields + Go Back/Continue
	coStepCoinControl                     // UTXO table + Go Back / Confirm
	coStepConfirm                         // summary + Go Back / Confirm
	coStepOpening                         // in-flight
	coStepResult                          // success or error
)

// ── Focus zones for coStepInput ────────────────────────

const (
	coZonePeers   = 0
	coZoneAmounts = 1
	coZoneFee     = 2
	coZoneToggles = 3
	coZoneButtons = 4
)

// ── Focus zones for coStepCustomPeer ───────────────────

const (
	coCustomZonePubkey  = 0
	coCustomZoneHost    = 1
	coCustomZoneButtons = 2
)

// ── Focus zones for coStepCoinControl ─────────────────

const (
	coCCZoneList    = 0
	coCCZoneButtons = 1
)

// ── ChannelOpenScreen ──────────────────────────────────

type ChannelOpenScreen struct {
	ctx  *ScreenContext
	step chanOpenStep

	// Peer selection
	peerList     []peerOption
	peerIdx      int
	pubkeyInput  textinput.Model
	hostInput    textinput.Model
	customPubkey string
	customHost   string
	customAlias  string
	customZone   int
	customBtnIdx int

	// Amount selection
	amountIdx   int // 0=coin control btn, 1=amount
	amountInput AmountInput
	fundMax     bool

	// Fee rate
	feeInput AmountInput
	fees     feeSuggestions

	// Toggles
	private   bool
	taproot   bool
	toggleIdx int

	// UTXO selection (coin control)
	utxos       []lndrpc.UTXO
	txs         []lndrpc.OnChainTx
	selection   app.CoinSelection
	refresh     *channelOpenRefresh
	coinScope   walletObservationScope
	utxoErr     error
	utxoCursor  int
	utxoFetched bool
	ccZone      int // sub-step focus zone
	ccBtnIdx    int // sub-step button index

	// Selection state (✓ indicators)
	peerConfirmed   bool
	amountConfirmed bool

	// Navigation
	focusZone int

	// Buttons
	btnIdx int

	// Confirm
	confirmBtnIdx int

	// Result
	client  app.ChannelOpenClient
	attempt *channelOpenAttempt
	result  app.ChannelOpenResult
	error   string
}

func NewChannelOpenScreen(
	ctx *ScreenContext,
) *ChannelOpenScreen {
	s := &ChannelOpenScreen{
		ctx:          ctx,
		step:         coStepInput,
		peerList:     channelOpenPeers(),
		amountInput:  NewAmountInput(),
		feeInput:     NewFeeInput(),
		private:      true,
		taproot:      true,
		btnIdx:       1,
		pubkeyInput:  newChanPubkeyInput(),
		hostInput:    newChanHostInput(),
		customBtnIdx: 1,
	}
	if ctx.LndClient != nil {
		s.client = ctx.LndClient
	}
	return s
}

// ── Screen interface ────────────────────────────────────

func (s *ChannelOpenScreen) Init() tea.Cmd {
	return tea.Batch(s.refreshCoins(), s.fees.refresh(s.ctx))
}

func (s *ChannelOpenScreen) HandleKey(
	keyStr string, msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
	switch s.step {
	case coStepInput:
		return s.handleInputKey(keyStr, msg)
	case coStepCustomPeer:
		return s.handleCustomPeerKey(keyStr, msg)
	case coStepCoinControl:
		return s.handleCoinControlKey(keyStr)
	case coStepConfirm:
		return s.handleConfirmKey(keyStr)
	case coStepOpening:
		return s.handleOpeningKey(keyStr)
	case coStepResult:
		return s.handleResultKey(keyStr)
	}
	return s, nil
}

func (s *ChannelOpenScreen) HandleMsg(
	msg tea.Msg,
) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tabActivatedMsg:
		if s.step >= coStepOpening {
			return s, nil
		}
		return s, tea.Batch(s.refreshCoins(), s.fees.refresh(s.ctx))
	case tea.PasteMsg:
		return s.handlePaste(msg)
	case channelOpenResultMsg:
		return s.handleOpenResult(msg)
	case channelCoinsMsg:
		return s.handleCoinRefresh(msg)
	case feeSuggestionsMsg:
		return s.handleFeeSuggestions(msg)
	}
	return s, nil
}

func (s *ChannelOpenScreen) View(w, h int) string {
	switch s.step {
	case coStepInput:
		return s.viewInput(w, h)
	case coStepCustomPeer:
		return s.viewCustomPeer(w, h)
	case coStepCoinControl:
		return s.viewCoinControl(w, h)
	case coStepConfirm:
		return s.viewConfirm(w, h)
	case coStepOpening:
		return s.viewOpening(w, h)
	case coStepResult:
		return s.viewResult(w, h)
	}
	return ""
}

func (s *ChannelOpenScreen) HelpBindings() []key.Binding {
	switch s.step {
	case coStepInput:
		return s.inputBindings()
	case coStepCustomPeer:
		return s.customPeerBindings()
	case coStepCoinControl:
		return s.coinControlBindings()
	case coStepConfirm:
		return actionButtonBindings(
			s.confirmBtnIdx, s.ctx.HasTabs)
	case coStepOpening:
		return []key.Binding{kSidebar, kUpShiftTabBar}
	case coStepResult:
		return resultBindings(s.ctx.HasTabs)
	}
	return nil
}

// ── Input step ─────────────────────────────────────────

func (s *ChannelOpenScreen) handleInputKey(
	keyStr string, msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
	switch s.focusZone {
	case coZonePeers:
		return s.handlePeerListKey(keyStr)
	case coZoneAmounts:
		return s.handleAmountListKey(keyStr, msg)
	case coZoneFee:
		return s.handleFeeZoneKey(keyStr, msg)
	case coZoneToggles:
		return s.handleToggleKey(keyStr)
	case coZoneButtons:
		return s.handleButtonKey(keyStr)
	}
	return s, nil
}

func (s *ChannelOpenScreen) handlePeerListKey(
	keyStr string,
) (Screen, tea.Cmd) {
	customIdx := len(s.peerList)
	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit
	case "left":
		return s, emitFocusSidebar
	case "up":
		if s.peerIdx > 0 {
			s.peerIdx--
			s.peerConfirmed = false
		} else if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
		return s, nil
	case "down":
		if s.peerIdx < customIdx {
			s.peerIdx++
			s.peerConfirmed = false
		} else {
			s.focusZone = coZoneAmounts
		}
		return s, nil
	case "tab":
		s.focusZone = coZoneAmounts
		return s, nil
	case "shift+tab":
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
		return s, nil
	case "enter":
		if s.peerIdx == customIdx {
			// Open custom peer sub-step
			s.pubkeyInput = newChanPubkeyInput()
			s.hostInput = newChanHostInput()
			cw := tuiWidth - 2 - 14 - 1 - 6
			if cw > 58 {
				cw = 58
			}
			if cw < 20 {
				cw = 20
			}
			s.pubkeyInput.SetWidth(cw)
			s.hostInput.SetWidth(cw)
			s.customZone = coCustomZonePubkey
			s.customBtnIdx = 1
			s.error = ""
			s.step = coStepCustomPeer
			return s, nil
		}
		// Curated peer: confirm + advance
		s.peerConfirmed = true
		s.focusZone = coZoneAmounts
		return s, nil
	case "backspace":
		return s, emitFocusParent
	}
	return s, nil
}

func (s *ChannelOpenScreen) handleAmountListKey(
	keyStr string, msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
	onCoinCtrl := s.amountIdx == 0
	editing := s.amountIdx == 1 &&
		s.amountInput.Focused()
	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit
	case "left":
		if editing && !s.amountInput.Empty() {
			cmd := s.amountInput.Update(tea.Msg(msg))
			return s, cmd
		}
		return s, emitFocusSidebar
	case "right":
		if editing {
			cmd := s.amountInput.Update(tea.Msg(msg))
			return s, cmd
		}
		return s, nil
	case "up":
		if onCoinCtrl {
			s.enterPeersBackward()
		} else {
			// Amount → coin control
			if editing {
				s.amountInput.Blur()
			}
			s.amountIdx = 0
		}
		return s, nil
	case "down":
		if onCoinCtrl {
			// Coin control → amount
			s.amountIdx = 1
			if !s.amountConfirmed {
				s.amountInput.Focus()
			}
			return s, nil
		}
		// Amount: advance to fee
		if editing && !s.amountInput.Empty() {
			if !s.confirmAmountAndAdvance() {
				return s, nil
			}
			return s, nil
		}
		if editing {
			s.amountInput.Blur()
		}
		s.focusZone = coZoneFee
		s.feeInput.Focus()
		return s, nil
	case "tab":
		if editing && !s.amountInput.Empty() {
			if !s.confirmAmountAndAdvance() {
				return s, nil
			}
			return s, nil
		}
		if editing {
			s.amountInput.Blur()
		}
		s.focusZone = coZoneFee
		s.feeInput.Focus()
		return s, nil
	case "shift+tab":
		if editing {
			s.amountInput.Blur()
		}
		s.enterPeersBackward()
		return s, nil
	case "backspace":
		if editing {
			cmd := s.amountInput.Update(tea.Msg(msg))
			return s, cmd
		}
		return s, emitFocusParent
	case "enter":
		if onCoinCtrl {
			// Open coin control sub-step
			s.ccZone = coCCZoneList
			s.ccBtnIdx = 1
			s.error = ""
			s.step = coStepCoinControl
			return s, nil
		}
		if s.amountConfirmed && !editing {
			// Unlock editing on auto-confirmed amount
			s.amountConfirmed = false
			s.amountInput.Focus()
			return s, nil
		}
		if editing {
			if !s.confirmAmountAndAdvance() {
				return s, nil
			}
			return s, nil
		}
		return s, nil
	}
	if editing {
		cmd := s.amountInput.Update(tea.Msg(msg))
		return s, cmd
	}
	return s, nil
}

func (s *ChannelOpenScreen) handleToggleKey(
	keyStr string,
) (Screen, tea.Cmd) {
	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit
	case "left":
		return s, emitFocusSidebar
	case "up":
		if s.toggleIdx > 0 {
			s.toggleIdx--
		} else {
			s.enterFeeBackward()
		}
		return s, nil
	case "down":
		if s.toggleIdx < 1 {
			s.toggleIdx++
		} else {
			s.focusZone = coZoneButtons
		}
		return s, nil
	case "tab":
		s.focusZone = coZoneButtons
		return s, nil
	case "shift+tab":
		s.enterFeeBackward()
		return s, nil
	case "enter":
		s.focusZone = coZoneButtons
		return s, nil
	case "space":
		switch s.toggleIdx {
		case 0:
			if s.private && s.taproot {
				s.error = "Taproot channels must be private; select Legacy first"
				return s, nil
			}
			s.private = !s.private
		case 1:
			if !s.taproot && !s.private {
				s.error = "Taproot channels must be private; select Private first"
				return s, nil
			}
			s.taproot = !s.taproot
		}
		s.error = ""
		return s, nil
	case "backspace":
		return s, emitFocusParent
	}
	return s, nil
}

func (s *ChannelOpenScreen) handleButtonKey(
	keyStr string,
) (Screen, tea.Cmd) {
	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit
	case "left":
		if s.btnIdx > 0 {
			s.btnIdx--
		} else {
			return s, emitFocusSidebar
		}
		return s, nil
	case "right":
		if s.btnIdx < 1 {
			s.btnIdx++
		}
		return s, nil
	case "up":
		s.enterTogglesBackward()
		return s, nil
	case "tab":
		return s, nil
	case "shift+tab":
		s.enterTogglesBackward()
		return s, nil
	case "enter":
		switch s.btnIdx {
		case 0: // Clear
			s.clearForm()
			return s, s.refreshCoins()
		case 1: // Open Channel
			return s.prepareChannelOpenConfirmation()
		}
		return s, nil
	case "backspace":
		return s, emitFocusParent
	}
	return s, nil
}

// ── Fee zone key handling ─────────────────────────────

func (s *ChannelOpenScreen) handleFeeZoneKey(
	keyStr string, msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit
	case "left":
		if !s.feeInput.Empty() {
			cmd := s.fees.edit(&s.feeInput, tea.Msg(msg))
			return s, cmd
		}
		return s, emitFocusSidebar
	case "right":
		if !s.feeInput.Empty() {
			cmd := s.fees.edit(&s.feeInput, tea.Msg(msg))
			return s, cmd
		}
		return s, nil
	case "up":
		s.feeInput.Blur()
		s.enterAmountsBackward()
		return s, nil
	case "down":
		s.feeInput.Blur()
		s.focusZone = coZoneToggles
		s.toggleIdx = 0
		return s, nil
	case "tab":
		s.feeInput.Blur()
		s.focusZone = coZoneToggles
		s.toggleIdx = 0
		return s, nil
	case "shift+tab":
		s.feeInput.Blur()
		s.enterAmountsBackward()
		return s, nil
	case "backspace":
		cmd := s.fees.edit(&s.feeInput, tea.Msg(msg))
		return s, cmd
	case "enter":
		s.feeInput.Blur()
		s.focusZone = coZoneToggles
		s.toggleIdx = 0
		return s, nil
	default:
		cmd := s.fees.edit(&s.feeInput, tea.Msg(msg))
		return s, cmd
	}
}

// ── Paste handling ─────────────────────────────────────

func (s *ChannelOpenScreen) handlePaste(
	msg tea.PasteMsg,
) (Screen, tea.Cmd) {
	if s.step == coStepCustomPeer {
		var cmd tea.Cmd
		if s.customZone == coCustomZonePubkey {
			s.pubkeyInput, cmd =
				s.pubkeyInput.Update(msg)
		} else if s.customZone == coCustomZoneHost {
			s.hostInput, cmd =
				s.hostInput.Update(msg)
		}
		return s, cmd
	}
	if s.step == coStepInput &&
		s.focusZone == coZoneAmounts &&
		s.amountIdx == 1 &&
		s.amountInput.Focused() {
		cmd := s.amountInput.Update(msg)
		return s, cmd
	}
	if s.step == coStepInput &&
		s.focusZone == coZoneFee {
		cmd := s.fees.edit(&s.feeInput, msg)
		return s, cmd
	}
	return s, nil
}

// ── Async message handlers ─────────────────────────────

func (s *ChannelOpenScreen) handleOpenResult(
	msg channelOpenResultMsg,
) (Screen, tea.Cmd) {
	if s.step != coStepOpening || s.attempt == nil || msg.attempt != s.attempt {
		return s, nil
	}
	s.result = msg.result
	s.step = coStepResult
	return s, channelHistoryChangedCmd
}

func (s *ChannelOpenScreen) handleFeeSuggestions(
	msg feeSuggestionsMsg,
) (Screen, tea.Cmd) {
	if !s.fees.complete(msg, s.ctx.Cfg.Network) || s.step >= coStepConfirm {
		return s, nil
	}
	if rate := s.fees.defaultRate(s.ctx.Cfg.Network); !s.fees.edited && s.feeInput.Empty() && rate > 0 {
		s.feeInput.SetSats(rate)
	}
	return s, nil
}

// ── Form actions ───────────────────────────────────────

func (s *ChannelOpenScreen) validateCustomAmount() (int64, string) {
	n := s.amountInput.Sats()
	if err := app.ValidateChannelAmount(n); err != nil {
		return 0, err.Error()
	}
	return n, ""
}

// confirmAmountAndAdvance validates the amount input,
// commits it, detects FundMax, and advances to fee zone.
// Returns false on validation failure.
func (s *ChannelOpenScreen) confirmAmountAndAdvance() bool {
	n, errMsg := s.validateCustomAmount()
	if errMsg != "" {
		s.error = errMsg
		return false
	}
	s.error = ""
	// FundMax when amount matches selected UTXO total
	total, err := s.selection.Total(s.utxos)
	s.fundMax = err == nil && s.selection.Len() > 0 && n == total
	s.amountConfirmed = true
	s.amountInput.Blur()
	s.focusZone = coZoneFee
	s.feeInput.Focus()
	return true
}

// Backward navigation keeps entered values but requires another explicit review.
func (s *ChannelOpenScreen) enterPeersBackward() {
	s.focusZone = coZonePeers
	s.peerConfirmed = false
	s.error = ""
}

func (s *ChannelOpenScreen) enterAmountsBackward() {
	s.focusZone = coZoneAmounts
	s.amountConfirmed = false
	s.error = ""
	s.amountIdx = 1
	s.amountInput.Focus()
}

func (s *ChannelOpenScreen) enterFeeBackward() {
	s.focusZone = coZoneFee
	s.feeInput.Focus()
	s.error = ""
}

func (s *ChannelOpenScreen) enterTogglesBackward() {
	s.focusZone = coZoneToggles
	s.error = ""
}

func (s *ChannelOpenScreen) clearForm() {
	s.peerIdx = 0
	s.peerConfirmed = false
	s.customPubkey = ""
	s.customHost = ""
	s.customAlias = ""
	s.amountIdx = 0
	s.amountConfirmed = false
	s.amountInput.Clear()
	s.fundMax = false
	s.feeInput = NewFeeInput()
	s.fees.edited = false
	if rate := s.fees.defaultRate(s.ctx.Cfg.Network); rate > 0 {
		s.feeInput.SetSats(rate)
	}
	s.private = true
	s.taproot = true
	s.toggleIdx = 0
	s.selection.Clear()
	s.utxoErr = nil
	s.attempt = nil
	s.cancelCoinRefresh()
	s.utxoCursor = 0
	s.utxoFetched = false
	s.txs = nil
	s.focusZone = coZonePeers
	s.btnIdx = 1
	s.error = ""
}

func (s *ChannelOpenScreen) prepareChannelOpenConfirmation() (
	Screen, tea.Cmd,
) {
	if !s.requireCurrentCoinScope() {
		return s, nil
	}
	if !s.ctx.walletExists() {
		return s, nil
	}
	if !s.peerConfirmed {
		s.error = "Select a peer first"
		return s, nil
	}
	if !s.amountConfirmed {
		s.error = "Select a channel size first"
		return s, nil
	}
	if s.utxoErr != nil {
		s.error = s.utxoErr.Error()
		return s, nil
	}
	prepared, err := app.PrepareChannelOpen(app.ChannelOpenInput{
		Pubkey: s.selectedPubkey(), Host: s.selectedHost(),
		AmountSats: s.amountInput.Sats(), FundMax: s.fundMax,
		Private: s.private, Taproot: s.taproot, SatPerVbyte: s.feeInput.Sats(),
		Outpoints: s.selection.Outpoints(),
	}, s.utxos)
	if err != nil {
		s.error = err.Error()
		return s, nil
	}
	s.attempt = &channelOpenAttempt{prepared: prepared, alias: s.selectedAlias()}
	s.error = ""
	s.confirmBtnIdx = 0
	s.step = coStepConfirm
	return s, nil
}

// ── Helpers ────────────────────────────────────────────

func (s *ChannelOpenScreen) selectedPubkey() string {
	if s.peerIdx < len(s.peerList) {
		return s.peerList[s.peerIdx].Pubkey
	}
	return s.customPubkey
}

func (s *ChannelOpenScreen) selectedHost() string {
	if s.peerIdx < len(s.peerList) {
		return s.peerList[s.peerIdx].Host
	}
	return s.customHost
}

func (s *ChannelOpenScreen) selectedAlias() string {
	if s.peerIdx < len(s.peerList) {
		return s.peerList[s.peerIdx].Alias
	}
	if s.customAlias != "" {
		return s.customAlias
	}
	return "Custom peer"
}

// ── Views ──────────────────────────────────────────────

func (s *ChannelOpenScreen) viewInput(
	w, h int,
) string {
	p := newPane(w)
	p.title(theme.Header, "Open Channel")

	p.field("On-Chain Balance: ", onChainBalanceText(s.ctx.Status))
	p.blank()

	isFocused := s.ctx.ContentFocused

	// ── Peer list ──
	p.line(" " + theme.Header.Render("Select a peer:"))
	maxAlias := 0
	for _, peer := range s.peerList {
		maxAlias = max(maxAlias, min(len(peer.Alias), 20))
	}
	for i, peer := range s.peerList {
		prefix := " "
		style := theme.Value
		isCursor := isFocused &&
			s.focusZone == coZonePeers &&
			s.peerIdx == i
		isConfirmed := s.peerConfirmed &&
			s.peerIdx == i
		if isCursor {
			prefix = theme.NavActive.Render("▸")
			style = theme.Action
		}
		if isConfirmed {
			prefix = "✓"
			style = theme.Action
		}
		name := peer.Alias
		if len(name) > 20 {
			name = name[:20]
		}
		var details []string
		if peer.Taproot {
			details = append(details, "Taproot")
		}
		if peer.TorOnly {
			details = append(details, "Tor")
		}
		if peer.MinChanSize > 0 {
			details = append(details,
				formatSatsCompact(peer.MinChanSize)+
					" min")
		}
		tags := "(" + strings.Join(details, " · ") + ")"
		pad := maxAlias + 2 - len(name)
		if pad < 2 {
			pad = 2
		}
		p.line(fmt.Sprintf("%s %s%*s%s",
			prefix, style.Render(name),
			pad, "",
			theme.Dim.Render(tags)))
	}
	// [Custom peer] option
	customPrefix := " "
	customStyle := theme.Value
	customCursor := isFocused &&
		s.focusZone == coZonePeers &&
		s.peerIdx == len(s.peerList)
	customConfirmed := s.peerConfirmed &&
		s.peerIdx == len(s.peerList)
	if customCursor {
		customPrefix = theme.NavActive.Render("▸")
		customStyle = theme.Action
	}
	if customConfirmed {
		customPrefix = "✓"
		customStyle = theme.Action
	}
	customLabel := "[Custom peer]"
	if s.customPubkey != "" {
		customLabel = fmt.Sprintf("[%s]",
			s.customAlias)
	}
	p.line(fmt.Sprintf("%s %s",
		customPrefix,
		customStyle.Render(customLabel)))
	p.blank()

	// ── Channel size: coin control + amount ──
	p.line(" " + theme.Header.Render("Channel size:"))

	amtFocused := isFocused &&
		s.focusZone == coZoneAmounts

	// Coin control button
	ccPrefix := " "
	ccStyle := theme.Value
	if amtFocused && s.amountIdx == 0 {
		ccPrefix = theme.NavActive.Render("▸")
		ccStyle = theme.Action
	}
	ccLabel := "[Coin control]"
	if s.selection.Len() > 0 {
		ccLabel = "[Coin control: " + s.selectionSummary() + "]"
	}
	p.line(fmt.Sprintf("%s %s",
		ccPrefix, ccStyle.Render(ccLabel)))

	// Amount line
	amtPrefix := " "
	amtStyle := theme.Value
	amtCursor := amtFocused && s.amountIdx == 1
	if amtCursor {
		amtPrefix = theme.NavActive.Render("▸")
		amtStyle = theme.Action
	}
	if s.amountConfirmed {
		amtPrefix = "✓"
		amtStyle = theme.Action
	}

	if s.amountConfirmed && !s.amountInput.Focused() {
		label := formatSats(s.amountInput.Sats()) + " sats"
		if s.fundMax {
			label = "Max (capacity determined by LND)"
		}
		p.line(fmt.Sprintf("%s %s", amtPrefix, amtStyle.Render(label)))
	} else {
		s.amountInput.SetWidth(min(w-14, 20))
		p.line(fmt.Sprintf("%s %s %s", amtPrefix, amtStyle.Render("Amount:"), s.amountInput.View()))
		if total, err := s.selection.Total(s.utxos); err == nil && total > 0 && s.amountInput.Sats() == total {
			p.dim(" Max: LND determines capacity and any change.")
		}
	}
	p.blank()

	// ── Fee rate ──
	feeActive := isFocused &&
		s.focusZone == coZoneFee
	feeMarker := " "
	if feeActive {
		feeMarker = theme.NavActive.Render("▸")
	}
	p.line(" " + theme.Header.Render(
		"Fee Rate (sat/vB):"))
	p.line(feeMarker + " " + s.feeInput.View())
	hints := s.fees.hints(s.ctx.Cfg.Network)
	if hints != "" {
		p.line("  " + theme.Dim.Render(hints))
	}
	p.blank()

	// ── Toggles ──
	p.line(" " + theme.Header.Render("Channel type:"))
	toggleFocused := isFocused &&
		s.focusZone == coZoneToggles
	s.addToggles(p, toggleFocused)

	// ── Error ──
	p.appendError(s.error)

	// ── Buttons pinned to bottom ──
	btnFocused := isFocused &&
		s.focusZone == coZoneButtons
	return p.renderWithBottomButtons(
		[]string{"Clear", "Open Channel"},
		s.btnIdx, btnFocused, h, s.ctx.disabledWalletButtons(1)...)
}

func (s *ChannelOpenScreen) addToggles(
	p *paneBuilder, focused bool,
) {
	p.line(renderToggleSwitch(
		"Private", "Public",
		s.private,
		focused && s.toggleIdx == 0))
	p.line(renderToggleSwitch(
		"Taproot", "Legacy",
		s.taproot,
		focused && s.toggleIdx == 1))
}

// renderToggleSwitch renders a bracket-style toggle with
// both options visible. The knob slides left/right inside
// the housing to indicate the active selection.
//
//	Public [ ━━● ] Private   (on=true, Private selected)
//	Legacy [ ●━━ ] Taproot   (on=false, Legacy selected)
func renderToggleSwitch(
	label string, altLabel string,
	on bool, focused bool,
) string {
	leftW := 8 // right-align left labels for alignment

	activeStyle := theme.Value
	if focused {
		activeStyle = theme.Action
	}

	var leftStyled, rightStyled, toggle string
	bracket := theme.Dim
	if on {
		leftStyled = theme.Dim.Render(
			fmt.Sprintf("%*s", leftW, altLabel))
		rightStyled = activeStyle.Render(label)
		toggle = bracket.Render("[ ") +
			theme.Dim.Render("━━") +
			activeStyle.Render("●") +
			bracket.Render(" ]")
	} else {
		leftStyled = activeStyle.Render(
			fmt.Sprintf("%*s", leftW, altLabel))
		rightStyled = theme.Dim.Render(label)
		toggle = bracket.Render("[ ") +
			activeStyle.Render("●") +
			theme.Dim.Render("━━") +
			bracket.Render(" ]")
	}

	prefix := " "
	if focused {
		prefix = theme.NavActive.Render("▸")
	}

	return prefix + leftStyled + " " +
		toggle + " " + rightStyled
}

// ── Helpbar bindings ───────────────────────────────────

func (s *ChannelOpenScreen) inputBindings() []key.Binding {
	switch s.focusZone {
	case coZonePeers:
		return s.peerListBindings()
	case coZoneAmounts:
		return s.amountListBindings()
	case coZoneFee:
		return s.feeZoneBindings()
	case coZoneToggles:
		return s.toggleBindings()
	case coZoneButtons:
		return s.buttonBindings()
	}
	return nil
}

func (s *ChannelOpenScreen) peerListBindings() []key.Binding {
	binds := []key.Binding{
		kUpDownSelect, kTabNext, kEnterConfirm,
		kSidebar,
	}
	if s.ctx.HasTabs {
		binds = append(binds, kShiftTabBar)
	}
	binds = append(binds, kBack, kQuit)
	return binds
}

func (s *ChannelOpenScreen) amountListBindings() []key.Binding {
	return []key.Binding{
		kUpDownSelect, kTabNext,
		bind("⇧tab", "peers", "shift+tab"),
		kEnterConfirm, kSidebar, kBack, kQuit,
	}
}

func (s *ChannelOpenScreen) toggleBindings() []key.Binding {
	return []key.Binding{
		kUpDownSelect,
		bind("space", "toggle", "space"),
		kEnterNext,
		bind("⇧tab", "fee", "shift+tab"),
		kSidebar, kBack, kQuit,
	}
}

func (s *ChannelOpenScreen) feeZoneBindings() []key.Binding {
	return []key.Binding{
		kLeftRightCursor, kTabNext,
		bind("⇧tab", "amount", "shift+tab"),
		kSidebar, kBack, kQuit,
	}
}

func (s *ChannelOpenScreen) buttonBindings() []key.Binding {
	binds := buttonNav(s.btnIdx)
	binds = append(binds, kEnter,
		bind("⇧tab", "toggles", "shift+tab"),
		kBack, kQuit)
	return binds
}

// ── channelOpenPeers ───────────────────────────────────

func channelOpenPeers() []peerOption {
	return []peerOption{
		{
			Alias:       "Zeus",
			Pubkey:      "031b301307574bbe9b9ac7b79cbe1700e31e544513eae0b5d7497483083f99e581",
			Host:        "r46dwvxcdri754hf6n3rwexmc53h5x4natg5g6hidnxfzejm5xrqn2id.onion:9735",
			TorOnly:     true,
			Taproot:     true,
			MinChanSize: 150000,
		},
		{
			Alias:       "ACINQ",
			Pubkey:      "03864ef025fde8fb587d989186ce6a4a186895ee44a926bfc370e2c366597a3f8f",
			Host:        "of7husrflx7sforh3fw6yqlpwstee3wg5imvvmkp4bz6rbjxtg5nljad.onion:9735",
			TorOnly:     true,
			MinChanSize: 400000,
		},
		{
			Alias:       "LNBig",
			Pubkey:      "034ea80f8b148c750463546bd999bf7321a0e6dfc60aaf84bd0400a2e8d376c0d5",
			Host:        "qimt6abvc2iuexwrtl5tzyrygnu7mshjahvresve5hdli6nstdg7elyd.onion:9735",
			TorOnly:     true,
			MinChanSize: 500000,
		},
	}
}
