package tui

import (
	"fmt"
	"slices"

	tea "charm.land/bubbletea/v2"

	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/theme"
)

// ── Confirm step ───────────────────────────────────────

func (s *ChannelOpenScreen) handleConfirmKey(
	keyStr string,
) (Screen, tea.Cmd) {
	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit
	case "left":
		if s.confirmBtnIdx > 0 {
			s.confirmBtnIdx--
		} else {
			return s, emitFocusSidebar
		}
		return s, nil
	case "right":
		if s.confirmBtnIdx < 1 {
			s.confirmBtnIdx++
		}
		return s, nil
	case "up":
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
		return s, nil
	case "backspace":
		s.backToInput()
		return s, nil
	case "enter":
		switch s.confirmBtnIdx {
		case 0: // Go Back
			s.backToInput()
			return s, nil
		case 1: // Confirm
			if s.attempt == nil || !s.ctx.walletExists() || !s.requireCurrentCoinScope() {
				return s, nil
			}
			if !slices.Equal(s.selection.Outpoints(), s.attempt.prepared.Request().Outpoints) {
				s.backToInput()
				s.error = "Coin selection changed. Review the channel again"
				return s, nil
			}
			if s.utxoErr != nil {
				s.error = s.utxoErr.Error()
				return s, nil
			}
			if _, err := s.selection.Total(s.utxos); err != nil {
				s.error = err.Error()
				return s, nil
			}
			s.cancelCoinRefresh()
			s.error = ""
			s.step = coStepOpening
			return s, openChannelCmd(s.client, s.attempt)
		}
	}
	return s, nil
}

func (s *ChannelOpenScreen) backToInput() {
	s.attempt = nil
	s.step = coStepInput
	s.error = ""
	s.confirmBtnIdx = 0
	s.focusZone = coZoneButtons
	s.btnIdx = 1
}

// ── Opening step ───────────────────────────────────────

func (s *ChannelOpenScreen) handleOpeningKey(
	keyStr string,
) (Screen, tea.Cmd) {
	switch keyStr {
	case "left":
		return s, emitFocusSidebar
	case "up", "shift+tab":
		return s, emitFocusTabBar
	}
	return s, nil
}

// ── Result step ────────────────────────────────────────

func (s *ChannelOpenScreen) handleResultKey(
	keyStr string,
) (Screen, tea.Cmd) {
	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit
	case "enter":
		return s, func() tea.Msg { return closeChannelOpenMsg{screen: s} }
	case "left":
		return s, emitFocusSidebar
	case "up", "shift+tab":
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
	case "backspace":
		return s, emitFocusParent
	}
	return s, nil
}

type closeChannelOpenMsg struct{ screen *ChannelOpenScreen }

func channelOpenBusy(screen Screen) bool {
	s, ok := screen.(*ChannelOpenScreen)
	return ok && s.step == coStepOpening
}

func (s *ChannelOpenScreen) viewConfirm(w, h int) string {
	p := newPane(w)
	p.title(theme.Warning, "Confirm Channel Open")
	req := s.attempt.prepared.Request()
	p.field("Peer: ", s.attempt.alias)
	p.monoWrap(req.Pubkey)
	if req.FundMax {
		p.line("Capacity: Max (determined by LND)")
	} else {
		p.line("Capacity: " + formatSats(req.AmountSats) + " sats")
	}
	kind := "public"
	if req.Private {
		kind = "private"
	}
	if req.Taproot {
		kind += ", taproot"
	}
	p.field("Type: ", kind)
	p.line(fmt.Sprintf("Funding pool: %d selected (%s sats)", len(req.Outpoints), formatSats(s.attempt.prepared.SelectedTotal())))
	p.line("LND may use a subset; no other coins are allowed.")
	p.line("Unconfirmed inputs: allowed, subject to LND checks")
	if req.SatPerVbyte == 0 {
		p.line("Fee rate: auto (LND default)")
	} else {
		p.line(fmt.Sprintf("Fee rate: %d sat/vB", req.SatPerVbyte))
	}
	p.line("Total fee and change: unavailable before funding")
	if req.FundMax {
		p.line("Max deducts fees and may leave change for reserves or limits.")
	}
	p.appendError(s.error)
	return p.renderWithBottomButtons([]string{"Go Back", "Confirm"}, s.confirmBtnIdx, s.ctx.ContentFocused, h, s.ctx.disabledWalletButtons(1)...)
}

func (s *ChannelOpenScreen) viewOpening(w, h int) string {
	p := newPane(w)
	p.title(theme.Header, "Opening Channel...")
	p.valueWrap("Connecting to peer, checking selected coins and requesting funding.")
	p.blank()
	p.dim("This may take several minutes over Tor.")
	p.dim("Do not close the terminal.")
	return p.renderWithBottomButtons([]string{"Opening..."}, 0, false, h)
}

func (s *ChannelOpenScreen) viewResult(w, h int) string {
	p := newPane(w)
	switch s.result.State {
	case app.ChannelBroadcast:
		p.title(theme.Success, "Channel Opening")
		p.line("Funding tx broadcast successfully.")
		p.field("Peer: ", s.attempt.alias)
		p.labelLine("TX ID:")
		p.monoWrap(s.result.Txid)
		p.dim("Channel is pending; it is not active yet.")
	case app.ChannelOutcomeUnknown:
		p.title(theme.Warning, "Channel Open Outcome Unknown")
		p.warnWrapWords("Check pending channels and transaction history before attempting another open. Funding may still complete.")
	default:
		p.title(theme.Warning, "Channel Not Submitted")
	}
	if s.result.Err != nil {
		p.warnWrap(s.result.Err.Error())
	}
	return p.renderWithBottomButtons([]string{"Done"}, 0, s.ctx.ContentFocused, h)
}
