package tui

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/virtpriv/node/internal/lndrpc"
	"github.com/virtpriv/node/internal/theme"
)

// PaymentDetailScreen follows one daemon record in its original wallet scope.
type PaymentDetailScreen struct {
	ctx      *ScreenContext
	owner    *paymentHistoryContext
	scope    walletObservationScope
	key      string
	incoming bool
}

func NewPaymentDetailScreen(ctx *ScreenContext, entry lndrpc.PaymentEntry) *PaymentDetailScreen {
	return &PaymentDetailScreen{ctx: ctx, owner: ctx.PaymentHistory, scope: ctx.walletObservationScope(), key: paymentHistoryKey(entry), incoming: entry.IsIncoming}
}

func (s *PaymentDetailScreen) current() bool {
	return s.owner != nil && s.owner == s.ctx.PaymentHistory && s.scope == s.ctx.walletObservationScope() && s.scope == s.owner.scope
}

func (s *PaymentDetailScreen) record() (lndrpc.PaymentEntry, string, bool) {
	if !s.current() {
		return lndrpc.PaymentEntry{}, previousWalletDetail, false
	}
	observation := s.owner.Payments
	if s.incoming {
		observation = s.owner.Invoices
	}
	return observedListRecord(observation, func(entry lndrpc.PaymentEntry) bool { return paymentHistoryKey(entry) == s.key })
}

func (s *PaymentDetailScreen) label() string {
	entry, _, found := s.record()
	label := "Payment"
	if found {
		label = entry.Memo
		if label == "" {
			prefix := "↑ "
			if entry.IsIncoming {
				prefix = "↓ "
			}
			label = prefix + formatSats(entry.AmountSats)
		}
	}
	return ansi.Truncate(label, 14, "..")
}

// ── Screen interface ────────────────────────────────────

func (s *PaymentDetailScreen) Init() tea.Cmd {
	return nil
}

func (s *PaymentDetailScreen) HandleKey(
	keyStr string, msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
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
		return s, nil
	case "backspace":
		return s, emitFocusParent
	}
	return s, nil
}

func (s *PaymentDetailScreen) HandleMsg(
	msg tea.Msg,
) (Screen, tea.Cmd) {
	return s, nil
}

func (s *PaymentDetailScreen) View(
	w, h int,
) string {
	p := newPane(w)
	entry, notice, found := s.record()
	if !found {
		p.title(theme.Header, "Payment Detail").warnWrap(notice)
		return p.render()
	}

	if entry.IsIncoming {
		switch entry.Status {
		case "SETTLED":
			p.title(theme.Success,
				"Received Payment")
		case "OPEN":
			p.title(theme.Header,
				"Pending Invoice")
		case "EXPIRED":
			p.title(theme.Warning,
				"Expired Invoice")
		case "CANCELED":
			p.title(theme.Warning,
				"Canceled Invoice")
		case "ACCEPTED":
			p.title(theme.Header,
				"Accepting Payment")
		default:
			p.title(theme.Header,
				"Incoming Invoice")
		}
	} else {
		switch entry.Status {
		case "SUCCEEDED":
			p.title(theme.Success, "Sent Payment")
		case "FAILED":
			p.title(theme.Warning, "Failed Payment")
		case "IN_FLIGHT", "INITIATED":
			p.title(theme.Header, "Pending Payment")
		default:
			p.title(theme.Warning, "Payment Status Unknown")
		}
	}

	if notice != "" {
		p.warnWrap(notice).blank()
	}
	p.field("Amount:  ",
		formatSats(entry.AmountSats)+" sats")
	if entry.FeeSats > 0 {
		p.field("Fee:     ",
			formatSats(entry.FeeSats)+" sats")
	}
	p.field("Status:  ", entry.Status)
	if entry.Memo != "" {
		p.field("Memo:    ", entry.Memo)
	}
	p.field("Date:    ",
		formatTimestampFull(entry.CreationDate))

	if entry.Preimage != "" {
		p.blank()
		p.labelLine("Preimage:")
		p.monoWrap(entry.Preimage)
	}
	if entry.PaymentHash != "" {
		p.blank()
		p.labelLine("Payment Hash:")
		p.monoWrap(entry.PaymentHash)
	}
	if len(entry.Hops) > 0 {
		p.blank()
		p.labelLine("Route:")
		p.line(renderRouteDiagram(entry.Hops, w))
	}

	return p.render()
}

func (s *PaymentDetailScreen) HelpBindings() []key.Binding {
	return viewDetailBindings(s.ctx.HasTabs)
}
