package tui

import (
	"fmt"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/virtpriv/node/internal/theme"
)

// NodeInfoScreen presents shared node observations. QR and Copy require a
// current advertisement; terminal Copy retains its accepted snapshot.
type NodeInfoScreen struct {
	ctx       *ScreenContext
	buttonIdx int
	display   qrCopyDisplayState
}

func NewNodeInfoScreen(
	ctx *ScreenContext,
) *NodeInfoScreen {
	return &NodeInfoScreen{ctx: ctx}
}

// ── Screen interface ────────────────────────────────────

func (s *NodeInfoScreen) Init() tea.Cmd {
	return nil
}

// isTorURI classifies a node URI as Tor or clearnet by
// looking for the ".onion:" substring. Matches the
// inline pattern used elsewhere in the tui package.
func isTorURI(uri string) bool {
	return strings.Contains(uri, ".onion:")
}

// classifyURIs splits the URI list into clearnet-first
// and tor-second order, matching LND's typical
// advertisement order. Returns the split lists so the
// screen can label buttons and QRs independently.
func classifyURIs(
	uris []string,
) (clearnet, tor []string) {
	for _, u := range uris {
		if isTorURI(u) {
			tor = append(tor, u)
		} else {
			clearnet = append(clearnet, u)
		}
	}
	return
}

// Sharing actions require a current advertisement; retained values remain
// visible in the body during an observation failure.
func (s *NodeInfoScreen) buttons() []string {
	if s.ctx.Status == nil || !s.ctx.Status.Node.Fresh() {
		return nil
	}
	clearnet, tor := classifyURIs(s.ctx.Status.Node.Value.URIs)
	var out []string
	if len(clearnet) > 0 {
		out = append(out, "Show QR (Clearnet)")
	}
	if len(tor) > 0 {
		out = append(out, "Show QR (Tor)")
	}
	if len(s.ctx.Status.Node.Value.URIs) > 0 {
		out = append(out, "Copy URIs")
	}
	return out
}

// buttonAction returns the tea.Cmd for the button at
// the given index in the current buttons() slice, or
// nil if the index is out of range / the action has no
// effect.
func (s *NodeInfoScreen) buttonAction(
	idx int,
) tea.Cmd {
	if s.ctx.Status == nil || !s.ctx.Status.Node.Fresh() {
		return nil
	}
	clearnet, tor := classifyURIs(s.ctx.Status.Node.Value.URIs)
	buttons := s.buttons()
	if idx < 0 || idx >= len(buttons) {
		return nil
	}
	request := &qrCopyDisplayRequest{
		owner: s, wallet: s.ctx.walletObservationScope(), pubkey: s.ctx.Status.Node.Value.Pubkey,
	}
	switch buttons[idx] {
	case "Show QR (Clearnet)":
		request.text, request.label = clearnet[0], "Node URI (Clearnet)"
	case "Show QR (Tor)":
		request.text, request.label = tor[0], "Node URI (Tor)"
	case "Copy URIs":
		request.copy = true
		request.uris = slices.Clone(s.ctx.Status.Node.Value.URIs)
		request.text = nodeURIText(request.uris)
	}
	return s.display.command(request)
}

func (s *NodeInfoScreen) maxBtn() int {
	return len(s.buttons()) - 1
}

func (s *NodeInfoScreen) HandleKey(
	keyStr string, msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit
	case "left":
		if s.buttonIdx > 0 {
			s.buttonIdx--
			return s, nil
		}
		return s, emitFocusSidebar
	case "right":
		if s.buttonIdx < s.maxBtn() {
			s.buttonIdx++
		}
		return s, nil
	case "up":
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
		return s, nil
	case "down", "tab":
		return s, nil
	case "shift+tab":
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
		return s, nil
	case "backspace":
		return s, emitFocusParent
	case "enter":
		return s, s.buttonAction(s.buttonIdx)
	}
	return s, nil
}

func (s *NodeInfoScreen) HandleMsg(
	msg tea.Msg,
) (Screen, tea.Cmd) {
	// Model publishes application observations through ctx.Status.
	//
	// No defensive clamp on buttonIdx either. If the
	// button count changes under us (user toggles
	// P2P mode, URI list shifts), the three consumers
	// of buttonIdx — HandleKey's left/right bounds
	// checks and buttonAction's range check — are all
	// independently range-safe. Worst case is a stale
	// buttonIdx that points past the new end; enter
	// does nothing, the user presses left, the
	// highlight snaps back into range. Same pattern as
	// PairingScreen.
	return s, nil
}

func (s *NodeInfoScreen) View(w, h int) string {
	status := s.ctx.Status
	cfg := s.ctx.Cfg

	p := newPane(w)
	p.title(theme.Header, "Node Info")

	if status == nil {
		p.dim("Waiting for LND...")
		// No buttons — user navigates away via arrow
		// keys like any other non-flow screen.
		return p.renderWithBottomButtons(
			nil, 0, s.ctx.ContentFocused, h)
	}

	// ── Pubkey ────────────────────────────────────
	// The pubkey's first char aligns with the
	// "Pubkey:" label's P via p.mono's standard
	// 1-char leading space. A 66-char pubkey plus
	// that space fills the 67-char pane exactly, so
	// the right edge touches the pane border with no
	// margin. That is a deliberate trade for label
	// alignment over symmetric breathing room.
	p.labelLine("Pubkey:")
	if status.Node.Value.Pubkey != "" {
		p.mono(status.Node.Value.Pubkey)
	} else {
		p.dim("(unavailable)")
	}
	p.blank()

	// labelW is the width of the longest label in
	// the aligned field groups below ("Outbound
	// Liquidity:" at 19 chars) plus 1 char of gap
	// between the colon and the value.
	const labelW = 20

	// ── Identity group ────────────────────────────
	alias := status.Node.Value.Alias
	if alias == "" {
		alias = "(none)"
	}
	p.fieldAligned("Alias:", observationText(status.Node, alias), labelW)
	p.fieldAligned("P2P Mode:",
		p2pModeLabel(cfg.P2PMode), labelW)
	if nodeVersion(status) != "" {
		p.fieldAligned("LND Version:",
			nodeVersion(status), labelW)
	}
	p.blank()

	// ── Network presence group ────────────────────
	// Node Capacity is the sum of channel capacities
	// (static total), distinct from
	// Outbound+Inbound (dynamic split minus reserves
	// and commitment fees).
	var totalCap, totalLocal, totalRemote int64
	activeCount := 0
	for _, ch := range status.Channels.Value.Channels {
		if ch.Pending {
			continue
		}
		totalCap += ch.Capacity
		totalLocal += ch.LocalBalance
		totalRemote += ch.RemoteBalance
		if ch.Active {
			activeCount++
		}
	}
	p.fieldAligned("Peers:",
		observationText(status.Node, fmt.Sprintf("%d", status.Node.Value.Peers)), labelW)
	p.fieldAligned("Active Channels:",
		observationText(status.Channels, fmt.Sprintf("%d", activeCount)), labelW)
	p.fieldAligned("Node Capacity:",
		channelAmountText(status, totalCap),
		labelW)
	p.blank()

	// ── Balances group ────────────────────────────
	// Total Spendable is Outbound + On-Chain — what
	// the user can actually send right now.
	// Inbound is deliberately excluded because it's
	// not the user's money; it's the remote side's
	// liquidity that can be received into.
	totalSpendable := "unavailable"
	if status.Balance.Known() && status.Channels.Known() {
		totalSpendable = formatSats(totalLocal+parseBalance(status.Balance.Value.TotalBalance)) + " sats"
		if !status.Balance.Fresh() || !status.Channels.Fresh() {
			totalSpendable += " (stale)"
		}
	}
	p.line(statusNotice(status))
	p.fieldAligned("Outbound Liquidity:",
		channelAmountText(status, totalLocal),
		labelW)
	p.fieldAligned("Inbound Liquidity:",
		channelAmountText(status, totalRemote),
		labelW)
	p.fieldAligned("On-Chain Balance:",
		onChainBalanceText(status),
		labelW)
	p.fieldAligned("Total Spendable:",
		totalSpendable,
		labelW)
	p.blank()

	// ── URI status ────────────────────────────────
	// URI text stays in the copy view, where terminal selection avoids wrapping.
	if !status.Node.Fresh() {
		p.dim("Advertised addresses unavailable or stale.")
	} else if len(status.Node.Value.URIs) == 0 {
		if !status.Node.Value.SyncedChain {
			p.dim(
				"No URIs advertised yet — LND starts")
			p.dim(
				"advertising addresses once the chain")
			p.dim("finishes syncing.")
		} else {
			p.warn(
				"No URIs advertised by LND — configure")
			p.warn(
				"listen addresses to make your node")
			p.warn("reachable.")
		}
	} else {
		p.dim(
			"Press Copy URIs to view your node URIs")
		p.dim(
			"for easy copy-paste, or Show")
		p.dim(
			"QR to scan one into another device.")
	}

	if s.display.failed {
		p.warn("Copy display did not complete. Try again.")
	}

	// ── Buttons pinned to bottom ──────────────────
	return p.renderWithBottomButtons(
		s.buttons(), s.buttonIdx,
		s.ctx.ContentFocused, h)
}

func (s *NodeInfoScreen) HelpBindings() []key.Binding {
	binds := buttonNav(s.buttonIdx)
	binds = append(binds, kEnter)
	if s.ctx.HasTabs {
		binds = append(binds, kShiftTabBar)
	}
	binds = append(binds, kQuit)
	return binds
}
