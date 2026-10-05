package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/lndrpc"
	"github.com/virtpriv/node/internal/theme"
)

type screenOnChainClient struct {
	coins []lndrpc.UTXO
	sent  []lndrpc.SendCoinsRequest
	err   error
}

func (c *screenOnChainClient) ListUnspent(int32, int32) ([]lndrpc.UTXO, error) { return c.coins, nil }
func (c *screenOnChainClient) SendCoins(req lndrpc.SendCoinsRequest) (*lndrpc.SendCoinsResult, error) {
	c.sent = append(c.sent, req)
	return &lndrpc.SendCoinsResult{Txid: "broadcast"}, c.err
}

func onChainScreen(t *testing.T) (*OnChainSendScreen, *screenOnChainClient) {
	t.Helper()
	coin := lndrpc.UTXO{Txid: strings.Repeat("a", 64), AmountSats: 10000}
	client := &screenOnChainClient{coins: []lndrpc.UTXO{coin}}
	ocCtx := &OnChainContext{OnChainSnapshot: app.OnChainSnapshot{Utxos: freshStatus(client.coins)}, reader: &onChainTestReader{}}
	ocCtx.Selection.Toggle(coin)
	s := NewOnChainSendScreen(&ScreenContext{Cfg: config.Default(), State: &RuntimeState{WalletKnown: true, WalletExists: true}}, ocCtx)
	s.ctx.OnChain = ocCtx
	ocCtx.scope = s.ctx.walletObservationScope()
	s.client = client
	addr, err := btcutil.NewAddressWitnessPubKeyHash(make([]byte, 20), &chaincfg.MainNetParams)
	if err != nil {
		t.Fatal(err)
	}
	s.addrInput.SetValue(addr.EncodeAddress())
	s.amtInput.SetSats(1000)
	s.feeInput.SetSats(9)
	s.labelInput.SetValue("reviewed")
	s.step = ocStepButtons
	return s, client
}

func confirmOnChain(t *testing.T, s *OnChainSendScreen) tea.Cmd {
	t.Helper()
	s.validateAndConfirm()
	if s.step != ocStepConfirm {
		t.Fatalf("preparation failed: %s", s.error)
	}
	s.confirmBtnIdx = 1
	_, cmd := s.HandleKey("enter", tea.KeyPressMsg{})
	if cmd == nil {
		t.Fatalf("submission not scheduled: %s", s.error)
	}
	return cmd
}

func TestOnChainReviewAndSubmitStayTogether(t *testing.T) {
	theme.Init(true)
	s, client := onChainScreen(t)
	s.validateAndConfirm()
	reviewed := s.View(82, 34)
	s.addrInput.SetValue("changed")
	s.amtInput.SetSats(9999)
	s.feeInput.Clear()
	s.labelInput.SetValue("changed")
	s.sendAll = true
	s.HandleMsg(feeReply(t, &s.fees, s.ctx, 100))
	if s.View(82, 34) != reviewed {
		t.Fatal("form or late fee suggestion changed confirmation")
	}
	s.confirmBtnIdx = 1
	_, cmd := s.HandleKey("enter", tea.KeyPressMsg{})
	if cmd == nil {
		t.Fatal("no submission")
	}
	if _, duplicate := s.HandleKey("enter", tea.KeyPressMsg{}); duplicate != nil {
		t.Fatal("duplicate submission scheduled")
	}
	msg := cmd()
	s.HandleMsg(msg)
	if len(client.sent) != 1 || client.sent[0].Address == "changed" || client.sent[0].AmountSats != 1000 ||
		client.sent[0].SatPerVbyte != 9 || client.sent[0].SendAll || client.sent[0].Label != "reviewed" {
		t.Fatalf("submitted edited form: %+v", client.sent)
	}
	if _, duplicate := s.HandleMsg(msg); duplicate != nil {
		t.Fatal("duplicate result repeated completion work")
	}
}

func TestOnChainSelectionRefreshAndRenewedConfirmation(t *testing.T) {
	s, client := onChainScreen(t)
	original := client.coins[0]
	other := lndrpc.UTXO{Txid: strings.Repeat("b", 64), AmountSats: 20000}
	s.validateAndConfirm()
	m := Model{screenCtx: s.ctx}
	publishOnChain(t, &m, app.OnChainSnapshot{Utxos: freshStatus([]lndrpc.UTXO{other, original})})
	if !s.ocCtx.Selection.Contains(original) || s.ocCtx.Selection.Contains(other) {
		t.Fatal("refresh substituted selected coin")
	}
	s.ocCtx.Selection.Toggle(other)
	s.confirmBtnIdx = 1
	if _, cmd := s.HandleKey("enter", tea.KeyPressMsg{}); cmd != nil || s.step != ocStepButtons {
		t.Fatal("selection edit bypassed review")
	}
	s.validateAndConfirm()
	publishOnChain(t, &m, app.OnChainSnapshot{Utxos: freshStatus([]lndrpc.UTXO(nil))})
	s.confirmBtnIdx = 1
	_, cmd := s.HandleKey("enter", tea.KeyPressMsg{})
	client.coins = nil
	s.HandleMsg(cmd())
	if s.result.State != app.OnChainNotSubmitted || len(client.sent) != 0 || s.ocCtx.Selection.Len() != 2 {
		t.Fatal("missing selected coins became an automatic send")
	}
	s.step = ocStepButtons
	s.sendBtnIdx = 0
	s.HandleKey("enter", tea.KeyPressMsg{})
	if s.ocCtx.Selection.Len() != 0 {
		t.Fatal("Clear cannot recover unavailable selection")
	}
}

func TestOnChainResultsRespectAttemptAndTabOwnership(t *testing.T) {
	s, client := onChainScreen(t)
	oldCommand := confirmOnChain(t, s)
	oldMsg := oldCommand()
	s.HandleMsg(oldMsg)
	current, _ := onChainScreen(t)
	currentCommand := confirmOnChain(t, current)
	if _, cmd := current.HandleMsg(oldMsg); cmd != nil || current.step != ocStepBroadcast {
		t.Fatal("old result reached replacement screen")
	}
	m := Model{nav: NewNavSidebar(), screenCtx: current.ctx,
		tabs: []openTab{{Kind: tabOnChain, Section: secOnChain, Screen: current}}}
	m.nav.ActiveItem = secOnChain
	m.activeTab = 1
	closed, _ := m.closeTab(1)
	if len(closed.(Model).tabs) != 1 {
		t.Fatal("closed a pending broadcast")
	}
	replaced, _ := m.Update(openTabMsg{Kind: tabOnChain, Screen: s, Replace: true})
	m = replaced.(Model)
	if m.tabs[0].Screen != current {
		t.Fatal("replaced a pending broadcast")
	}
	m.nav.ActiveItem = secSystem
	added := lndrpc.UTXO{Txid: strings.Repeat("b", 64), AmountSats: 20000}
	current.ocCtx.Selection.Toggle(added)
	msg := currentCommand()
	updated, cmd := m.Update(msg)
	m = updated.(Model)
	if current.step != ocStepResult || current.result.State != app.OnChainBroadcast || cmd == nil {
		t.Fatal("hidden on-chain tab lost its result")
	}
	listReads, statusRequests := 0, 0
	for _, command := range cmd().(tea.BatchMsg) {
		switch message := command().(type) {
		case refreshOnChainMsg:
			read := statusUpdate(&m, message)
			if read == nil {
				t.Fatal("send completion did not reach list admission")
			}
			statusUpdate(&m, read())
			listReads++
		case refreshStatusMsg:
			statusRequests++
		default:
			t.Fatalf("unexpected send-completion command %T", message)
		}
	}
	if listReads != 1 || statusRequests != 1 {
		t.Fatal("send completion omitted a refresh")
	}
	if _, duplicate := m.Update(msg); duplicate != nil {
		t.Fatal("duplicate result triggered wallet refresh again")
	}
	if !current.ocCtx.Selection.Contains(added) {
		t.Fatal("completion cleared a newer selection")
	}
	if len(client.sent) != 1 {
		t.Fatal("old send retried")
	}
	m.nav.ActiveItem = secOnChain
	closed, _ = m.closeTab(1)
	if len(closed.(Model).tabs) != 0 {
		t.Fatal("completed send cannot be closed")
	}
}

func TestOnChainDoneCannotCloseAnotherTab(t *testing.T) {
	s, _ := onChainScreen(t)
	s.HandleMsg(confirmOnChain(t, s)())
	_, done := s.HandleKey("enter", tea.KeyPressMsg{})
	other, _ := onChainScreen(t)
	m := Model{nav: NewNavSidebar(), screenCtx: s.ctx, activeTab: 1,
		tabs: []openTab{
			{Kind: tabOnChain, Section: secOnChain, Screen: s},
			{Kind: tabReceive, Section: secWallet, Screen: other},
		}}
	m.nav.ActiveItem = secWallet
	updated, _ := m.Update(done())
	m = updated.(Model)
	if len(m.tabs) != 1 || m.tabs[0].Screen != other {
		t.Fatal("delayed Done closed the newly active tab")
	}
	m.tabs = append(m.tabs, openTab{Kind: tabOnChain, Section: secOnChain, Screen: other})
	updated, _ = m.Update(done())
	if len(updated.(Model).tabs) != 2 {
		t.Fatal("duplicate Done closed a replacement tab")
	}
}

func TestOnChainMaxAndUnknownOutcomePresentation(t *testing.T) {
	theme.Init(true)
	s, client := onChainScreen(t)
	s.applyMax()
	s.validateAndConfirm()
	view := s.View(67, 30)
	if len(strings.Split(view, "\n")) > 30 || lipgloss.Width(view) > 67 {
		t.Fatal("review exceeds the current content pane")
	}
	for _, text := range []string{"net amount determined by LND", "Total fee: unavailable", "reserve change", "Unconfirmed inputs: allowed"} {
		if !strings.Contains(view, text) {
			t.Fatalf("missing Max contract: %s", text)
		}
	}
	if strings.Contains(view, "10,000") {
		t.Fatal("gross selection presented as net Max amount")
	}
	s.confirmBtnIdx = 1
	_, cmd := s.HandleKey("enter", tea.KeyPressMsg{})
	client.err = errors.New("connection lost")
	s.HandleMsg(cmd())
	view = s.View(82, 34)
	if !strings.Contains(view, "Broadcast Outcome Unknown") || strings.Contains(view, "Send Failed") || strings.Contains(view, "Transaction Broadcast") {
		t.Fatal("transport error presented as a definite outcome")
	}
	if len(client.sent) != 1 || !client.sent[0].SendAll || client.sent[0].AmountSats != 0 {
		t.Fatal("Max submitted a guessed amount")
	}
	if s.ocCtx.Selection.Len() != 1 {
		t.Fatal("uncertain send cleared selection")
	}
}
