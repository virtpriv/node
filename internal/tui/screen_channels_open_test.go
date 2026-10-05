package tui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"errors"
	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/lndrpc"
	"github.com/virtpriv/node/internal/theme"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestChannelOpenRejectsInvalidTaprootPrivacyToggles(t *testing.T) {
	t.Run("public while Taproot", func(t *testing.T) {
		s := &ChannelOpenScreen{
			private:   true,
			taproot:   true,
			toggleIdx: 0,
		}
		s.handleToggleKey("space")
		if !s.private || s.error == "" {
			t.Fatalf("invalid toggle accepted: private=%v error=%q",
				s.private, s.error)
		}
	})

	t.Run("Taproot while public", func(t *testing.T) {
		s := &ChannelOpenScreen{
			private:   false,
			taproot:   false,
			toggleIdx: 1,
		}
		s.handleToggleKey("space")
		if s.taproot || s.error == "" {
			t.Fatalf("invalid toggle accepted: taproot=%v error=%q",
				s.taproot, s.error)
		}
	})
}

type screenChannelClient struct {
	coins  []lndrpc.UTXO
	sent   []lndrpc.ChannelOpenRequest
	result lndrpc.ChannelOpenResult
}

func (c *screenChannelClient) ConnectPeer(string, string) error                { return nil }
func (c *screenChannelClient) WaitForPeer(string, time.Duration) error         { return nil }
func (c *screenChannelClient) ListUnspent(int32, int32) ([]lndrpc.UTXO, error) { return c.coins, nil }
func (c *screenChannelClient) OpenChannel(req lndrpc.ChannelOpenRequest) lndrpc.ChannelOpenResult {
	c.sent = append(c.sent, req)
	return c.result
}

func channelScreen(t *testing.T) (*ChannelOpenScreen, *screenChannelClient) {
	t.Helper()
	coin := lndrpc.UTXO{Txid: strings.Repeat("a", 64), AmountSats: 50000}
	c := &screenChannelClient{coins: []lndrpc.UTXO{coin}, result: lndrpc.ChannelOpenResult{Submitted: true, FundingTxID: strings.Repeat("c", 64)}}
	s := NewChannelOpenScreen(&ScreenContext{Cfg: config.Default(), State: &RuntimeState{WalletKnown: true, WalletExists: true}})
	s.client = c
	s.peerIdx = len(s.peerList)
	s.customPubkey = "0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"
	s.customHost = "peer.onion:9735"
	s.customAlias = "reviewed peer"
	s.peerConfirmed = true
	publishChannelCoins(s, c.coins, nil)
	t.Cleanup(s.cancelCoinRefresh)
	t.Cleanup(s.ctx.OnChain.reader.Close)
	s.toggleUtxoSelection(0)
	s.returnFromCoinControl(true)
	s.feeInput.SetSats(9)
	return s, c
}

// Intent tests inject observations; the read lifecycle is exercised separately.
func publishChannelCoins(s *ChannelOpenScreen, coins []lndrpc.UTXO, err error) {
	s.refreshCoins()
	s.HandleMsg(channelCoinsMsg{refresh: s.refresh, snapshot: app.OnChainSnapshot{
		Utxos:      app.Observation[[]lndrpc.UTXO]{Value: coins, ObservedAt: time.Now(), Err: err},
		OnChainTxs: freshStatus(s.txs),
	}})
}

func confirmChannel(t *testing.T, s *ChannelOpenScreen) tea.Cmd {
	t.Helper()
	s.prepareChannelOpenConfirmation()
	if s.step != coStepConfirm {
		t.Fatalf("preparation failed: %s", s.error)
	}
	s.confirmBtnIdx = 1
	_, cmd := s.HandleKey("enter", tea.KeyPressMsg{})
	if cmd == nil {
		t.Fatalf("submission not scheduled: %s", s.error)
	}
	return cmd
}

func TestChannelSelectionRefreshAndReview(t *testing.T) {
	s, c := channelScreen(t)
	original := c.coins[0]
	other := lndrpc.UTXO{Txid: strings.Repeat("b", 64), AmountSats: 50000}
	s.prepareChannelOpenConfirmation()
	publishChannelCoins(s, []lndrpc.UTXO{other, original}, nil)
	if !s.selection.Contains(original) || s.selection.Contains(other) {
		t.Fatal("insert/reorder substituted the selected coin")
	}
	publishChannelCoins(s, nil, nil)
	s.confirmBtnIdx = 1
	if _, cmd := s.HandleKey("enter", tea.KeyPressMsg{}); cmd != nil || s.selection.Len() != 1 || len(c.sent) != 0 {
		t.Fatal("missing coin became automatic selection")
	}
	publishChannelCoins(s, []lndrpc.UTXO{other, original}, nil)
	s.selection.Toggle(other)
	if _, cmd := s.HandleKey("enter", tea.KeyPressMsg{}); cmd != nil || s.step != coStepInput {
		t.Fatal("changed selection bypassed renewed review")
	}
	s.clearForm()
	if s.selection.Len() != 0 {
		t.Fatal("Clear could not reset selected identities")
	}
	s.peerConfirmed = true
	s.amountConfirmed = true
	s.prepareChannelOpenConfirmation()
	if s.step == coStepConfirm {
		t.Fatal("empty selection accepted")
	}
}

func TestChannelConfirmationOwnsIntentAndUnknownTotals(t *testing.T) {
	theme.Init(true)
	for _, max := range []bool{false, true} {
		s, c := channelScreen(t)
		s.fundMax = max
		if !max {
			s.amountInput.SetSats(30000)
		} else {
			s.feeInput.Clear()
		}
		s.prepareChannelOpenConfirmation()
		before := s.View(67, 30)
		want := s.attempt.prepared.Request()
		for _, text := range []string{"Total fee and change: unavailable", "no other coins are allowed", "Unconfirmed inputs: allowed"} {
			if !strings.Contains(before, text) {
				t.Fatalf("missing review contract: %s", text)
			}
		}
		if max && (!strings.Contains(before, "may leave change") || !strings.Contains(before, "Fee rate: auto")) {
			t.Fatal("Max or automatic fee contract missing")
		}
		if lipgloss.Width(before) > 67 || len(strings.Split(before, "\n")) > 30 {
			t.Fatal("confirmation exceeds pane")
		}
		s.amountInput.SetSats(49999)
		s.feeInput.Clear()
		s.customPubkey = "edited"
		s.customAlias = "edited"
		s.fundMax = !max
		s.HandleMsg(feeReply(t, &s.fees, s.ctx, 99))
		if s.View(67, 30) != before {
			t.Fatal("late suggestion or mutable form changed reviewed intent")
		}
		s.confirmBtnIdx = 1
		_, cmd := s.HandleKey("enter", tea.KeyPressMsg{})
		if cmd == nil {
			t.Fatal("no submission")
		}
		if _, duplicate := s.HandleKey("enter", tea.KeyPressMsg{}); duplicate != nil {
			t.Fatal("duplicate confirmation scheduled funding")
		}
		s.HandleMsg(cmd())
		if len(c.sent) != 1 || !reflect.DeepEqual(c.sent[0], want) {
			t.Fatal("submission differs from frozen confirmation")
		}
	}
}

func TestChannelRefreshAndResultOwnership(t *testing.T) {
	s, _ := channelScreen(t)
	s.refreshCoins()
	oldRefresh := s.refresh
	s.cancelCoinRefresh()
	s.refreshCoins()
	s.HandleMsg(channelCoinsMsg{refresh: oldRefresh, snapshot: app.OnChainSnapshot{}.Unavailable(errors.New("old error"))})
	if s.utxoErr != nil {
		t.Fatal("older refresh replaced current state")
	}
	publishChannelCoins(s, nil, errors.New("current error"))
	s.prepareChannelOpenConfirmation()
	if s.step == coStepConfirm {
		t.Fatal("failed current fetch allowed preparation")
	}
	publishChannelCoins(s, s.utxos, nil)
	cmd := confirmChannel(t, s)
	msg := cmd()
	current, _ := channelScreen(t)
	currentCmd := confirmChannel(t, current)
	current.HandleMsg(msg)
	if current.step != coStepOpening {
		t.Fatal("old attempt completed another screen")
	}
	m := Model{nav: NewNavSidebar(), screenCtx: current.ctx, activeTab: 1, tabs: []openTab{{Kind: tabOpenChannel, Section: secChannels, Screen: current}}}
	m.nav.ActiveItem = secChannels
	closed, _ := m.closeTab(1)
	if len(closed.(Model).tabs) != 1 {
		t.Fatal("closed a pending channel submission")
	}
	replaced, _ := m.Update(openTabMsg{Kind: tabOpenChannel, Screen: s, Replace: true})
	m = replaced.(Model)
	if len(m.tabs) != 1 || m.tabs[0].Screen != current {
		t.Fatal("replaced a pending channel submission")
	}
	// Hide Channels so completion must reach the retained tab across sections.
	m.nav.ActiveItem = secSystem
	result := currentCmd()
	updated, refresh := m.Update(result)
	m = updated.(Model)
	if current.step != coStepResult || current.result.State != app.ChannelBroadcast || refresh == nil {
		t.Fatal("hidden channel tab lost completion")
	}
	if _, duplicate := m.Update(result); duplicate != nil {
		t.Fatal("duplicate completion repeated refresh")
	}
	current.HandleMsg(channelCoinsMsg{refresh: oldRefresh, snapshot: app.OnChainSnapshot{
		Utxos:      app.Observation[[]lndrpc.UTXO]{Err: errors.New("late failure")},
		OnChainTxs: freshStatus([]lndrpc.OnChainTx{{Txid: "stale"}}),
	}})
	if strings.Contains(current.View(67, 30), "Failed") || current.result.State != app.ChannelBroadcast || len(current.txs) != 0 {
		t.Fatal("late reads overwrote completion")
	}
	_, done := current.HandleKey("enter", tea.KeyPressMsg{})
	// A delayed Done must close its originating screen, even after focus moves.
	other := NewChannelOpenScreen(s.ctx)
	m.tabs = append(m.tabs, openTab{Kind: tabReceive, Section: secWallet, Screen: other})
	m.nav.ActiveItem = secWallet
	updated, _ = m.Update(done())
	m = updated.(Model)
	if len(m.tabs) != 1 || m.tabs[0].Screen != other {
		t.Fatal("Done closed another tab")
	}
	m.tabs = append(m.tabs, openTab{Kind: tabOpenChannel, Section: secChannels, Screen: s})
	updated, _ = m.Update(done())
	if len(updated.(Model).tabs) != 2 {
		t.Fatal("duplicate Done closed a replacement")
	}

}

func TestChannelUnknownOutcomeGuidance(t *testing.T) {
	theme.Init(true)
	s, c := channelScreen(t)
	c.result = lndrpc.ChannelOpenResult{Submitted: true, Err: errors.New("deadline exceeded")}
	s.HandleMsg(confirmChannel(t, s)())
	view := s.View(67, 30)
	if !strings.Contains(view, "Outcome Unknown") || !strings.Contains(view, "Funding may still complete") || strings.Contains(view, "Channel Open Failed") {
		t.Fatal("unknown outcome rendered as definite failure")
	}
	if len(c.sent) != 1 {
		t.Fatal("submission was retried")
	}
}
