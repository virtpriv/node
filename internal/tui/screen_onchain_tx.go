package tui

import (
	"fmt"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/virtpriv/node/internal/lndrpc"
	"github.com/virtpriv/node/internal/theme"
)

// ── OnChainTxScreen ────────────────────────────────────
// View-only tab showing a single on-chain transaction.
// Backspace returns to the parent.

type OnChainTxScreen struct{ onChainDetail }

func NewOnChainTxScreen(ctx *ScreenContext, txid string) *OnChainTxScreen {
	return &OnChainTxScreen{onChainDetail: newOnChainDetail(ctx, txid)}
}

// ── Screen interface ────────────────────────────────────

func (s *OnChainTxScreen) Init() tea.Cmd {
	return nil
}

func (s *OnChainTxScreen) HandleKey(
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

func (s *OnChainTxScreen) HandleMsg(
	msg tea.Msg,
) (Screen, tea.Cmd) {
	return s, nil
}

func (s *OnChainTxScreen) View(
	w, h int,
) string {
	p := newPane(w)
	tx, notice, found := s.record()
	if !found {
		p.title(theme.Header, "Transaction Detail").warnWrap(notice).blank()
		p.labelLine("TX ID:").monoWrap(s.key)
		return p.render()
	}

	switch {
	case tx.IsAnchorSweep:
		// No title — explanation text is the header
	case tx.TxType == "channel_open":
		p.title(theme.Header, "Channel Open")
	case tx.TxType == "channel_close":
		p.title(theme.Warning, "Channel Close")
	case tx.TxType == "send":
		p.title(theme.Warning, "On-Chain Send")
	default:
		p.title(theme.Success, "On-Chain Receive")
	}

	if notice != "" {
		p.warnWrap(notice).blank()
	}
	if tx.IsAnchorSweep {
		p.dim("330-sat anchor from force close.")
		p.dim("Sweep fee exceeded value.")
		p.dim("No funds lost -- this is normal.")
		p.blank()
	}

	if tx.ChannelPeer != "" {
		p.field("Peer:    ", tx.ChannelPeer)
	}
	absAmt := tx.Amount
	if absAmt < 0 {
		absAmt = -absAmt
	}
	p.field("Amount:  ",
		formatSats(absAmt)+" sats")
	if tx.Fee > 0 {
		p.field("Fee:     ",
			formatSats(tx.Fee)+" sats")
	}
	confStr := fmt.Sprintf("%d", tx.Confirmations)
	if tx.Confirmations == 0 {
		if tx.IsAnchorSweep {
			confStr = "abandoned"
		} else {
			confStr = "unconfirmed"
		}
	}
	p.field("Confs:   ", confStr)

	if tx.TxType == "channel_close" && tx.Confirmations > 0 {
		if s.ctx.Status == nil || !s.ctx.Status.Channels.Fresh() {
			p.field("Locked:  ", "unavailable")
		} else {
			for _, fc := range s.ctx.Status.Channels.Value.Pending.PendingForceCloseChannels {
				if fc.ClosingTxid == tx.Txid && fc.BlocksRemaining > 0 {
					p.field("Locked:  ", fmt.Sprintf("~%d blocks remaining", fc.BlocksRemaining))
					break
				}
			}
		}
	}

	if tx.BlockHeight > 0 {
		p.field("Block:   ",
			fmt.Sprintf("%d", tx.BlockHeight))
	}
	p.field("Date:    ",
		formatTimestampFull(tx.Timestamp))

	p.blank()
	p.labelLine("TX ID:")
	p.monoWrap(tx.Txid)

	if len(tx.Inputs) > 0 {
		p.blank()
		p.labelLine("Inputs")
		maxW := max(w-5, 16)
		for i, inp := range tx.Inputs {
			isLast := i == len(tx.Inputs)-1
			connector := "├──"
			if isLast {
				connector = "└──"
			}
			cont := "│  "
			if isLast {
				cont = "   "
			}
			ownership := ""
			if inp.IsOurs {
				ownership = " (ours)"
			}
			outpoint := inp.Outpoint
			if len(outpoint)+len(ownership) <= maxW {
				p.line(fmt.Sprintf(" %s %s%s",
					connector,
					theme.Mono.Render(outpoint),
					theme.Dim.Render(ownership)))
			} else {
				rem := outpoint
				first := true
				for len(rem) > 0 {
					pfx := " " + cont + " "
					if first {
						pfx = " " + connector + " "
						first = false
					}
					end := maxW
					if end > len(rem) {
						end = len(rem)
					}
					p.line(pfx +
						theme.Mono.Render(rem[:end]))
					rem = rem[end:]
				}
				if ownership != "" {
					p.line(" " + cont + " " +
						theme.Dim.Render(ownership))
				}
			}
			if !isLast {
				p.line(" │")
			}
		}
	}

	if len(tx.Outputs) > 0 {
		p.blank()
		p.labelLine("Outputs")
		maxW := max(w-5, 16)
		for i, out := range tx.Outputs {
			amtStr := formatSats(out.Amount)
			if out.Amount == 0 {
				amtStr = "—"
			}
			labelStr := ""
			if out.Label != "" {
				labelStr = " (" + out.Label + ")"
			}
			isLast := i == len(tx.Outputs)-1
			connector := "├──"
			if isLast {
				connector = "└──"
			}
			cont := "│  "
			if isLast {
				cont = "   "
			}
			addrStyle := theme.Mono
			if out.Label == "destination" ||
				out.Label == "channel" {
				addrStyle = theme.Value
			}
			addr := out.Address
			if len(addr)+2+len(amtStr)+5+
				len(labelStr) <= maxW {
				p.line(fmt.Sprintf(" %s %s%s",
					connector,
					addrStyle.Render(addr),
					theme.Value.Render("  "+
						amtStr+" sats")+
						theme.Dim.Render(labelStr)))
			} else {
				rem := addr
				first := true
				for len(rem) > 0 {
					pfx := " " + cont + " "
					if first {
						pfx = " " + connector + " "
						first = false
					}
					end := maxW
					if end > len(rem) {
						end = len(rem)
					}
					p.line(pfx +
						addrStyle.Render(rem[:end]))
					rem = rem[end:]
				}
				p.line(fmt.Sprintf(" %s %s%s",
					cont,
					theme.Value.Render(
						amtStr+" sats"),
					theme.Dim.Render(labelStr)))
			}
			if !isLast {
				p.line(" │")
			}
		}
	}

	if tx.Fee > 0 {
		p.blank()
		p.field("Fee:     ",
			formatSats(tx.Fee)+" sats")
	}

	return p.render()
}

func (s *OnChainTxScreen) HelpBindings() []key.Binding {
	return viewDetailBindings(s.ctx.HasTabs)
}

func (s *OnChainTxScreen) record() (lndrpc.OnChainTx, string, bool) {
	if !s.current() {
		return lndrpc.OnChainTx{}, previousWalletDetail, false
	}
	return observedListRecord(s.owner.OnChainTxs, func(tx lndrpc.OnChainTx) bool { return tx.Txid == s.key })
}
