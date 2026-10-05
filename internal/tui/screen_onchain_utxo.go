package tui

import (
	"fmt"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/virtpriv/node/internal/lndrpc"
	"github.com/virtpriv/node/internal/theme"
)

// ── UtxoDetailScreen ───────────────────────────────────
// View-only tab showing a single UTXO's details.
// Backspace returns to the parent.

type UtxoDetailScreen struct{ onChainDetail }

func NewUtxoDetailScreen(ctx *ScreenContext, outpoint string) *UtxoDetailScreen {
	return &UtxoDetailScreen{onChainDetail: newOnChainDetail(ctx, outpoint)}
}

// ── Screen interface ────────────────────────────────────

func (s *UtxoDetailScreen) Init() tea.Cmd {
	return nil
}

func (s *UtxoDetailScreen) HandleKey(
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

func (s *UtxoDetailScreen) HandleMsg(
	msg tea.Msg,
) (Screen, tea.Cmd) {
	return s, nil
}

func (s *UtxoDetailScreen) View(
	w, h int,
) string {
	p := newPane(w)
	p.title(theme.Header, "UTXO Detail")
	u, notice, found := s.record()
	if notice != "" {
		p.warnWrap(notice).blank()
	}
	if !found {
		p.labelLine("Outpoint:").monoWrap(s.key)
		return p.render()
	}
	tx, _, txFound := observedListRecord(s.owner.OnChainTxs, func(tx lndrpc.OnChainTx) bool { return tx.Txid == u.Txid })
	date, label := "unavailable", "unavailable"
	if txFound {
		date, label = formatDateShort(tx.Timestamp), tx.Label
		if label == "" {
			label = "none"
		}
		if !s.owner.OnChainTxs.Fresh() {
			date += " (stale)"
			label += " (stale)"
		}
	}

	p.field("Amount:    ",
		formatSats(u.AmountSats)+" sats")

	confStr := fmt.Sprintf("%d", u.Confirmations)
	if u.Confirmations == 0 {
		confStr = "unconfirmed"
	}
	p.field("Confs:     ", confStr)

	dateStr := date
	if u.Confirmations == 0 {
		dateStr = "unconfirmed"
	}
	p.field("Date:      ", dateStr)
	p.blank()

	p.labelLine("Outpoint:")
	outpoint := fmt.Sprintf("%s:%d", u.Txid, u.Vout)
	p.monoWrap(outpoint)
	p.blank()

	p.labelLine("Address:")
	p.monoWrap(u.Address)
	p.blank()

	p.field("Label:     ", label)

	return p.render()
}

func (s *UtxoDetailScreen) HelpBindings() []key.Binding {
	return viewDetailBindings(s.ctx.HasTabs)
}

func (s *UtxoDetailScreen) record() (lndrpc.UTXO, string, bool) {
	if !s.current() {
		return lndrpc.UTXO{}, previousWalletDetail, false
	}
	return observedListRecord(s.owner.Utxos, func(u lndrpc.UTXO) bool { return fmt.Sprintf("%s:%d", u.Txid, u.Vout) == s.key })
}
