package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/x/ansi"
	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/theme"
)

// ChannelDetailScreen retains the selected funding outpoint and owns its close
// flow. The embedded screen owns approval and the result for that channel.
type ChannelDetailScreen struct {
	ctx   *ScreenContext
	point string
	scope walletObservationScope

	// Button index for detail view (0=Cancel, 1=Close)
	viewBtnIdx int

	// A nil close screen means the detail view is active;
	// non-nil means the close flow is active and all
	// input/rendering delegates to it.
	closeScreen *ChannelCloseScreen
}

func NewChannelDetailScreen(
	ctx *ScreenContext,
	ch channelInfo,
) *ChannelDetailScreen {
	return &ChannelDetailScreen{
		ctx:   ctx,
		point: ch.ChannelPoint,
		scope: ctx.walletObservationScope(),
	}
}

// ── Screen interface ────────────────────────────────────

func (s *ChannelDetailScreen) Init() tea.Cmd {
	return nil
}

func (s *ChannelDetailScreen) HandleKey(
	keyStr string, msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
	if s.closeScreen != nil {
		newClose, cmd :=
			s.closeScreen.HandleKey(keyStr, msg)
		s.closeScreen = newClose.(*ChannelCloseScreen)
		if s.closeScreen.Cancelled {
			s.closeScreen.fees.cancel()
			s.closeScreen = nil
			return s, nil
		}
		return s, cmd
	}

	ch, _, found := s.record()
	if !found || ch.Pending {
		switch keyStr {
		case "ctrl+c":
			return s, tea.Quit
		case "left":
			return s, emitFocusSidebar
		case "backspace":
			return s, emitFocusParent
		case "up", "shift+tab":
			if s.ctx.HasTabs {
				return s, emitFocusTabBar
			}
		}
		return s, nil
	}

	// Non-pending: Cancel / Close Channel buttons
	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit
	case "left":
		if s.viewBtnIdx > 0 {
			s.viewBtnIdx--
			return s, nil
		}
		return s, emitFocusSidebar
	case "right":
		if s.viewBtnIdx < 1 {
			s.viewBtnIdx++
		}
		return s, nil
	case "up", "shift+tab":
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
		return s, nil
	case "down", "tab":
		// Already on buttons, nowhere to go
		return s, nil
	case "backspace":
		return s, emitFocusParent
	case "enter":
		if s.viewBtnIdx == 0 {
			return s, emitCloseTab
		}
		return s.launchClose()
	}
	return s, nil
}

func (s *ChannelDetailScreen) HandleMsg(
	msg tea.Msg,
) (Screen, tea.Cmd) {
	if s.closeScreen != nil {
		newClose, cmd := s.closeScreen.HandleMsg(msg)
		s.closeScreen = newClose.(*ChannelCloseScreen)
		return s, cmd
	}

	return s, nil
}

func (s *ChannelDetailScreen) View(
	w, h int,
) string {
	if s.closeScreen != nil {
		return s.closeScreen.View(w, h)
	}

	ch, notice, found := s.record()
	p := newPane(w)
	if !found {
		return p.title(theme.Header, "Channel Detail").warnWrap(notice).render()
	}

	name := ch.PeerAlias
	if name == "" {
		name = ch.RemotePubkey
		if len(name) > 16 {
			name = name[:16] + "..."
		}
	}
	p.title(theme.Header, ansi.Truncate(name, max(0, w-2), "..."))

	status := theme.Success.Render("active")
	if !ch.Active {
		status = theme.Warning.Render("inactive")
	}
	if ch.Pending {
		status = theme.Dim.Render("pending")
	}

	if notice != "" {
		p.warnWrap(notice)
	}
	p.line(" " + theme.Label.Render("Status:    ") +
		status)
	p.field("Capacity:  ",
		formatSats(ch.Capacity)+" sats")
	p.field("Local:     ",
		formatSats(ch.LocalBalance)+" sats")
	p.field("Remote:    ",
		formatSats(ch.RemoteBalance)+" sats")

	barW := w - 4
	if barW > 40 {
		barW = 40
	}
	if barW >= 10 {
		p.blank()
		p.line(" " + renderLiquidityBar(
			ch.LocalBalance, ch.RemoteBalance,
			ch.Capacity, barW))
	}
	p.blank()

	if ch.Private {
		p.field("Type:      ", "private")
	} else {
		p.field("Type:      ", "public")
	}
	if strings.Contains(ch.CommitmentType, "TAPROOT") {
		p.field("Channel:   ", "taproot")
	}
	if ch.Initiator {
		p.field("Initiator: ", "you")
	}

	p.blank()
	p.labelLine("Pubkey:")
	p.mono(ch.RemotePubkey)

	if ch.ChanID > 0 {
		p.blank()
		p.monoField("Channel ID: ",
			fmt.Sprintf("%d", ch.ChanID))
	}

	// Only available open channels offer a close action.
	if !ch.Pending {
		btnFocused := s.ctx.ContentFocused
		return p.renderWithBottomButtons(
			[]string{"Cancel", "Close Channel"},
			s.viewBtnIdx, btnFocused, h, s.ctx.disabledWalletButtons(1)...)
	}

	return p.render()
}

func (s *ChannelDetailScreen) HelpBindings() []key.Binding {
	if s.closeScreen != nil {
		return s.closeScreen.HelpBindings()
	}
	ch, _, found := s.record()
	if !found || ch.Pending {
		return viewDetailBindings(s.ctx.HasTabs)
	}
	return detailActionBindings(
		"close channel", s.viewBtnIdx, s.ctx.HasTabs)
}

// ── Close channel launch ───────────────────────────────

func (s *ChannelDetailScreen) launchClose() (
	Screen, tea.Cmd,
) {
	ch, _, found := s.record()
	if !found || ch.Pending || !s.ctx.walletExists() {
		return s, nil
	}
	s.closeScreen = NewChannelCloseScreen(
		s.ctx,
		ch.ChannelPoint,
		ch.PeerAlias,
		ch.Capacity,
		ch.LocalBalance)
	return s, s.closeScreen.Init()
}

func (s *ChannelDetailScreen) current() bool { return s.scope == s.ctx.walletObservationScope() }

func (s *ChannelDetailScreen) record() (channelInfo, string, bool) {
	if !s.current() {
		return channelInfo{}, previousWalletDetail, false
	}
	observation := s.ctx.channelObservation()
	return observedListRecord(app.Observation[[]channelInfo]{Value: observation.Value.Channels, ObservedAt: observation.ObservedAt, Err: observation.Err}, func(ch channelInfo) bool { return ch.ChannelPoint == s.point })
}

func (s *ChannelDetailScreen) label() string {
	ch, _, found := s.record()
	if !found {
		return "Channel"
	}
	label := ch.PeerAlias
	if label == "" {
		label = ch.RemotePubkey
	}
	return ansi.Truncate(label, 20, "...")
}
