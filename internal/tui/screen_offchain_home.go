package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/lndrpc"
	"github.com/virtpriv/node/internal/theme"
)

// WalletHomeScreen owns navigation over shared invoice/payment observations.

const (
	walletHomeZoneButtons = 0
	walletHomeZoneList    = 1
)

type WalletHomeScreen struct {
	ctx       *ScreenContext
	btnIdx    int // 0=Send, 1=Receive, 2=Pairing
	focusZone int // 0=buttons, 1=payment list
	cursor    int // position in payment list
}

func NewWalletHomeScreen(
	ctx *ScreenContext,
) *WalletHomeScreen {
	return &WalletHomeScreen{
		ctx: ctx,
	}
}

// ── Screen interface ────────────────────────────────────

func (s *WalletHomeScreen) Init() tea.Cmd {
	return nil
}

func (s *WalletHomeScreen) HandleKey(
	keyStr string, msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
	s.clampCursor()
	entries := s.ctx.paymentHistory().Entries()

	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit
	case "left":
		if s.focusZone == walletHomeZoneButtons &&
			s.btnIdx > 0 {
			s.btnIdx--
			return s, nil
		}
		return s, emitFocusSidebar
	case "right":
		if s.focusZone == walletHomeZoneButtons &&
			s.ctx.Cfg.HasLND() &&
			s.ctx.walletDisplayAvailable() &&
			s.btnIdx < 2 {
			s.btnIdx++
		}
		return s, nil
	case "up":
		if s.focusZone == walletHomeZoneList {
			if s.cursor > 0 {
				s.cursor--
			} else {
				s.focusZone = walletHomeZoneButtons
				s.btnIdx = 0
			}
			return s, nil
		}
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
		return s, nil
	case "down", "tab":
		if s.focusZone == walletHomeZoneButtons {
			if len(entries) > 0 {
				s.focusZone = walletHomeZoneList
				s.cursor = 0
			}
			return s, nil
		}
		if s.focusZone == walletHomeZoneList {
			if s.cursor < len(entries)-1 {
				s.cursor++
			}
		}
		return s, nil
	case "shift+tab":
		if s.focusZone == walletHomeZoneList {
			s.focusZone = walletHomeZoneButtons
			s.btnIdx = 0
			return s, nil
		}
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
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

func (s *WalletHomeScreen) handleEnter() (
	Screen, tea.Cmd,
) {
	entries := s.ctx.paymentHistory().Entries()
	if s.focusZone == walletHomeZoneButtons {
		switch s.btnIdx {
		case 0: // Send
			return s.openSend()
		case 1: // Receive
			return s.openReceive()
		case 2: // Pairing
			return s.openPairing()
		}
		return s, nil
	}

	// Open the selected daemon record.
	if s.cursor < len(entries) {
		entry := entries[s.cursor]
		screen := NewPaymentDetailScreen(s.ctx, entry)
		return s, func() tea.Msg {
			return openTabMsg{
				Kind:        tabPayment,
				Label:       screen.label(),
				Key:         screen.key,
				Screen:      screen,
				FocusTabBar: true,
			}
		}
	}
	return s, nil
}

func (s *WalletHomeScreen) openSend() (
	Screen, tea.Cmd,
) {
	cfg := s.ctx.Cfg
	if s.ctx.LndClient == nil || !cfg.HasLND() ||
		!s.ctx.walletExists() {
		return s, nil
	}
	screen := NewSendScreen(s.ctx)
	return s, func() tea.Msg {
		return openTabMsg{
			Kind:   tabSend,
			Label:  "⚡ Send",
			Screen: screen,
		}
	}
}

func (s *WalletHomeScreen) openReceive() (
	Screen, tea.Cmd,
) {
	cfg := s.ctx.Cfg
	if s.ctx.LndClient == nil || !cfg.HasLND() ||
		!s.ctx.walletExists() {
		return s, nil
	}
	screen := NewReceiveScreen(s.ctx)
	return s, func() tea.Msg {
		return openTabMsg{
			Kind:   tabReceive,
			Label:  "⚡ Receive",
			Screen: screen,
		}
	}
}

func (s *WalletHomeScreen) openPairing() (
	Screen, tea.Cmd,
) {
	if !s.ctx.walletExists() {
		return s, nil
	}
	screen := NewPairingScreen(s.ctx)
	return s, func() tea.Msg {
		return openTabMsg{
			Kind:   tabPairing,
			Label:  "⚡ Zeus — LND REST",
			Screen: screen,
		}
	}
}

func (s *WalletHomeScreen) HandleMsg(
	msg tea.Msg,
) (Screen, tea.Cmd) {
	return s, nil
}

// ── View ────────────────────────────────────────────────

func (s *WalletHomeScreen) View(
	w, h int,
) string {
	s.clampCursor()
	history := s.ctx.paymentHistory()
	entries := history.Entries()
	cfg := s.ctx.Cfg
	status := s.ctx.Status

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

	isFocused := s.ctx.ContentFocused

	// ── Fixed header ─────────────────────────────
	var headerLines []string
	headerLines = append(headerLines, "")
	headerLines = append(headerLines,
		centerPad(
			theme.Header.Render("Off-Chain Wallet"),
			w))
	headerLines = append(headerLines, "")
	headerLines = append(headerLines,
		balanceSummaryLines(status, w)...)
	headerLines = append(headerLines, "")
	headerLines = append(headerLines, "")

	for _, source := range []struct {
		name        string
		observation app.Observation[[]lndrpc.PaymentEntry]
	}{{"Invoices", history.Invoices}, {"Payments", history.Payments}} {
		if !source.observation.Fresh() {
			notice := source.name + ": " + observedEmptyText(source.observation, "")
			if source.observation.Known() {
				notice = source.name + " stale. Retrying..."
			}
			headerLines = append(headerLines, " "+theme.Warning.Render(notice))
		}
	}

	// ── Buttons ──────────────────────────────────
	isOnButton := isFocused &&
		s.focusZone == walletHomeZoneButtons
	headerLines = append(headerLines,
		renderButtons(
			[]string{"Send", "Receive", "Pairing"},
			s.btnIdx, isOnButton, w, s.ctx.disabledWalletButtons(0, 1, 2)...))
	headerLines = append(headerLines, "")
	headerLines = append(headerLines, "")

	// ── Table header ─────────────────────────────
	tableW := w - 2
	if tableW < 40 {
		tableW = 40
	}

	dateW := 18
	memoW := tableW - dateW - 14 - 14 - 3
	if memoW < 6 {
		memoW = 6
	}
	valW := 14
	balW := 14

	hdrStyle := theme.TableHeader
	sepStyle := theme.TableDim

	hdr := " " +
		hdrStyle.Render(
			fmt.Sprintf("%-*s", dateW, "Date")) +
		hdrStyle.Render(
			fmt.Sprintf("%-*s", memoW, "Memo")) +
		hdrStyle.Render(
			fmt.Sprintf("%*s", valW, "Value")) +
		hdrStyle.Render(
			fmt.Sprintf("%*s", balW, "Balance"))
	headerLines = append(headerLines, hdr)
	headerLines = append(headerLines,
		" "+sepStyle.Render(
			strings.Repeat("─", tableW)))

	header := strings.Join(headerLines, "\n")
	headerH := len(headerLines)

	// ── Scrollable middle (payment rows) ─────────
	var midLines []string

	if len(entries) == 0 {
		midLines = append(midLines,
			" "+theme.Dim.Render(paymentHistoryEmptyText(history)))
	} else {
		balances := s.computeBalances(entries)

		negStyle := lipgloss.NewStyle().
			Foreground(theme.ColorDanger)
		posStyle := lipgloss.NewStyle().
			Foreground(theme.ColorPrimary)
		dimStyle := theme.Dim
		selBg := theme.NavActive

		for i, entry := range entries {
			isSelected := i == s.cursor &&
				isFocused &&
				s.focusZone == walletHomeZoneList

			date := formatTimestampTable(
				entry.CreationDate)
			dateStr := fmt.Sprintf("%-*s",
				dateW, date)

			// Unresolved payments are not failures or proof that no funds moved.
			unresolved := !entry.IsIncoming && entry.Status != "SUCCEEDED"

			memo := entry.Memo
			if unresolved {
				memo = "(" + paymentStateLabel(entry.Status) + ") " + memo
			} else if memo == "" {
				switch entry.Status {
				case "OPEN":
					memo = "(pending)"
				case "EXPIRED":
					memo = "(expired)"
				case "CANCELED":
					memo = "(canceled)"
				case "ACCEPTED":
					memo = "(accepted)"
				default:
					memo = "-"
				}
			}

			memo = ansi.Truncate(memo, memoW-1, "..")
			memoStr := pad(memo, memoW)

			var valStr string
			if unresolved {
				// Do not present unresolved transfers as settled value.
				valStr = fmt.Sprintf("%*s",
					valW, "-")
			} else if entry.IsIncoming {
				valStr = fmt.Sprintf("%*s", valW,
					formatSats(entry.AmountSats))
			} else {
				valStr = fmt.Sprintf("%*s", valW,
					"-"+formatSats(
						entry.AmountSats))
			}

			// Only settled records participate in the existing balance display.
			var balStr string
			if (entry.IsIncoming &&
				entry.Status != "SETTLED") ||
				unresolved {
				balStr = fmt.Sprintf("%*s", balW, "-")
			} else if i >= len(balances) {
				balStr = fmt.Sprintf("%*s", balW, "N/A")
			} else {
				bal := balances[i]
				balStr = fmt.Sprintf("%*s",
					balW, formatSats(bal))
			}

			marker := " "
			if isSelected {
				marker = theme.NavActive.Render("▸")
				midLines = append(midLines,
					marker+
						selBg.Render(dateStr)+
						selBg.Render(memoStr)+
						selBg.Render(valStr)+
						selBg.Render(balStr))
			} else if unresolved {
				// Keep unsettled outgoing records visually subdued.
				midLines = append(midLines,
					marker+
						dimStyle.Render(dateStr)+
						dimStyle.Render(memoStr)+
						dimStyle.Render(valStr)+
						dimStyle.Render(balStr))
			} else {
				var valRendered string
				if entry.IsIncoming &&
					entry.Status == "SETTLED" {
					valRendered =
						posStyle.Render(valStr)
				} else if entry.IsIncoming {
					// Unsettled invoice value is still a requested amount.
					valRendered =
						dimStyle.Render(valStr)
				} else {
					valRendered =
						negStyle.Render(valStr)
				}

				var dateRendered, memoRendered,
					balRendered string
				if entry.IsIncoming &&
					entry.Status != "SETTLED" {
					dateRendered =
						dimStyle.Render(dateStr)
					memoRendered =
						dimStyle.Render(memoStr)
					balRendered =
						dimStyle.Render(balStr)
				} else {
					dateRendered =
						theme.Value.Render(dateStr)
					memoRendered =
						theme.Dim.Render(memoStr)
					balRendered =
						theme.Value.Render(balStr)
				}
				midLines = append(midLines,
					marker+
						dateRendered+
						memoRendered+
						valRendered+
						balRendered)
			}
		}
	}

	midContent := strings.Join(midLines, "\n")

	// ── Viewport ─────────────────────────────────
	vpH := h - headerH
	if vpH < 1 {
		vpH = 1
	}

	vpRendered := renderViewport(
		midContent, w, vpH, s.cursor,
		len(midLines),
		len(entries) > 0 &&
			s.focusZone == walletHomeZoneList)

	// ── Assemble output ──────────────────────────
	return header + "\n" + vpRendered
}

// ── HelpBindings ────────────────────────────────────────

func (s *WalletHomeScreen) HelpBindings() []key.Binding {
	if s.ctx.walletClientUnavailable() || !s.ctx.walletDisplayAvailable() {
		return walletUnavailableHelpBindings(s.ctx)
	}
	if s.focusZone == walletHomeZoneList {
		return homeListBindings(
			"payments", "details", "buttons")
	}
	return homeButtonBindings(
		"payments", s.btnIdx, s.ctx.HasTabs)
}

// ── Helpers ─────────────────────────────────────────────

func (s *WalletHomeScreen) computeBalances(entries []lndrpc.PaymentEntry) []int64 {
	history := s.ctx.paymentHistory()
	if !history.Fresh() || s.ctx.Status == nil || !s.ctx.Status.Channels.Fresh() {
		return nil
	}
	balances := make([]int64, len(entries))
	var runBal int64
	for _, ch := range s.ctx.Status.Channels.Value.Channels {
		runBal += ch.LocalBalance
	}
	for i := 0; i < len(entries); i++ {
		balances[i] = runBal
		entry := entries[i]
		if entry.IsIncoming &&
			entry.Status == "SETTLED" {
			runBal -= entry.AmountSats
		} else if !entry.IsIncoming &&
			entry.Status == "SUCCEEDED" {
			// Only successful outgoing payments
			// affect the running balance.
			runBal += entry.AmountSats +
				entry.FeeSats
		}
	}
	return balances
}

func (s *WalletHomeScreen) clampCursor() {
	history := s.ctx.paymentHistory()
	count := len(history.Invoices.Value) + len(history.Payments.Value)
	if count == 0 {
		s.cursor = 0
		return
	}
	if s.cursor >= count {
		s.cursor = count - 1
	}
	if s.cursor < 0 {
		s.cursor = 0
	}
}

func paymentHistoryEmptyText(history app.PaymentHistorySnapshot) string {
	if history.Fresh() {
		return "No payments in the recent history window."
	}
	if history.Invoices.Err != nil || history.Payments.Err != nil {
		return "History unavailable. Retrying..."
	}
	return "Loading..."
}

func paymentStateLabel(state string) string {
	switch state {
	case "FAILED":
		return "failed"
	case "IN_FLIGHT":
		return "in flight"
	case "INITIATED":
		return "initiated"
	default:
		return "unknown"
	}
}
