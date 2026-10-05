package tui

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/virtpriv/node/internal/theme"
)

// ── ChannelsHomeScreen ─────────────────────────────────
// Section home for Channels. Two focus zones: buttons
// (Open Channel, Node Info, History) and scrollable
// channel list. Reads live data through ctx.Status
// pointer — no snapshot, since the home screen persists
// for the lifetime of the program and must always show
// current data.

const (
	chanHomeZoneButtons = 0
	chanHomeZoneList    = 1
)

type ChannelsHomeScreen struct {
	ctx       *ScreenContext
	btnIdx    int // 0=Open Channel, 1=Node Info, 2=History
	focusZone int // 0=buttons, 1=channel list
	cursor    int // position in channel list

	// Zero balance interstitial
	zeroBalanceMsg bool
	fundBtnIdx     int // 0=Go Back, 1=Fund Wallet
}

func NewChannelsHomeScreen(
	ctx *ScreenContext,
) *ChannelsHomeScreen {
	return &ChannelsHomeScreen{
		ctx: ctx,
	}
}

// ── Screen interface ────────────────────────────────────

func (s *ChannelsHomeScreen) Init() tea.Cmd {
	return nil
}

func (s *ChannelsHomeScreen) HandleKey(
	keyStr string, msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
	// Zero-balance interstitial intercepts all keys
	if s.zeroBalanceMsg {
		return s.handleZeroBalanceKey(keyStr)
	}

	channels := s.channels()
	s.clampCursor()

	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit
	case "left":
		if s.focusZone == chanHomeZoneButtons &&
			s.btnIdx > 0 {
			s.btnIdx--
			return s, nil
		}
		return s, emitFocusSidebar
	case "right":
		if s.focusZone == chanHomeZoneButtons &&
			s.ctx.Cfg.HasLND() &&
			s.ctx.walletDisplayAvailable() &&
			s.btnIdx < 2 {
			s.btnIdx++
		}
		return s, nil
	case "up":
		if s.focusZone == chanHomeZoneList {
			if s.cursor > 0 {
				s.cursor--
			} else {
				s.focusZone = chanHomeZoneButtons
				s.btnIdx = 0
			}
			return s, nil
		}
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
		return s, nil
	case "down", "tab":
		if s.focusZone == chanHomeZoneButtons {
			if len(channels) > 0 {
				s.focusZone = chanHomeZoneList
				s.cursor = 0
			}
			return s, nil
		}
		if s.focusZone == chanHomeZoneList {
			if s.cursor < len(channels)-1 {
				s.cursor++
			}
		}
		return s, nil
	case "shift+tab":
		if s.focusZone == chanHomeZoneList {
			s.focusZone = chanHomeZoneButtons
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

func (s *ChannelsHomeScreen) handleEnter() (
	Screen, tea.Cmd,
) {
	if s.focusZone == chanHomeZoneButtons {
		switch s.btnIdx {
		case 0: // Open Channel
			return s.openChannel()
		case 1: // Node Info
			return s.openNodeInfo()
		case 2: // History
			return s.openHistory()
		}
		return s, nil
	}

	// Open the selected channel by funding outpoint.
	channels := s.channels()
	if s.cursor < len(channels) &&
		!channels[s.cursor].Pending {
		ch := channels[s.cursor]
		screen := NewChannelDetailScreen(
			s.ctx, ch)
		return s, func() tea.Msg {
			return openTabMsg{
				Kind:   tabChannel,
				Label:  screen.label(),
				Key:    ch.ChannelPoint,
				Screen: screen,
			}
		}
	}
	return s, nil
}

func (s *ChannelsHomeScreen) openChannel() (
	Screen, tea.Cmd,
) {
	cfg := s.ctx.Cfg
	if s.ctx.LndClient == nil || !cfg.HasLND() ||
		!s.ctx.walletExists() {
		return s, nil
	}
	// Zero balance: show educational message
	if s.ctx.Status != nil &&
		s.ctx.Status.Balance.Fresh() && s.ctx.Status.Balance.Value.TotalBalance == "0" {
		s.zeroBalanceMsg = true
		// Highlight Fund Wallet (index 1) by default so
		// pressing enter or down lands on the action
		// button, not the Go Back button.
		s.fundBtnIdx = 1
		return s, nil
	}
	// Normal path: channel open
	screen := NewChannelOpenScreen(s.ctx)
	return s, func() tea.Msg {
		return openTabMsg{
			Kind:   tabOpenChannel,
			Label:  "Open Channel",
			Screen: screen,
		}
	}
}

func (s *ChannelsHomeScreen) openNodeInfo() (
	Screen, tea.Cmd,
) {
	screen := NewNodeInfoScreen(s.ctx)
	return s, func() tea.Msg {
		return openTabMsg{
			Kind:   tabNodeInfo,
			Label:  "Node Info",
			Screen: screen,
		}
	}
}

func (s *ChannelsHomeScreen) openHistory() (
	Screen, tea.Cmd,
) {
	screen := NewChannelHistoryScreen(s.ctx)
	return s, func() tea.Msg {
		return openTabMsg{Kind: tabChannelHistory, Label: "History", Screen: screen}
	}
}

func (s *ChannelsHomeScreen) handleZeroBalanceKey(
	keyStr string,
) (Screen, tea.Cmd) {
	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit
	case "left":
		if s.fundBtnIdx > 0 {
			s.fundBtnIdx--
			return s, nil
		}
		return s, emitFocusSidebar
	case "right":
		if s.fundBtnIdx < 1 {
			s.fundBtnIdx++
		}
		return s, nil
	case "up":
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
		return s, nil
	case "enter":
		if s.fundBtnIdx == 0 {
			// Go Back
			s.zeroBalanceMsg = false
			return s, nil
		}
		// Fund Wallet → open on-chain receive
		s.zeroBalanceMsg = false
		screen := NewOCReceiveScreen(s.ctx)
		return s, func() tea.Msg {
			return openTabMsg{
				Kind:   tabOCReceive,
				Label:  "⛓ Receive",
				Screen: screen,
			}
		}
	}
	return s, nil
}

func (s *ChannelsHomeScreen) HandleMsg(
	msg tea.Msg,
) (Screen, tea.Cmd) {
	return s, nil
}

// ── View ────────────────────────────────────────────────

func (s *ChannelsHomeScreen) View(
	w, h int,
) string {
	s.clampCursor()
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

	// Zero-balance educational message
	if s.zeroBalanceMsg {
		p := newPane(w)
		p.blank()
		p.line(centerPad(
			theme.Header.Render("Fund Your Wallet"), w))
		p.blank()
		p.dim("  Opening a Lightning channel requires")
		p.dim("  on-chain Bitcoin. Send Bitcoin to your")
		p.dim("  on-chain address first, then return")
		p.dim("  here to open a channel.")
		if status.Bitcoin.Fresh() && !status.Bitcoin.Value.Synced {
			p.blank()
			p.line("  " + theme.Warn.Render(
				"Bitcoin Core is syncing. If you have"))
			p.line("  " + theme.Warn.Render(
				"already sent funds, they will appear"))
			p.line("  " + theme.Warn.Render(
				"once sync is complete."))
		}
		return p.renderWithBottomButtons(
			[]string{"Go Back", "Fund Wallet"},
			s.fundBtnIdx, true, h)
	}

	isFocused := s.ctx.ContentFocused
	channels := status.Channels.Value.Channels

	// ── Fixed header ─────────────────────────────
	var headerLines []string
	headerLines = append(headerLines, "")
	headerLines = append(headerLines,
		centerPad(
			theme.Header.Render(
				"Lightning Channels Dashboard"),
			w))
	headerLines = append(headerLines, "")

	// P2P Mode sits above the balance group. The
	// balanceSummaryLines helper begins its output
	// with a blank row (row 0 of leftLines pairs with
	// the box top border on the right), so no explicit
	// separator is needed here — the caller-side blank
	// plus the helper's blank would produce two rows
	// of gap, which is too much.
	headerLines = append(headerLines,
		" "+theme.Label.Render("P2P Mode: ")+
			theme.Value.Render(
				p2pModeLabel(cfg.P2PMode)))

	headerLines = append(headerLines,
		balanceSummaryLines(status, w)...)

	headerLines = append(headerLines, "")
	headerLines = append(headerLines, "")

	header := strings.Join(headerLines, "\n")
	headerH := len(headerLines)

	// ── Buttons (below balance summary) ─────────
	isOnButton := isFocused &&
		s.focusZone == chanHomeZoneButtons
	var btnLines []string
	btnLines = append(btnLines,
		renderButtons(
			[]string{
				"Open Channel",
				"Node Info",
				"History",
			},
			s.btnIdx, isOnButton, w, s.ctx.disabledWalletButtons(0)...))
	btnLines = append(btnLines, "")
	btnLines = append(btnLines, "")

	btnContent := strings.Join(btnLines, "\n")
	btnH := len(btnLines)

	// ── Scrollable middle (all channel bars) ─────
	chanCount := len(channels)
	nameW := 17
	barW := w - nameW - 22
	if barW < 8 {
		barW = 8
	}

	var midLines []string

	if chanCount == 0 {
		midLines = append(midLines,
			theme.Dim.Render(" "+observationText(status.Channels, "No channels yet.")))
	} else {
		for i, ch := range channels {
			if i > 0 {
				midLines = append(midLines, "")
			}

			isSelected := isFocused &&
				s.cursor == i &&
				s.focusZone == chanHomeZoneList

			name := ch.PeerAlias
			if name == "" {
				name = ch.RemotePubkey
			}
			if len(name) > nameW {
				name = name[:nameW-3] + "..."
			}

			dot := theme.RedDot.Render("○")
			if ch.Active {
				dot = theme.GreenDot.Render("●")
			}
			if ch.Pending {
				dot = theme.Dim.Render("◌")
			}

			localFill := 0
			if ch.Capacity > 0 {
				localFill = int(
					float64(ch.LocalBalance) /
						float64(ch.Capacity) *
						float64(barW))
			}
			if localFill > barW {
				localFill = barW
			}
			remoteFill := barW - localFill

			// Bar colors reflect channel state only, not
			// cursor position. The row's orange highlight
			// is sufficient to show which channel the
			// cursor is on — the bar color is reserved
			// for the semantic question "is this channel
			// reachable right now?"
			var lColor, rColor color.Color
			if ch.Active {
				lColor = theme.ColorChanLocal
				rColor = theme.ColorChanRemote
			} else {
				lColor = theme.ColorChanLocalDim
				rColor = theme.ColorChanRemoteDim
			}

			lBar := lipgloss.NewStyle().
				Foreground(lColor).
				Render(
					strings.Repeat("█", localFill))
			rBar := lipgloss.NewStyle().
				Foreground(rColor).
				Render(strings.Repeat("█",
					remoteFill))
			barStr := lBar + rBar

			vals := fmt.Sprintf("%s / %s",
				formatSatsCompact(ch.LocalBalance),
				formatSatsCompact(ch.RemoteBalance))
			valsPad := pad(vals, 14)

			marker := " "
			nameStyle := theme.Value
			if isSelected {
				marker = theme.NavActive.Render("▸")
				nameStyle = theme.NavActive
			}
			namePad := pad(name, nameW)

			line := marker + " " + dot + " " +
				nameStyle.Render(namePad) + " " +
				barStr + " " +
				theme.Dim.Render(valsPad)

			midLines = append(midLines, line)
		}

		if status.Channels.Value.Pending.PendingOpen > 0 {
			midLines = append(midLines, "")
			midLines = append(midLines,
				" "+theme.Dim.Render(
					fmt.Sprintf("%d pending",
						status.Channels.Value.Pending.PendingOpen)))
		}
	}

	midContent := strings.Join(midLines, "\n")

	// ── Viewport ─────────────────────────────────
	vpH := h - headerH - btnH
	if vpH < 1 {
		vpH = 1
	}

	// Each channel is 2 lines (bar + blank gap)
	// except last which is 1 line
	cursorLine := s.cursor * 2

	vpRendered := renderViewport(
		midContent, w, vpH, cursorLine,
		len(midLines),
		chanCount > 0 &&
			s.focusZone == chanHomeZoneList)

	// ── Assemble output ──────────────────────────
	return header + "\n" + btnContent + "\n" + vpRendered
}

// ── HelpBindings ────────────────────────────────────────

func (s *ChannelsHomeScreen) HelpBindings() []key.Binding {
	if s.ctx.walletClientUnavailable() || !s.ctx.walletDisplayAvailable() {
		return walletUnavailableHelpBindings(s.ctx)
	}
	if s.zeroBalanceMsg {
		return actionButtonBindings(
			s.fundBtnIdx, s.ctx.HasTabs)
	}
	if s.focusZone == chanHomeZoneList {
		return homeListBindings(
			"channels", "details", "buttons")
	}
	return homeButtonBindings(
		"channels", s.btnIdx, s.ctx.HasTabs)
}

// ── Helpers ─────────────────────────────────────────────

func (s *ChannelsHomeScreen) channels() []channelInfo {
	if s.ctx.Status == nil {
		return nil
	}
	return s.ctx.Status.Channels.Value.Channels
}

func (s *ChannelsHomeScreen) clampCursor() {
	channels := s.channels()
	if len(channels) == 0 {
		s.cursor = 0
		return
	}
	if s.cursor >= len(channels) {
		s.cursor = len(channels) - 1
	}
	if s.cursor < 0 {
		s.cursor = 0
	}
}
