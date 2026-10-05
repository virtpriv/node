package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/lndrpc"
)

type statusReadCall struct {
	cfg    config.AppConfig
	wallet bool
	result chan app.StatusSnapshot
}
type controlledStatusReader struct {
	calls chan statusReadCall
	done  <-chan struct{}
}

func (r *controlledStatusReader) Collect(cfg config.AppConfig, wallet bool, _ app.StatusLND) app.StatusSnapshot {
	call := statusReadCall{cfg: cfg, wallet: wallet, result: make(chan app.StatusSnapshot, 1)}
	r.calls <- call
	select {
	case result := <-call.result:
		return result
	case <-r.done:
		return app.StatusSnapshot{}
	}
}
func (*controlledStatusReader) Close() {}
func statusModelFixture(t *testing.T) (Model, *controlledStatusReader) {
	t.Helper()
	cfg := config.Default()
	client := &lndrpc.Client{}
	ctx := &ScreenContext{Cfg: cfg, LndClient: client, State: &RuntimeState{WalletKnown: true, WalletExists: true}}
	r := &controlledStatusReader{calls: make(chan statusReadCall, 4), done: t.Context().Done()}
	return Model{cfg: cfg, state: ctx.State, screenCtx: ctx, lndClient: client, statusCollector: r, nav: NewNavSidebar()}, r
}
func startStatusCommand(t *testing.T, cmd tea.Cmd, r *controlledStatusReader) (statusReadCall, <-chan tea.Msg) {
	t.Helper()
	if cmd == nil {
		t.Fatal("refresh was not scheduled")
	}
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	select {
	case call := <-r.calls:
		return call, result
	case <-time.After(time.Second):
		t.Fatal("collector not reached")
		return statusReadCall{}, nil
	}
}
func statusUpdate(m *Model, msg tea.Msg) tea.Cmd {
	updated, cmd := m.Update(msg)
	*m = updated.(Model)
	return cmd
}
func freshStatus[T any](v T) app.Observation[T] {
	return app.Observation[T]{Value: v, ObservedAt: time.Now()}
}

func TestStatusCoalescesAndRejectsReplayedCompletion(t *testing.T) {
	m, r := statusModelFixture(t)
	first, firstDone := startStatusCommand(t, statusUpdate(&m, refreshStatusMsg{}), r)
	for range 5 {
		if statusUpdate(&m, refreshStatusMsg{}) != nil {
			t.Fatal("overlapping refresh admitted a reader")
		}
	}
	first.result <- app.StatusSnapshot{Balance: freshStatus(lndrpc.WalletBalance{TotalBalance: "10000"})}
	old := <-firstDone
	next := statusUpdate(&m, old)
	second, secondDone := startStatusCommand(t, next, r)
	if statusUpdate(&m, old) != nil || m.screenCtx.Status.Balance.Value.TotalBalance != "10000" {
		t.Fatal("duplicate completion changed publication")
	}
	// A replay must not clear the second read's admission guard.
	if statusUpdate(&m, refreshStatusMsg{}) != nil {
		t.Fatal("old completion cleared the current guard")
	}
	second.result <- app.StatusSnapshot{Balance: freshStatus(lndrpc.WalletBalance{TotalBalance: "20000"})}
	thirdCmd := statusUpdate(&m, <-secondDone)
	third, thirdDone := startStatusCommand(t, thirdCmd, r)
	third.result <- app.StatusSnapshot{Balance: freshStatus(lndrpc.WalletBalance{TotalBalance: "30000"})}
	if statusUpdate(&m, <-thirdDone) != nil {
		t.Fatal("coalesced requests became an unbounded queue")
	}
	statusUpdate(&m, old)
	if m.screenCtx.Status.Balance.Value.TotalBalance != "30000" {
		t.Fatal("older completion replaced current balance")
	}
}

func TestStatusSchedulingSnapshotAndScopeChange(t *testing.T) {
	m, r := statusModelFixture(t)
	cmd := statusUpdate(&m, refreshStatusMsg{})
	m.cfg.SyncthingEnabled = true
	first, done := startStatusCommand(t, cmd, r)
	if first.cfg.SyncthingEnabled {
		t.Fatal("command read mutable configuration after scheduling")
	}
	first.result <- app.StatusSnapshot{Balance: freshStatus(lndrpc.WalletBalance{TotalBalance: "900"})}
	secondCmd := statusUpdate(&m, <-done)
	if m.screenCtx.Status != nil {
		t.Fatal("obsolete configuration result was published")
	}
	second, done := startStatusCommand(t, secondCmd, r)
	if !second.cfg.SyncthingEnabled {
		t.Fatal("follow-up did not use current configuration")
	}
	second.result <- app.StatusSnapshot{Balance: freshStatus(lndrpc.WalletBalance{TotalBalance: "1000"})}
	statusUpdate(&m, <-done)
}

func TestStatusFailureRenderingAndRecovery(t *testing.T) {
	m, r := statusModelFixture(t)
	oc := &OnChainContext{OnChainSnapshot: app.OnChainSnapshot{OnChainTxs: freshStatus([]lndrpc.OnChainTx{{Amount: 10000}, {Amount: 10000}})}}
	onchain := NewOnChainHomeScreen(m.screenCtx, oc)
	channels := NewChannelsHomeScreen(m.screenCtx)
	offchain := NewWalletHomeScreen(m.screenCtx)
	m.screenCtx.PaymentHistory = &paymentHistoryContext{scope: m.screenCtx.walletObservationScope(), PaymentHistorySnapshot: app.PaymentHistorySnapshot{Invoices: freshStatus([]lndrpc.PaymentEntry{{IsIncoming: true, Status: "SETTLED", AmountSats: 1000}}), Payments: freshStatus([]lndrpc.PaymentEntry(nil))}}
	failed := errors.New("injected read failure")
	publish := func(s app.StatusSnapshot) {
		t.Helper()
		call, done := startStatusCommand(t, statusUpdate(&m, refreshStatusMsg{}), r)
		call.result <- s
		statusUpdate(&m, <-done)
	}
	publish(app.StatusSnapshot{Node: freshStatus(lndrpc.NodeInfo{}), Balance: app.Observation[lndrpc.WalletBalance]{Err: failed}, Channels: app.Observation[app.ChannelStatus]{Err: failed}})
	view := ansi.Strip(onchain.View(90, 42))
	if !strings.Contains(view, "unavailable") || strings.Contains(view, "Balance:  0 sats") {
		t.Fatalf("unavailable total became a fabricated balance:\n%s", view)
	}
	if !strings.Contains(view, "N/A") || !strings.Contains(ansi.Strip(offchain.View(90, 42)), "N/A") {
		t.Fatal("history rows fabricated running balances")
	}
	channelView := ansi.Strip(channels.View(90, 42))
	if !strings.Contains(channelView, "Channels unavailable") || strings.Contains(channelView, "No channels yet") {
		t.Fatalf("failed channel read became empty:\n%s", channelView)
	}
	publish(app.StatusSnapshot{Node: freshStatus(lndrpc.NodeInfo{}), Balance: freshStatus(lndrpc.WalletBalance{TotalBalance: "20000"}), Channels: freshStatus(app.ChannelStatus{Channels: []app.StatusChannel{{Channel: lndrpc.Channel{PeerAlias: "retained peer", LocalBalance: 5000}}}})})
	publish(app.StatusSnapshot{Node: freshStatus(lndrpc.NodeInfo{}), Balance: app.Observation[lndrpc.WalletBalance]{Err: failed}, Channels: app.Observation[app.ChannelStatus]{Err: failed}})
	view = ansi.Strip(onchain.View(90, 42))
	if !strings.Contains(view, "20,000 sats (stale)") || !strings.Contains(view, "N/A") {
		t.Fatalf("last-good balance was not explicitly stale:\n%s", view)
	}
	channelView = ansi.Strip(channels.View(90, 42))
	if !strings.Contains(channelView, "Channels stale") || !strings.Contains(channelView, "retained peer") {
		t.Fatalf("last-good channels disappeared or lost stale marker:\n%s", channelView)
	}
	publish(app.StatusSnapshot{Node: freshStatus(lndrpc.NodeInfo{}), Balance: freshStatus(lndrpc.WalletBalance{TotalBalance: "0"}), Channels: freshStatus(app.ChannelStatus{})})
	if !strings.Contains(ansi.Strip(onchain.View(90, 42)), "0 sats") || !strings.Contains(ansi.Strip(channels.View(90, 42)), "No channels yet") || !m.screenCtx.Status.Balance.Fresh() {
		t.Fatal("successful empty observation failed to recover")
	}
}

func TestStatusRetainsWalletDisplayAcrossPresenceFailure(t *testing.T) {
	m, reader := statusModelFixture(t)
	oc := &OnChainContext{OnChainSnapshot: app.OnChainSnapshot{OnChainTxs: freshStatus([]lndrpc.OnChainTx{{Amount: 10000}, {Amount: 10000}})}}
	m.sectionScreens[secOnChain] = NewOnChainHomeScreen(m.screenCtx, oc)
	m.sectionScreens[secWallet] = NewWalletHomeScreen(m.screenCtx)
	m.sectionScreens[secChannels] = NewChannelsHomeScreen(m.screenCtx)
	assertStale := func() {
		t.Helper()
		for _, sec := range []int{secOnChain, secWallet, secChannels} {
			m.nav.ActiveItem = sec
			view := ansi.Strip(m.renderActiveTabContent(67, 36))
			if !strings.Contains(view, "20,000 sats (stale)") || !strings.Contains(view, "Wallet actions are temporarily disabled.") {
				t.Fatalf("section %d hid retained observations or uncertainty:\n%s", sec, view)
			}
			if sec == secOnChain && !strings.Contains(view, "N/A") {
				t.Fatal("stale total produced running balances")
			}
			if sec == secChannels && !strings.Contains(view, "No channels yet. (stale)") {
				t.Fatal("previous empty list was presented as current")
			}
		}
	}
	good := app.StatusSnapshot{Node: freshStatus(lndrpc.NodeInfo{}),
		Balance: freshStatus(lndrpc.WalletBalance{TotalBalance: "20000"}), Channels: freshStatus(app.ChannelStatus{})}
	call, done := startStatusCommand(t, statusUpdate(&m, refreshStatusMsg{}), reader)
	call.result <- good
	statusUpdate(&m, <-done)
	// Schedule through the real timer. Supply asynchronous results below without
	// running helper I/O or waiting for the next timer.
	statusUpdate(&m, tickMsg(time.Now()))
	failedPresence := walletObservationMsg(t, &m, false, errors.New("LND stopped"))
	call, done = startStatusCommand(t, statusUpdate(&m, refreshStatusMsg{}), reader)
	statusUpdate(&m, failedPresence)
	assertStale()
	// A successful read already in flight cannot override unknown presence.
	call.result <- good
	statusUpdate(&m, <-done)
	assertStale()
	statusUpdate(&m, tickMsg(time.Now()))
	call, done = startStatusCommand(t, statusUpdate(&m, refreshStatusMsg{}), reader)
	if !call.wallet {
		t.Fatal("lost presence read stopped same-wallet status queries")
	}
	failed := errors.New("RPC unavailable")
	call.result <- app.StatusSnapshot{Balance: app.Observation[lndrpc.WalletBalance]{Err: failed}, Channels: app.Observation[app.ChannelStatus]{Err: failed}}
	statusUpdate(&m, <-done)
	assertStale()
	statusUpdate(&m, walletObservationMsg(t, &m, true, nil))
	call, done = startStatusCommand(t, statusUpdate(&m, refreshStatusMsg{}), reader)
	call.result <- good
	statusUpdate(&m, <-done)
	statusUpdate(&m, failedPresence)
	for _, sec := range []int{secOnChain, secWallet, secChannels} {
		m.nav.ActiveItem = sec
		view := ansi.Strip(m.renderActiveTabContent(67, 36))
		if strings.Contains(view, "stale") || strings.Contains(view, "disabled") || !strings.Contains(view, "20,000 sats") {
			t.Fatalf("section %d did not recover:\n%s", sec, view)
		}
	}
}

func TestStatusPresenceChangeRejectsPriorWalletCompletion(t *testing.T) {
	m, reader := statusModelFixture(t)
	m.sectionScreens[secOnChain] = NewOnChainHomeScreen(m.screenCtx, &OnChainContext{})
	m.nav.ActiveItem = secOnChain
	good := app.StatusSnapshot{Balance: freshStatus(lndrpc.WalletBalance{TotalBalance: "20000"})}
	call, done := startStatusCommand(t, statusUpdate(&m, refreshStatusMsg{}), reader)
	call.result <- good
	statusUpdate(&m, <-done)
	call, done = startStatusCommand(t, statusUpdate(&m, refreshStatusMsg{}), reader)
	for _, exists := range []bool{false, true} {
		statusUpdate(&m, walletObservationMsg(t, &m, exists, nil))
	}
	statusUpdate(&m, walletObservationMsg(t, &m, false, errors.New("new wallet not observable")))
	if m.screenCtx.Status != nil {
		t.Fatal("confirmed presence change retained the previous wallet snapshot")
	}
	call.result <- good
	next := statusUpdate(&m, <-done)
	call, done = startStatusCommand(t, next, reader)
	call.result <- app.StatusSnapshot{Balance: app.Observation[lndrpc.WalletBalance]{Err: errors.New("unavailable")}}
	statusUpdate(&m, <-done)
	view := ansi.Strip(m.renderActiveTabContent(67, 36))
	if !strings.Contains(view, "Wallet State Unavailable") || strings.Contains(view, "20,000") {
		t.Fatalf("new unknown wallet inherited an old observation:\n%s", view)
	}
}
