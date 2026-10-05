package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/theme"
)

// ── OnChainHomeScreen ─────────────────────────────────
// Section home for On-Chain. Three focus zones: buttons
// (Receive, Send/Send Selected), UTXO table with pencil
// edit and coin control, and transaction table. Reads
// live status through ctx.Status pointer. UTXOs and
// transactions arrive via HandleMsg. Coin control
// selection is shared with OnChainSendScreen through
// the *OnChainContext pointer.

const (
	ocHomeZoneButtons = 0
	ocHomeZoneUtxos   = 1
	ocHomeZoneTxs     = 2
)

type OnChainHomeScreen struct {
	ctx   *ScreenContext
	ocCtx *OnChainContext

	btnIdx    int // 0=Send, 1=Receive
	focusZone int // 0=buttons, 1=utxo table, 2=tx table

	utxoCursor    int
	txCursor      int
	pencilFocused bool

	// Coin control dirty flag — set on any spacebar
	// toggle, cleared when openSend reads it.
	utxoChanged bool

	// Label popup state
	labelEditing bool
	labelInput   textinput.Model
	labelOnBtn   bool // true when on Save/Cancel buttons
	labelBtnIdx  int  // 0=Cancel, 1=Save
	labelTxid    string
	labelAttempt uint64
	labelPending bool
	labelErr     string
	labels       app.TransactionLabelClient
}

func NewOnChainHomeScreen(
	ctx *ScreenContext,
	ocCtx *OnChainContext,
) *OnChainHomeScreen {
	return &OnChainHomeScreen{
		ctx:   ctx,
		ocCtx: ocCtx,
	}
}

// ── Screen interface ────────────────────────────────────

func (s *OnChainHomeScreen) Init() tea.Cmd {
	return nil
}

func (s *OnChainHomeScreen) HandleKey(
	keyStr string, msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
	// Label popup intercepts ALL keys
	if s.labelEditing {
		return s.handleLabelPopupKey(keyStr, msg)
	}

	s.clampCursors()

	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit
	case "left":
		return s.handleLeft()
	case "right":
		return s.handleRight()
	case "up":
		return s.handleUp()
	case "down":
		return s.handleDown()
	case "tab":
		return s.handleTab()
	case "shift+tab":
		return s.handleShiftTab()
	case "space":
		if s.focusZone == ocHomeZoneUtxos &&
			s.utxoCursor < len(s.ocCtx.Utxos.Value) {
			s.toggleSelection(s.utxoCursor)
			s.utxoChanged = true
		}
		return s, nil
	case "backspace":
		return s, emitFocusSidebar
	case "enter":
		if s.ctx.walletClientUnavailable() {
			return s, fetchWalletStateCmd(s.ctx)
		}
		// No wallet → trigger wallet creation flow
		if !s.ctx.walletKnown() && !s.ctx.walletDisplayAvailable() {
			return s, fetchWalletStateCmd(s.ctx)
		}
		if s.ctx.walletKnown() && !s.ctx.walletExists() {
			screen := NewWalletCreateScreen(s.ctx)
			return s, func() tea.Msg {
				return openTabMsg{
					Kind:   tabWalletCreate,
					Label:  "Create Wallet",
					Screen: screen,
				}
			}
		}
		return s.handleEnter()
	}
	return s, nil
}

func (s *OnChainHomeScreen) handleLeft() (
	Screen, tea.Cmd,
) {
	switch s.focusZone {
	case ocHomeZoneUtxos:
		if s.pencilFocused {
			s.pencilFocused = false
			return s, nil
		}
	case ocHomeZoneButtons:
		if s.btnIdx > 0 {
			s.btnIdx--
			return s, nil
		}
	}
	return s, emitFocusSidebar
}

func (s *OnChainHomeScreen) handleRight() (
	Screen, tea.Cmd,
) {
	switch s.focusZone {
	case ocHomeZoneButtons:
		if s.ctx.Cfg.HasLND() &&
			s.ctx.walletDisplayAvailable() &&
			s.btnIdx < 1 {
			s.btnIdx++
		}
	case ocHomeZoneUtxos:
		if s.utxoCursor < len(s.ocCtx.Utxos.Value) &&
			!s.pencilFocused {
			s.pencilFocused = true
		}
	}
	return s, nil
}

func (s *OnChainHomeScreen) handleUp() (
	Screen, tea.Cmd,
) {
	switch s.focusZone {
	case ocHomeZoneTxs:
		if s.txCursor > 0 {
			s.txCursor--
		} else {
			s.focusZone = ocHomeZoneUtxos
			if len(s.ocCtx.Utxos.Value) > 0 {
				s.utxoCursor =
					len(s.ocCtx.Utxos.Value) - 1
			}
		}
	case ocHomeZoneUtxos:
		if s.utxoCursor > 0 {
			s.utxoCursor--
			s.pencilFocused = false
		} else {
			s.focusZone = ocHomeZoneButtons
			s.btnIdx = 0
			s.pencilFocused = false
		}
	case ocHomeZoneButtons:
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
	}
	return s, nil
}

func (s *OnChainHomeScreen) handleDown() (
	Screen, tea.Cmd,
) {
	switch s.focusZone {
	case ocHomeZoneButtons:
		if len(s.ocCtx.Utxos.Value) > 0 {
			s.focusZone = ocHomeZoneUtxos
			s.utxoCursor = 0
			s.pencilFocused = false
		} else if len(s.ocCtx.OnChainTxs.Value) > 0 {
			s.focusZone = ocHomeZoneTxs
			s.txCursor = 0
		}
	case ocHomeZoneUtxos:
		if s.utxoCursor < len(s.ocCtx.Utxos.Value)-1 {
			s.utxoCursor++
			s.pencilFocused = false
		} else if len(s.ocCtx.OnChainTxs.Value) > 0 {
			s.focusZone = ocHomeZoneTxs
			s.txCursor = 0
			s.pencilFocused = false
		}
	case ocHomeZoneTxs:
		if s.txCursor < len(s.ocCtx.OnChainTxs.Value)-1 {
			s.txCursor++
		}
	}
	return s, nil
}

func (s *OnChainHomeScreen) handleTab() (
	Screen, tea.Cmd,
) {
	switch s.focusZone {
	case ocHomeZoneButtons:
		if len(s.ocCtx.Utxos.Value) > 0 {
			s.focusZone = ocHomeZoneUtxos
			s.utxoCursor = 0
			s.pencilFocused = false
		} else if len(s.ocCtx.OnChainTxs.Value) > 0 {
			s.focusZone = ocHomeZoneTxs
			s.txCursor = 0
		}
	case ocHomeZoneUtxos:
		if len(s.ocCtx.OnChainTxs.Value) > 0 {
			s.focusZone = ocHomeZoneTxs
			s.txCursor = 0
			s.pencilFocused = false
		}
	case ocHomeZoneTxs:
		// At bottom zone — nowhere to go
	}
	return s, nil
}

func (s *OnChainHomeScreen) handleShiftTab() (
	Screen, tea.Cmd,
) {
	switch s.focusZone {
	case ocHomeZoneTxs:
		s.focusZone = ocHomeZoneUtxos
		if len(s.ocCtx.Utxos.Value) > 0 {
			s.utxoCursor = 0
		} else {
			s.focusZone = ocHomeZoneButtons
			s.btnIdx = 0
		}
		return s, nil
	case ocHomeZoneUtxos:
		s.focusZone = ocHomeZoneButtons
		s.btnIdx = 0
		s.pencilFocused = false
		return s, nil
	case ocHomeZoneButtons:
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
	}
	return s, nil
}

func (s *OnChainHomeScreen) handleEnter() (
	Screen, tea.Cmd,
) {
	switch s.focusZone {
	case ocHomeZoneButtons:
		switch s.btnIdx {
		case 0: // Send
			return s.openSend()
		case 1: // Receive
			return s.openReceive()
		}
	case ocHomeZoneUtxos:
		if s.utxoCursor < len(s.ocCtx.Utxos.Value) {
			if s.pencilFocused {
				s.openLabelPopup()
				return s, nil
			}
			return s.openUtxoDetail()
		}
	case ocHomeZoneTxs:
		if s.txCursor < len(s.ocCtx.OnChainTxs.Value) {
			return s.openTxDetail()
		}
	}
	return s, nil
}

func (s *OnChainHomeScreen) openReceive() (
	Screen, tea.Cmd,
) {
	if !s.ctx.walletExists() {
		return s, nil
	}
	screen := NewOCReceiveScreen(s.ctx)
	return s, func() tea.Msg {
		return openTabMsg{
			Kind:   tabOCReceive,
			Label:  "⛓ Receive",
			Screen: screen,
		}
	}
}

func (s *OnChainHomeScreen) openSend() (
	Screen, tea.Cmd,
) {
	if !s.ctx.walletExists() {
		return s, nil
	}
	screen := NewOnChainSendScreen(
		s.ctx, s.ocCtx)

	// Selected coins default to Max; LND determines the net amount.
	if s.ocCtx.Selection.Len() > 0 {
		screen.applyMax()
	}
	replace := s.utxoChanged
	s.utxoChanged = false
	return s, func() tea.Msg {
		return openTabMsg{
			Kind:    tabOnChain,
			Label:   "⛓ Send",
			Screen:  screen,
			Replace: replace,
		}
	}
}

func (s *OnChainHomeScreen) openUtxoDetail() (
	Screen, tea.Cmd,
) {
	u := s.ocCtx.Utxos.Value[s.utxoCursor]
	key := fmt.Sprintf("%s:%d", u.Txid, u.Vout)
	screen := NewUtxoDetailScreen(s.ctx, key)
	return s, func() tea.Msg {
		return openTabMsg{
			Kind:        tabUtxoDetail,
			Key:         key,
			Screen:      screen,
			FocusTabBar: true,
		}
	}
}

func (s *OnChainHomeScreen) openTxDetail() (
	Screen, tea.Cmd,
) {
	tx := s.ocCtx.OnChainTxs.Value[s.txCursor]
	key := tx.Txid
	screen := NewOnChainTxScreen(s.ctx, key)
	return s, func() tea.Msg {
		return openTabMsg{
			Kind:        tabOnChainTx,
			Key:         key,
			Screen:      screen,
			FocusTabBar: true,
		}
	}
}

// ── Label popup keys ────────────────────────────────────

func (s *OnChainHomeScreen) handleLabelPopupKey(
	keyStr string, msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
	if s.labelPending {
		switch keyStr {
		case "ctrl+c":
			return s, tea.Quit
		case "left":
			return s, emitFocusSidebar
		}
		return s, nil
	}
	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit
	case "up":
		if s.labelOnBtn {
			s.labelOnBtn = false
			s.labelInput.Focus()
		}
		return s, nil
	case "down", "tab":
		if !s.labelOnBtn {
			s.labelOnBtn = true
			// Land on Save (index 1) — the action button.
			s.labelBtnIdx = 1
			s.labelInput.Blur()
		}
		return s, nil
	case "left":
		if s.labelOnBtn {
			if s.labelBtnIdx > 0 {
				s.labelBtnIdx--
			}
			return s, nil
		}
		var cmd tea.Cmd
		s.labelInput, cmd =
			s.labelInput.Update(tea.Msg(msg))
		return s, cmd
	case "right":
		if s.labelOnBtn {
			if s.labelBtnIdx < 1 {
				s.labelBtnIdx++
			}
			return s, nil
		}
		var cmd tea.Cmd
		s.labelInput, cmd =
			s.labelInput.Update(tea.Msg(msg))
		return s, cmd
	case "enter":
		if s.labelOnBtn {
			switch s.labelBtnIdx {
			case 0: // Cancel
				s.closeLabelPopup()
				return s, nil
			case 1: // Save
				if !s.ctx.walletExists() {
					return s, nil
				}
				prepared, err := app.PrepareTransactionLabel(s.labelTxid, s.labelInput.Value())
				if err != nil {
					s.labelErr = err.Error()
					return s, nil
				}
				s.labelAttempt++
				s.labelPending = true
				s.labelErr = ""
				client := s.labels
				if client == nil && s.ctx.LndClient != nil {
					client = s.ctx.LndClient
				}
				return s, labelTxCmd(s, client, prepared)
			}
		}
		// On label field — move to buttons, land on Save.
		s.labelOnBtn = true
		s.labelBtnIdx = 1
		s.labelInput.Blur()
		return s, nil
	case "backspace":
		if !s.labelOnBtn {
			var cmd tea.Cmd
			s.labelInput, cmd =
				s.labelInput.Update(tea.Msg(msg))
			return s, cmd
		}
		return s, nil
	default:
		if !s.labelOnBtn {
			var cmd tea.Cmd
			s.labelInput, cmd =
				s.labelInput.Update(tea.Msg(msg))
			return s, cmd
		}
	}
	return s, nil
}

// ── HandleMsg ───────────────────────────────────────────

func (s *OnChainHomeScreen) HandleMsg(
	msg tea.Msg,
) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case labelTxMsg:
		if msg.owner != s || msg.attempt != s.labelAttempt || !s.labelPending {
			return s, nil
		}
		s.labelPending = false
		switch msg.result.Outcome {
		case app.TransactionLabelSaved:
			// Publish only the acknowledged label. Invalidate older history reads
			// before requesting a fresh snapshot, so they cannot undo this result.
			for i := range s.ocCtx.OnChainTxs.Value {
				if s.ocCtx.OnChainTxs.Value[i].Txid == s.labelTxid {
					s.ocCtx.OnChainTxs.Value[i].Label = s.labelInput.Value()
				}
			}
			s.closeLabelPopup()
			s.ocCtx.revision++
			return s, requestOnChainCmd
		case app.TransactionLabelNotSaved:
			s.labelErr = "Label was not saved."
			if msg.result.Err != nil {
				s.labelErr += " " + msg.result.Err.Error()
			}
		default:
			s.labelErr = "Save was not confirmed. Check the transaction label before retrying."
		}
		return s, nil
	case tea.PasteMsg:
		if s.labelEditing && !s.labelPending && !s.labelOnBtn {
			var cmd tea.Cmd
			s.labelInput, cmd =
				s.labelInput.Update(msg)
			return s, cmd
		}
	}
	return s, nil
}

// ── View ────────────────────────────────────────────────

func (s *OnChainHomeScreen) View(
	w, h int,
) string {
	// An active edit survives wallet/status failures and removal of its UTXO.
	if s.labelEditing {
		return strings.Join(s.renderLabelPopup(w), "\n")
	}
	s.clampCursors()
	cfg := s.ctx.Cfg
	status := s.ctx.Status

	// ── Fixed header ─────────────────────────────

	if s.ctx.walletClientUnavailable() {
		return s.ctx.walletConnectionNotice(w)
	}
	if !s.ctx.walletKnown() && !s.ctx.walletDisplayAvailable() {
		return renderWalletStateUnavailable(w, h)
	}
	if !cfg.HasLND() || !s.ctx.walletDisplayAvailable() {
		return renderWalletPrompt(
			w, h, s.ctx.ContentFocused)
	}

	if status == nil {
		return renderWaitingForLND(w, h)
	}

	var headerLines []string
	headerLines = append(headerLines, "")

	headerLines = append(headerLines,
		centerPad(
			theme.Header.Render("On-Chain Wallet"),
			w))
	headerLines = append(headerLines, "")

	isFocused := s.ctx.ContentFocused
	utxos := s.ocCtx.Utxos.Value
	txs := s.ocCtx.OnChainTxs.Value

	headerLines = append(headerLines,
		" "+theme.Label.Render("Balance:  ")+
			theme.Value.Render(
				onChainBalanceText(status))+
			theme.Dim.Render(
				s.utxoCountText()))

	if status.Bitcoin.Fresh() && !status.Bitcoin.Value.Synced {
		headerLines = append(headerLines, "")
		headerLines = append(headerLines,
			" "+theme.Warn.Render(
				"Bitcoin Core is syncing. Funds you have"))
		headerLines = append(headerLines,
			" "+theme.Warn.Render(
				"received will not appear until sync reaches"))
		headerLines = append(headerLines,
			" "+theme.Warn.Render(
				"that block. Funds cannot be spent until"))
		headerLines = append(headerLines,
			" "+theme.Warn.Render(
				"sync is complete."))
	}

	headerLines = append(headerLines, statusNotice(status))

	sendLabel := "Send"
	if s.ocCtx.Selection.Len() > 0 {
		total, err := s.ocCtx.Selection.Total(s.ocCtx.Utxos.Value)
		if err != nil {
			sendLabel = "Send Selected (unavailable)"
		} else {
			sendLabel = fmt.Sprintf("Send Selected (%s)", formatSats(total))
		}
	}
	headerLines = append(headerLines,
		renderButtons(
			[]string{sendLabel, "Receive"},
			s.btnIdx,
			isFocused &&
				s.focusZone == ocHomeZoneButtons,
			w, s.ctx.disabledWalletButtons(0, 1)...))
	headerLines = append(headerLines, "")

	header := strings.Join(headerLines, "\n")
	headerH := len(headerLines)

	// ── Shared styles ────────────────────────────
	hdrStyle := theme.TableHeader
	sepStyle := theme.TableDim

	// ── UTXO table header ───────────────────────
	uDateW := 12
	uLabelW := 15
	uAddrW := 18
	uValW := w - uDateW - uLabelW - uAddrW - 5
	uValW = max(uValW, 12)

	var utxoHeaderLines []string
	utxoHeaderLines = append(utxoHeaderLines,
		centerPad(
			theme.Header.Render(onChainListTitle("UTXOs", s.ocCtx.Utxos)), w))

	utxoHdr := " " +
		hdrStyle.Render(pad("Date", uDateW)) +
		hdrStyle.Render(pad("Label", uLabelW)) +
		hdrStyle.Render(pad("Address", uAddrW)) +
		hdrStyle.Render(
			fmt.Sprintf("%*s", uValW, "Value"))
	utxoHeaderLines = append(utxoHeaderLines, utxoHdr)
	utxoHeaderLines = append(utxoHeaderLines,
		" "+sepStyle.Render(
			strings.Repeat("─", w-2)))

	utxoHeader := strings.Join(utxoHeaderLines, "\n")
	utxoHeaderH := len(utxoHeaderLines)

	// ── Scrollable UTXO rows ────────────────────
	var utxoMidLines []string

	if len(utxos) == 0 {
		utxoMidLines = append(utxoMidLines,
			" "+theme.Dim.Render(observedEmptyText(s.ocCtx.Utxos, "No UTXOs found.")))
	} else {
		for i, u := range utxos {
			isSelected := isFocused &&
				s.focusZone == ocHomeZoneUtxos &&
				s.utxoCursor == i
			isChecked := s.ocCtx.Selection.Contains(u)

			// Date from tx lookup
			dateStr := s.utxoDate(u.Txid)
			if u.Confirmations == 0 {
				dateStr = "unconfirmed"
			}
			dateStr = pad(dateStr, uDateW)

			// Label from tx lookup
			uLabel := s.utxoTxLabel(u.Txid)
			maxLbl := uLabelW - 2
			if isSelected && !s.labelEditing {
				maxLbl = uLabelW - 4
			}
			if len(uLabel) > maxLbl {
				uLabel = uLabel[:maxLbl-2] + ".."
			}

			// Build label cell with pencil
			var labelCell string
			if isSelected && !s.labelEditing {
				pencilStyle := theme.Dim
				labelStyle := lipgloss.NewStyle()
				if s.pencilFocused {
					pencilStyle = theme.NavActive
					labelStyle = theme.NavActive
				}
				labelCell = labelStyle.Render(
					pad(uLabel, maxLbl)) +
					" " +
					pencilStyle.Render("✎")
				cellW := maxLbl + 2
				if cellW < uLabelW {
					labelCell += strings.Repeat(
						" ", uLabelW-cellW)
				}
			} else {
				labelCell = pad(uLabel, uLabelW)
			}

			// Address: first7..last7
			uAddr := u.Address
			if len(uAddr) > 16 {
				uAddr = uAddr[:7] + ".." +
					uAddr[len(uAddr)-7:]
			}
			uAddrStr := pad(uAddr, uAddrW)

			// Value
			uValStr := fmt.Sprintf("%*s",
				uValW, formatSats(u.AmountSats))

			marker := " "
			if isChecked {
				marker = "✓"
			}
			if isSelected && !isChecked {
				marker = theme.NavActive.Render("▸")
			}

			selStyle := theme.NavActive

			switch {
			case isSelected && s.pencilFocused &&
				!s.labelEditing:
				utxoMidLines = append(utxoMidLines,
					marker+
						theme.Dim.Render(dateStr)+
						labelCell+
						theme.Dim.Render(uAddrStr)+
						theme.Value.Render(uValStr))
			case isSelected:
				utxoMidLines = append(utxoMidLines,
					marker+
						selStyle.Render(dateStr)+
						labelCell+
						selStyle.Render(uAddrStr)+
						selStyle.Render(uValStr))
			case isChecked:
				checkStyle := lipgloss.NewStyle().
					Foreground(
						theme.ColorCheck)
				utxoMidLines = append(utxoMidLines,
					marker+
						checkStyle.Render(dateStr)+
						checkStyle.Render(labelCell)+
						checkStyle.Render(uAddrStr)+
						checkStyle.Render(uValStr))
			default:
				utxoMidLines = append(utxoMidLines,
					marker+
						theme.Dim.Render(dateStr)+
						theme.Value.Render(labelCell)+
						theme.Dim.Render(uAddrStr)+
						theme.Value.Render(uValStr))
			}
		}
	}

	utxoMidContent := strings.Join(utxoMidLines, "\n")

	// ── Transaction table header ────────────────
	tDateW := 12
	tLabelW := 14
	tConfW := 3
	tValW := 14
	tBalW := w - tDateW - tLabelW - tConfW - tValW - 5
	tBalW = max(tBalW, 10)

	confStyle := lipgloss.NewStyle().
		Foreground(theme.ColorLabel)

	var txHeaderLines []string
	txHeaderLines = append(txHeaderLines,
		centerPad(
			theme.Header.Render(onChainListTitle("Transactions", s.ocCtx.OnChainTxs)), w))

	txHdr := " " +
		hdrStyle.Render(pad("Date", tDateW)) +
		hdrStyle.Render(pad("Label", tLabelW)) +
		hdrStyle.Render(pad("", tConfW)) +
		hdrStyle.Render(
			fmt.Sprintf("%*s", tValW, "Value")) +
		hdrStyle.Render(
			fmt.Sprintf("%*s", tBalW, "Balance"))
	txHeaderLines = append(txHeaderLines, txHdr)
	txHeaderLines = append(txHeaderLines,
		" "+sepStyle.Render(
			strings.Repeat("─", w-2)))

	txHeader := strings.Join(txHeaderLines, "\n")
	txHeaderH := len(txHeaderLines)

	// ── Scrollable tx rows ──────────────────────
	var txMidLines []string

	txBalances := s.computeTxBalances()

	if len(txs) == 0 {
		txMidLines = append(txMidLines,
			" "+theme.Dim.Render(
				observedEmptyText(s.ocCtx.OnChainTxs, "No on-chain transactions.")))
	} else {
		for i, tx := range txs {
			isSelected := isFocused &&
				s.focusZone == ocHomeZoneTxs &&
				s.txCursor == i

			date := "unconfirmed"
			if tx.IsAnchorSweep {
				date = formatDateShort(tx.Timestamp)
			} else if tx.Confirmations > 0 {
				date = formatDateShort(tx.Timestamp)
			}
			dateStr := pad(date, tDateW)

			txLabel := tx.Label
			if len(txLabel) > tLabelW-1 {
				txLabel = txLabel[:tLabelW-2] + ".."
			}
			labelStr := pad(txLabel, tLabelW)

			confIcon := confIndicator(
				tx.Confirmations)
			if tx.IsAnchorSweep {
				confIcon = "–"
			}
			confCell := confStyle.Render(
				" " + pad(confIcon, tConfW-1))

			valNum := formatSats(tx.Amount)
			if tx.Amount >= 0 {
				valNum = "+" + valNum
			}
			valStr := fmt.Sprintf("%*s",
				tValW, valNum)

			balanceText := "N/A"
			if i < len(txBalances) {
				balanceText = formatSats(txBalances[i])
			}
			balStr := fmt.Sprintf("%*s", tBalW, balanceText)

			marker := " "
			if isSelected {
				marker = theme.NavActive.Render("▸")
				selStyle := theme.NavActive
				txMidLines = append(txMidLines,
					marker+
						selStyle.Render(dateStr)+
						selStyle.Render(labelStr)+
						confCell+
						selStyle.Render(valStr)+
						selStyle.Render(balStr))
			} else {
				amtStyle := lipgloss.NewStyle().
					Foreground(
						theme.ColorPrimary)
				if tx.Amount < 0 {
					amtStyle = lipgloss.NewStyle().
						Foreground(
							theme.ColorDanger)
				}
				txMidLines = append(txMidLines,
					marker+
						theme.Dim.Render(dateStr)+
						theme.Value.Render(labelStr)+
						confCell+
						amtStyle.Render(valStr)+
						theme.Dim.Render(balStr))
			}
		}
	}

	txMidContent := strings.Join(txMidLines, "\n")

	// ── Size viewports ──────────────────────────
	fixedH := headerH + txHeaderH + utxoHeaderH
	remainH := max(h-fixedH, 2)

	txVPH := remainH / 2
	utxoVPH := remainH - txVPH
	txVPH = max(txVPH, 1)
	utxoVPH = max(utxoVPH, 1)

	utxoVPRendered := renderViewport(
		utxoMidContent, w, utxoVPH,
		s.utxoCursor,
		len(utxoMidLines),
		len(utxos) > 0 &&
			s.focusZone == ocHomeZoneUtxos)

	txVPRendered := renderViewport(
		txMidContent, w, txVPH,
		s.txCursor,
		len(txMidLines),
		len(txs) > 0 &&
			s.focusZone == ocHomeZoneTxs)

	return header + "\n" +
		utxoHeader + "\n" +
		utxoVPRendered + "\n" +
		txHeader + "\n" +
		txVPRendered
}

// ── Label popup ─────────────────────────────────────────

func (s *OnChainHomeScreen) openLabelPopup() {
	if s.utxoCursor < 0 || s.utxoCursor >= len(s.ocCtx.Utxos.Value) {
		return
	}
	s.labelTxid = s.ocCtx.Utxos.Value[s.utxoCursor].Txid
	s.labelErr = ""
	txLabel := s.utxoTxLabel(s.labelTxid)
	contentW := tuiWidth - 2 - 12 - 1 // nav width=12
	fieldW := contentW - 16
	if fieldW < 20 {
		fieldW = 20
	}
	s.labelInput = newDetailField(txLabel, fieldW)
	s.labelInput.Placeholder = "enter label"
	// app validates LND's byte limit; a rune limit can truncate existing labels.
	s.labelInput.Focus()
	s.labelEditing = true
	s.labelOnBtn = false
	// Default to Save (index 1) so the first down/tab
	// or enter off the label field lands on the action
	// button, not Cancel.
	s.labelBtnIdx = 1
	s.pencilFocused = false
}

func (s *OnChainHomeScreen) closeLabelPopup() {
	s.labelEditing = false
	s.labelOnBtn = false
	s.labelBtnIdx = 1
	s.labelInput.Blur()
}

func (s *OnChainHomeScreen) renderLabelPopup(
	w int,
) []string {
	isFocused := s.ctx.ContentFocused
	boxW := w - 4
	if boxW < 30 {
		boxW = 30
	}

	border := lipgloss.NewStyle().
		Foreground(theme.ColorGrayed)

	var lines []string
	lines = append(lines,
		"  "+border.Render(
			"┌"+strings.Repeat("─", boxW)+"┐"))
	for _, line := range strings.Split(ansi.Wrap("Transaction: "+s.labelTxid, boxW-2, ""), "\n") {
		lines = append(lines, "  "+border.Render("│")+pad(line, boxW)+border.Render("│"))
	}

	lblActive := isFocused && !s.labelOnBtn
	lblStyle := theme.Header
	marker := " "
	if lblActive {
		marker = theme.NavActive.Render("▸")
	}
	inputView := s.labelInput.View()
	inputW := lipgloss.Width(inputView)
	lblPrefix := marker + lblStyle.Render("Label: ")
	prefixW := lipgloss.Width(lblPrefix)
	padR := boxW - prefixW - inputW
	if padR < 0 {
		padR = 0
	}
	lines = append(lines,
		"  "+border.Render("│")+
			lblPrefix+inputView+
			strings.Repeat(" ", padR)+
			border.Render("│"))

	btnStr := renderButtons(
		[]string{"Cancel", "Save"},
		s.labelBtnIdx,
		isFocused && s.labelOnBtn, boxW, s.ctx.disabledWalletButtons(1)...)
	if s.labelPending {
		btnStr = theme.Dim.Render("Saving label...")
	}
	btnW := lipgloss.Width(btnStr)
	btnPad := boxW - btnW
	if btnPad < 0 {
		btnPad = 0
	}
	lines = append(lines,
		"  "+border.Render("│")+
			btnStr+
			strings.Repeat(" ", btnPad)+
			border.Render("│"))
	if s.labelErr != "" {
		for _, line := range strings.Split(ansi.Wrap(s.labelErr, boxW-2, ""), "\n") {
			lines = append(lines, "  "+border.Render("│")+pad(theme.Warning.Render(line), boxW)+border.Render("│"))
		}
	}

	lines = append(lines,
		"  "+border.Render(
			"└"+strings.Repeat("─", boxW)+"┘"))

	return lines
}

// ── HelpBindings ────────────────────────────────────────

func (s *OnChainHomeScreen) HelpBindings() []key.Binding {
	if s.labelEditing {
		return s.labelPopupBindings()
	}
	if s.ctx.walletClientUnavailable() || !s.ctx.walletDisplayAvailable() {
		return walletUnavailableHelpBindings(s.ctx)
	}
	switch s.focusZone {
	case ocHomeZoneUtxos:
		return s.utxoBindings()
	case ocHomeZoneTxs:
		return homeListBindings(
			"transactions", "details", "UTXOs")
	}
	return homeButtonBindings(
		"UTXOs", s.btnIdx, s.ctx.HasTabs)
}

func (s *OnChainHomeScreen) utxoBindings() []key.Binding {
	var binds []key.Binding
	if s.pencilFocused {
		binds = append(binds,
			bind("←", "UTXO", "left"),
			bind("enter", "edit label", "enter"))
	} else {
		binds = append(binds,
			bind("↑↓", "UTXOs", "up", "down"),
			bind("space", "select", "space"),
			kEnterDetails)
	}
	binds = append(binds,
		bind("tab", "transactions", "tab"),
		kShiftTabButtons,
		kSidebar,
		kBack, kQuit)
	return binds
}

func (s *OnChainHomeScreen) labelPopupBindings() []key.Binding {
	if s.labelPending {
		return []key.Binding{kSidebar, kQuit}
	}
	if s.labelOnBtn {
		return []key.Binding{
			kLeftRightButtons,
			bind("↑", "label", "up"),
			kEnter,
			kQuit,
		}
	}
	return []key.Binding{
		bind("↓", "buttons", "down"),
		bind("enter", "buttons", "enter"),
		kQuit,
	}
}

// ── Coin control ────────────────────────────────────────

func (s *OnChainHomeScreen) toggleSelection(idx int) {
	if idx < 0 || idx >= len(s.ocCtx.Utxos.Value) {
		return
	}
	s.ocCtx.Selection.Toggle(s.ocCtx.Utxos.Value[idx])
}

// ── Helpers ─────────────────────────────────────────────

func (s *OnChainHomeScreen) utxoDate(
	txid string,
) string {
	for _, tx := range s.ocCtx.OnChainTxs.Value {
		if tx.Txid == txid {
			return formatDateShort(tx.Timestamp)
		}
	}
	return "—"
}

func (s *OnChainHomeScreen) utxoTxLabel(
	txid string,
) string {
	for _, tx := range s.ocCtx.OnChainTxs.Value {
		if tx.Txid == txid {
			return tx.Label
		}
	}
	return ""
}

func (s *OnChainHomeScreen) computeTxBalances() []int64 {
	txs := s.ocCtx.OnChainTxs.Value
	if len(txs) == 0 {
		return nil
	}
	if !s.ocCtx.OnChainTxs.Fresh() || s.ctx.Status == nil || !s.ctx.Status.Balance.Fresh() {
		return nil
	}
	bal := parseBalance(s.ctx.Status.Balance.Value.TotalBalance)
	balances := make([]int64, len(txs))
	for i := range txs {
		balances[i] = bal
		bal -= txs[i].Amount
	}
	return balances
}

func (s *OnChainHomeScreen) clampCursors() {
	utxos := s.ocCtx.Utxos.Value
	if len(utxos) == 0 {
		s.utxoCursor = 0
	} else if s.utxoCursor >= len(utxos) {
		s.utxoCursor = len(utxos) - 1
	}
	if s.utxoCursor < 0 {
		s.utxoCursor = 0
	}

	txs := s.ocCtx.OnChainTxs.Value
	if len(txs) == 0 {
		s.txCursor = 0
	} else if s.txCursor >= len(txs) {
		s.txCursor = len(txs) - 1
	}
	if s.txCursor < 0 {
		s.txCursor = 0
	}
}
