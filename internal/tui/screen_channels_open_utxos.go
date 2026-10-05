package tui

import (
	"context"
	"fmt"
	"math"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/theme"
)

type channelOpenRefresh struct {
	screen    *ChannelOpenScreen
	scope     walletObservationScope
	cancel    context.CancelFunc
	coinsDone bool
	txsDone   bool
}

type channelCoinsMsg struct {
	refresh  *channelOpenRefresh
	snapshot app.OnChainSnapshot
}

// One form admits at most one same-scope collection. The shared app reader owns
// cancellation and joining; this screen owns selection and result publication.
func (s *ChannelOpenScreen) refreshCoins() tea.Cmd {
	if s.step >= coStepOpening {
		return nil
	}
	scope := s.ctx.walletObservationScope()
	if s.refresh != nil && s.refresh.scope == scope {
		return nil
	}
	s.cancelCoinRefresh()
	if s.coinScope != scope {
		if s.coinScope.client != scope.client {
			s.client = nil
			if scope.client != nil {
				s.client = scope.client
			}
		}
		s.utxos, s.txs = nil, nil
		s.utxoFetched, s.amountConfirmed = false, false
		s.utxoErr = nil
		s.selection.Clear()
		if s.step == coStepConfirm {
			s.backToInput()
		}
		s.coinScope = scope
	}
	if s.ctx.OnChain == nil {
		s.ctx.OnChain = &OnChainContext{}
	}
	if s.ctx.OnChain.reader == nil {
		s.ctx.OnChain.reader = app.NewOnChainReader()
	}
	reader := s.ctx.OnChain.reader
	ctx, cancel := context.WithCancel(context.Background())
	request := &channelOpenRefresh{screen: s, scope: scope, cancel: cancel}
	s.refresh = request
	var source app.OnChainSource
	if scope.client != nil {
		source = scope.client
	}
	return tea.Batch(func() tea.Msg {
		return channelCoinsMsg{refresh: request, snapshot: app.OnChainSnapshot{
			Utxos: reader.ReadUnspent(ctx, source, 0, math.MaxInt32),
		}}
	}, func() tea.Msg {
		return channelCoinsMsg{refresh: request, snapshot: app.OnChainSnapshot{
			OnChainTxs: reader.ReadTransactions(ctx, source),
		}}
	})
}

func (s *ChannelOpenScreen) requireCurrentCoinScope() bool {
	if s.coinScope != s.ctx.walletObservationScope() {
		s.cancelCoinRefresh()
		s.error = "Wallet changed. Reopen this form and review again"
		return false
	}
	return true
}

func (s *ChannelOpenScreen) cancelCoinRefresh() {
	if s.refresh != nil {
		s.refresh.cancel()
		s.refresh = nil
	}
}

func cancelChannelCoinRead(screen Screen) {
	if s, ok := screen.(*ChannelOpenScreen); ok {
		s.cancelCoinRefresh()
	}
}

func (s *ChannelOpenScreen) handleCoinRefresh(msg channelCoinsMsg) (Screen, tea.Cmd) {
	if msg.refresh == nil || msg.refresh != s.refresh {
		return s, nil
	}
	if s.step >= coStepOpening || msg.refresh.scope != s.ctx.walletObservationScope() {
		s.cancelCoinRefresh()
		return s, nil
	}
	coins := msg.snapshot.Utxos
	if !msg.refresh.coinsDone && (coins.Known() || coins.Err != nil) {
		msg.refresh.coinsDone = true
		if s.utxoErr != nil && s.error == s.utxoErr.Error() {
			s.error = ""
		}
		s.utxoErr = coins.Err
		if s.utxoErr != nil {
			s.error = s.utxoErr.Error()
		} else {
			s.utxos = coins.Value
			s.utxoFetched = true
			s.utxoCursor = max(0, min(s.utxoCursor, len(s.utxos)-1))
		}
	}
	txs := msg.snapshot.OnChainTxs
	if !msg.refresh.txsDone && (txs.Known() || txs.Err != nil) {
		msg.refresh.txsDone = true
		if txs.Err == nil {
			s.txs = txs.Value
		}
	}
	if msg.refresh.coinsDone && msg.refresh.txsDone {
		s.cancelCoinRefresh()
	}
	return s, nil
}

func (s *ChannelOpenScreen) selectionSummary() string {
	total, err := s.selection.Total(s.utxos)
	if err != nil {
		return fmt.Sprintf("%d selected; unavailable coins", s.selection.Len())
	}
	return fmt.Sprintf("%d selected (%s sats)", s.selection.Len(), formatSats(total))
}

// ── Transaction lookup helpers ────────────────────────
// Same pattern as on-chain home's utxoDate / utxoTxLabel
// but reading from the screen's own local tx list.

func (s *ChannelOpenScreen) utxoDate(
	txid string,
) string {
	for _, tx := range s.txs {
		if tx.Txid == txid {
			return formatDateShort(tx.Timestamp)
		}
	}
	return "—"
}

func (s *ChannelOpenScreen) utxoTxLabel(
	txid string,
) string {
	for _, tx := range s.txs {
		if tx.Txid == txid {
			return tx.Label
		}
	}
	return ""
}

// ── Coin control sub-step ─────────────────────────────
// Opened via enter on the [Coin control] button in the
// amounts zone. Same sub-step pattern as coStepCustomPeer.

const coUtxoVisibleRows = 10

func (s *ChannelOpenScreen) handleCoinControlKey(
	keyStr string,
) (Screen, tea.Cmd) {
	switch s.ccZone {
	case coCCZoneList:
		return s.handleCCListKey(keyStr)
	case coCCZoneButtons:
		return s.handleCCButtonKey(keyStr)
	}
	return s, nil
}

func (s *ChannelOpenScreen) handleCCListKey(
	keyStr string,
) (Screen, tea.Cmd) {
	if !s.utxoFetched || len(s.utxos) == 0 {
		switch keyStr {
		case "ctrl+c":
			return s, tea.Quit
		case "left":
			return s, emitFocusSidebar
		case "up", "shift+tab":
			if s.ctx.HasTabs {
				return s, emitFocusTabBar
			}
			return s, nil
		case "down", "tab":
			s.ccZone = coCCZoneButtons
			return s, nil
		case "backspace":
			return s.returnFromCoinControl(false)
		}
		return s, nil
	}

	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit
	case "left":
		return s, emitFocusSidebar
	case "up":
		if s.utxoCursor > 0 {
			s.utxoCursor--
		} else if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
		return s, nil
	case "down":
		if s.utxoCursor < len(s.utxos)-1 {
			s.utxoCursor++
		} else {
			s.ccZone = coCCZoneButtons
		}
		return s, nil
	case "tab":
		s.ccZone = coCCZoneButtons
		return s, nil
	case "shift+tab":
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
		return s, nil
	case "space", "enter":
		s.toggleUtxoSelection(s.utxoCursor)
		return s, nil
	case "backspace":
		return s.returnFromCoinControl(false)
	}
	return s, nil
}

func (s *ChannelOpenScreen) handleCCButtonKey(
	keyStr string,
) (Screen, tea.Cmd) {
	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit
	case "left":
		if s.ccBtnIdx > 0 {
			s.ccBtnIdx--
		} else {
			return s, emitFocusSidebar
		}
		return s, nil
	case "right":
		if s.ccBtnIdx < 1 {
			s.ccBtnIdx++
		}
		return s, nil
	case "up", "shift+tab":
		s.ccZone = coCCZoneList
		return s, nil
	case "enter":
		switch s.ccBtnIdx {
		case 0: // Go Back
			return s.returnFromCoinControl(false)
		case 1: // Confirm
			return s.returnFromCoinControl(true)
		}
		return s, nil
	case "backspace":
		return s.returnFromCoinControl(false)
	}
	return s, nil
}

// returnFromCoinControl exits the coin control sub-step.
// If confirm=true and UTXOs are selected, pre-fills amount
// with the UTXO total, sets FundMax, auto-confirms, and
// advances to the fee zone. Otherwise returns to amounts.
func (s *ChannelOpenScreen) returnFromCoinControl(
	confirm bool,
) (Screen, tea.Cmd) {
	s.step = coStepInput
	s.error = ""

	if confirm && s.selection.Len() > 0 {
		// Pre-fill amount, auto-confirm, advance
		total, err := s.selection.Total(s.utxos)
		if err != nil {
			s.error = err.Error()
			s.step = coStepCoinControl
			return s, nil
		}
		s.amountInput.SetSats(total)
		s.fundMax = true
		s.amountConfirmed = true
		s.amountIdx = 1
		s.amountInput.Blur()
		s.focusZone = coZoneFee
		s.feeInput.Focus()
		return s, nil
	}

	// Returning without confirmation requires another amount review.
	s.amountConfirmed = false
	if s.selection.Len() == 0 {
		s.fundMax = false
		s.amountInput.Clear()
	}
	s.focusZone = coZoneAmounts
	s.amountIdx = 0
	return s, nil
}

// ── Selection helpers ──────────────────────────────────

func (s *ChannelOpenScreen) toggleUtxoSelection(idx int) {
	if idx >= 0 && idx < len(s.utxos) {
		s.selection.Toggle(s.utxos[idx])
	}
}

// ── Coin control sub-step view ──────────────────────────

func (s *ChannelOpenScreen) viewCoinControl(
	w, h int,
) string {
	p := newPane(w)
	p.title(theme.Header, "Coin Control")

	if s.selection.Len() > 0 {
		p.field("Selected: ", s.selectionSummary())
	} else {
		p.dim(" Select UTXOs for the channel open.")
	}
	p.blank()

	focused := s.ctx.ContentFocused &&
		s.ccZone == coCCZoneList
	s.viewUtxoTable(p, w, focused)

	p.appendError(s.error)

	btnFocused := s.ctx.ContentFocused &&
		s.ccZone == coCCZoneButtons
	return p.renderWithBottomButtons(
		[]string{"Go Back", "Confirm"},
		s.ccBtnIdx, btnFocused, h)
}

// ── UTXO table rendering ───────────────────────────────
// Column layout matches on-chain home: Date, Label,
// Address, Value. Shows up to coUtxoVisibleRows with
// scroll indicators.

func (s *ChannelOpenScreen) viewUtxoTable(
	p *paneBuilder, w int, focused bool,
) {
	if !s.utxoFetched {
		p.dim(" Loading...")
		return
	}
	if len(s.utxos) == 0 {
		p.dim(" No UTXOs available.")
		return
	}

	// Column widths matching on-chain home
	dateW := 12
	labelW := 15
	addrW := 18
	valW := w - dateW - labelW - addrW - 5
	if valW < 12 {
		valW = 12
	}

	sepStyle := theme.TableDim
	hdrStyle := theme.TableHeader

	// Column headers
	p.line(" " +
		hdrStyle.Render(pad("Date", dateW)) +
		hdrStyle.Render(pad("Label", labelW)) +
		hdrStyle.Render(pad("Address", addrW)) +
		hdrStyle.Render(
			fmt.Sprintf("%*s", valW, "Value")))

	// Separator
	p.line(" " + sepStyle.Render(
		strings.Repeat("─", w-2)))

	// Visible window
	visRows := coUtxoVisibleRows
	if len(s.utxos) < visRows {
		visRows = len(s.utxos)
	}
	startIdx := s.utxoCursor - visRows/2
	if startIdx < 0 {
		startIdx = 0
	}
	if startIdx+visRows > len(s.utxos) {
		startIdx = len(s.utxos) - visRows
		if startIdx < 0 {
			startIdx = 0
		}
	}
	endIdx := startIdx + visRows

	hasAbove := startIdx > 0
	hasBelow := endIdx < len(s.utxos)

	checkStyle := lipgloss.NewStyle().
		Foreground(theme.ColorCheck)

	for i := startIdx; i < endIdx; i++ {
		u := s.utxos[i]
		isCursor := focused && s.utxoCursor == i
		isChecked := s.selection.Contains(u)

		// Date from tx lookup
		dateStr := s.utxoDate(u.Txid)
		if u.Confirmations == 0 {
			dateStr = "unconfirmed"
		}

		// Label from tx lookup
		uLabel := s.utxoTxLabel(u.Txid)
		maxLbl := labelW - 1
		if len(uLabel) > maxLbl {
			uLabel = uLabel[:maxLbl-2] + ".."
		}

		// Address: first7..last7
		addr := u.Address
		if len(addr) > 16 {
			addr = addr[:7] + ".." +
				addr[len(addr)-7:]
		}

		// Formatted cells
		dateCell := pad(dateStr, dateW)
		labelCell := pad(uLabel, labelW)
		addrCell := pad(addr, addrW)
		valCell := fmt.Sprintf("%*s",
			valW, formatSats(u.AmountSats))

		// Marker (1 char, matching on-chain home)
		marker := " "
		if isChecked {
			marker = "✓"
		}
		if isCursor && !isChecked {
			marker = theme.NavActive.Render("▸")
		}

		// Scroll indicator on edge rows
		scrollInd := ""
		if i == startIdx && hasAbove {
			scrollInd = " ▲"
		}
		if i == endIdx-1 && hasBelow {
			scrollInd = " ▼"
		}

		switch {
		case isCursor:
			p.line(marker +
				theme.NavActive.Render(dateCell) +
				theme.NavActive.Render(labelCell) +
				theme.NavActive.Render(addrCell) +
				theme.NavActive.Render(valCell) +
				theme.Dim.Render(scrollInd))
		case isChecked:
			p.line(marker +
				checkStyle.Render(dateCell) +
				checkStyle.Render(labelCell) +
				checkStyle.Render(addrCell) +
				checkStyle.Render(valCell) +
				theme.Dim.Render(scrollInd))
		default:
			p.line(marker +
				theme.Dim.Render(dateCell) +
				theme.Value.Render(labelCell) +
				theme.Dim.Render(addrCell) +
				theme.Value.Render(valCell) +
				theme.Dim.Render(scrollInd))
		}
	}
}

// ── Helpbar bindings ───────────────────────────────────

func (s *ChannelOpenScreen) coinControlBindings() []key.Binding {
	if s.ccZone == coCCZoneButtons {
		binds := buttonNav(s.ccBtnIdx)
		binds = append(binds,
			kEnter,
			bind("⇧tab", "UTXOs", "shift+tab"),
			kBack, kQuit)
		return binds
	}
	if !s.utxoFetched || len(s.utxos) == 0 {
		return []key.Binding{
			kTabNext, kBack, kQuit,
		}
	}
	return []key.Binding{
		bind("↑↓", "navigate", "up", "down"),
		bind("enter/space", "select", "enter", "space"),
		kTabNext, kBack, kQuit,
	}
}
