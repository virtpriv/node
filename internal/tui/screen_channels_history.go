package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/x/ansi"
	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/lndrpc"
	"github.com/virtpriv/node/internal/theme"
)

// ChannelHistoryScreen renders independently observed current and closed channels.
type ChannelHistoryScreen struct {
	ctx            *ScreenContext
	owner          *channelHistoryContext
	scope          walletObservationScope
	cursor         int
	selectedPoint  string
	selectedClosed bool
}

func NewChannelHistoryScreen(ctx *ScreenContext) *ChannelHistoryScreen {
	return &ChannelHistoryScreen{ctx: ctx, owner: ctx.ChannelHistory, scope: ctx.walletObservationScope()}
}

func (s *ChannelHistoryScreen) current() bool {
	return s.owner != nil && s.owner == s.ctx.ChannelHistory && s.scope == s.ctx.walletObservationScope()
}

func (s *ChannelHistoryScreen) entries() []app.ChannelHistoryEntry {
	if !s.current() {
		return nil
	}
	history := s.owner.ChannelHistorySnapshot
	if s.owner.scope != s.scope {
		history = app.ChannelHistorySnapshot{}
	}
	return history.Entries(s.ctx.channelObservation())
}

func (s *ChannelHistoryScreen) followSelection(entries []app.ChannelHistoryEntry) {
	for i, entry := range entries {
		if s.selectedPoint != "" && entry.ChannelPoint == s.selectedPoint && (entry.Status == "closed") == s.selectedClosed {
			s.cursor = i
			return
		}
	}
	s.cursor = min(s.cursor, max(0, len(entries)-1))
}

// ── Screen interface ────────────────────────────────────

func (s *ChannelHistoryScreen) Init() tea.Cmd {
	return tea.Batch(requestStatusCmd, requestChannelHistoryCmd)
}

func (s *ChannelHistoryScreen) HandleKey(
	keyStr string, msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
	entries := s.entries()
	s.followSelection(entries)
	defer func() {
		if len(entries) > 0 {
			s.selectedPoint = entries[s.cursor].ChannelPoint
			s.selectedClosed = entries[s.cursor].Status == "closed"
		}
	}()
	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit
	case "left":
		return s, emitFocusSidebar
	case "up":
		if s.cursor > 0 {
			s.cursor--
		} else if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
	case "down", "tab":
		if s.cursor < len(entries)-1 {
			s.cursor++
		}
	case "shift+tab":
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
	case "backspace":
		return s, emitFocusParent
	}
	return s, nil
}

func (s *ChannelHistoryScreen) HandleMsg(
	msg tea.Msg,
) (Screen, tea.Cmd) {
	if _, ok := msg.(tabActivatedMsg); ok && s.current() {
		return s, tea.Batch(requestStatusCmd, requestChannelHistoryCmd)
	}
	return s, nil
}

func (s *ChannelHistoryScreen) View(
	w, h int,
) string {
	entries := s.entries()
	s.followSelection(entries)
	if len(entries) > 0 {
		s.selectedPoint = entries[s.cursor].ChannelPoint
		s.selectedClosed = entries[s.cursor].Status == "closed"
	}
	if !s.current() {
		return newPane(w).title(theme.Header, "Channel History").warnWrap(previousWalletDetail).render()
	}
	var headerLines []string
	headerLines = append(headerLines, "")
	headerLines = append(headerLines,
		centerPad(
			theme.Header.Render("Channel History"),
			w))
	headerLines = append(headerLines, "")

	current := s.ctx.channelObservation()
	closed := s.owner.Closed
	if s.owner.scope != s.scope {
		closed = app.Observation[[]lndrpc.ClosedChannel]{}
	}
	for _, notice := range []string{channelSourceNotice("Current channels", current.Known(), current.Err), channelSourceNotice("Closed channels", closed.Known(), closed.Err)} {
		if notice != "" {
			headerLines = append(headerLines, " "+theme.Warning.Render(notice))
		}
	}
	if len(entries) == 0 {
		empty := "Channel history is not fully available."
		if current.Fresh() && closed.Fresh() {
			empty = "No channel history."
		}
		headerLines = append(headerLines,
			" "+theme.Dim.Render(empty))
		return strings.Join(headerLines, "\n")
	}

	isFocused := s.ctx.ContentFocused

	hdrStyle := theme.TableHeader
	sepStyle := theme.TableDim

	peerW := 16
	capW := 10
	statusW := 14
	closeW := w - peerW - capW - statusW - 5
	if closeW < 8 {
		closeW = 8
	}

	hdr := " " +
		hdrStyle.Render(pad("Peer", peerW)) +
		hdrStyle.Render(
			fmt.Sprintf("%*s", capW, "Capacity")) +
		hdrStyle.Render(
			pad("  Status", statusW)) +
		hdrStyle.Render(
			fmt.Sprintf("%*s", closeW, "Close"))
	headerLines = append(headerLines, hdr)
	headerLines = append(headerLines,
		" "+sepStyle.Render(
			strings.Repeat("─", w-2)))

	header := strings.Join(headerLines, "\n")
	headerH := len(headerLines)

	// ── Scrollable rows ──────────────────────────
	var midLines []string

	selStyle := theme.NavActive

	for i, ch := range entries {
		isSelected := isFocused &&
			s.cursor == i

		peer := ch.PeerAlias
		if peer == "" {
			if len(ch.RemotePubkey) > 12 {
				peer = ch.RemotePubkey[:12] + ".."
			} else {
				peer = ch.RemotePubkey
			}
		}
		peer = ansi.Truncate(peer, peerW-1, "..")
		peerStr := pad(peer, peerW)

		capStr := fmt.Sprintf("%*s", capW,
			formatSatsCompact(ch.Capacity))

		statusStr := pad("  "+ch.Status, statusW)
		closeLabel := ch.CloseType
		if closeLabel == "" {
			closeLabel = "-"
		}
		if closeLabel == "cooperative" {
			closeLabel = "coop"
		}
		if ch.BlocksRemaining > 0 {
			closeLabel = fmt.Sprintf("~%d blks",
				ch.BlocksRemaining)
		}
		closeStr := fmt.Sprintf("%*s",
			closeW, closeLabel)

		marker := " "
		if isSelected {
			marker = theme.NavActive.Render("▸")
			midLines = append(midLines,
				marker+
					selStyle.Render(peerStr)+
					selStyle.Render(capStr)+
					selStyle.Render(statusStr)+
					selStyle.Render(closeStr))
		} else {
			var statusRendered string
			switch ch.Status {
			case "active":
				statusRendered =
					theme.Success.Render(statusStr)
			case "pending close", "waiting close":
				statusRendered =
					theme.Warning.Render(statusStr)
			case "force close":
				statusRendered =
					theme.Warning.Render(statusStr)
			case "closed":
				statusRendered =
					theme.Dim.Render(statusStr)
			default:
				statusRendered =
					theme.Value.Render(statusStr)
			}

			var closeRendered string
			switch ch.CloseType {
			case "force":
				closeRendered =
					theme.Warning.Render(closeStr)
			default:
				closeRendered =
					theme.Dim.Render(closeStr)
			}

			midLines = append(midLines,
				marker+
					theme.Value.Render(peerStr)+
					theme.Value.Render(capStr)+
					statusRendered+
					closeRendered)
		}
	}

	midContent := strings.Join(midLines, "\n")

	vpH := h - headerH
	if vpH < 1 {
		vpH = 1
	}

	vpRendered := renderViewport(
		midContent, w, vpH, s.cursor,
		len(midLines),
		len(entries) > 0 && isFocused)

	return header + "\n" + vpRendered
}

func (s *ChannelHistoryScreen) HelpBindings() []key.Binding {
	binds := []key.Binding{kUpDownChannels, kSidebar}
	if s.ctx.HasTabs {
		binds = append(binds, kShiftTabBar)
	}
	binds = append(binds, kQuit)
	return binds
}

func channelSourceNotice(name string, known bool, err error) string {
	if err != nil {
		if known {
			return name + " stale; retrying."
		}
		return name + " unavailable; retrying."
	}
	if !known {
		return name + " loading..."
	}
	return ""
}
