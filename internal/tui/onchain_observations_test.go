package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/lndrpc"
	"github.com/virtpriv/node/internal/theme"
)

type onChainTestReader struct {
	next   app.OnChainSnapshot
	calls  int
	source app.OnChainSource
}

func (r *onChainTestReader) Collect(source app.OnChainSource) app.OnChainSnapshot {
	r.calls++
	r.source = source
	return r.next
}
func (r *onChainTestReader) ReadUnspent(_ context.Context, source app.OnChainSource, _, _ int32) app.Observation[[]lndrpc.UTXO] {
	return r.Collect(source).Utxos
}
func (r *onChainTestReader) ReadTransactions(_ context.Context, source app.OnChainSource) app.Observation[[]lndrpc.OnChainTx] {
	return r.Collect(source).OnChainTxs
}
func (*onChainTestReader) Close() {}

func onChainModel(t *testing.T) (Model, *OnChainHomeScreen, *onChainTestReader) {
	t.Helper()
	theme.Init(true)
	m, _ := statusModelFixture(t)
	r := &onChainTestReader{}
	oc := &OnChainContext{reader: r, scope: m.screenCtx.walletObservationScope()}
	m.screenCtx.OnChain = oc
	m.screenCtx.Status = &app.StatusSnapshot{Balance: freshStatus(lndrpc.WalletBalance{TotalBalance: "20000"}), Node: freshStatus(lndrpc.NodeInfo{})}
	m.state.KeyVerificationKnown = true
	s := NewOnChainHomeScreen(m.screenCtx, oc)
	m.sectionScreens[secOnChain] = s
	return m, s, r
}

func publishOnChain(t *testing.T, m *Model, snapshot app.OnChainSnapshot) {
	t.Helper()
	r := m.screenCtx.OnChain.reader.(*onChainTestReader)
	r.next = snapshot
	cmd := statusUpdate(m, requestOnChainCmd())
	if cmd == nil {
		t.Fatal("list read was not admitted")
	}
	if statusUpdate(m, cmd()) != nil {
		t.Fatal("unexpected follow-up")
	}
}

func TestOnChainEntryFailurePartialRecoveryAndEmpty(t *testing.T) {
	m, s, r := onChainModel(t)
	view := func() string {
		rendered := ansi.Strip(s.View(67, 36))
		if len(strings.Split(rendered, "\n")) > 36 {
			t.Fatal("list state exceeds pane height")
		}
		for _, line := range strings.Split(rendered, "\n") {
			if ansi.StringWidth(line) > 67 {
				t.Fatal("list state exceeds pane width")
			}
		}
		return rendered
	}
	if !strings.Contains(view(), "Loading...") || strings.Contains(view(), "(0 UTXOs)") {
		t.Fatal("initial read appears empty")
	}
	m.nav.Cursor, m.nav.Focused = secOnChain, true
	entry := statusUpdate(&m, tea.KeyPressMsg{Code: tea.KeyEnter})
	r.next = app.OnChainSnapshot{}.Unavailable(errors.New("disconnected"))
	read := statusUpdate(&m, entry())
	statusUpdate(&m, read())
	if strings.Count(view(), "Unavailable. Retrying...") != 2 || strings.Contains(view(), "No UTXOs") || strings.Contains(view(), "No on-chain") {
		t.Fatalf("failed first read became empty: %s", view())
	}
	coin := lndrpc.UTXO{Txid: "coin", AmountSats: 20000, Address: "retained-address"}
	good := app.OnChainSnapshot{Utxos: freshStatus([]lndrpc.UTXO{coin}), OnChainTxs: freshStatus([]lndrpc.OnChainTx{{Txid: "coin", Label: "retained-tx", Amount: 20000}})}
	publishOnChain(t, &m, good)
	s.ocCtx.Selection.Toggle(coin)
	failed := errors.New("history unavailable")
	publishOnChain(t, &m, app.OnChainSnapshot{Utxos: freshStatus([]lndrpc.UTXO(nil)), OnChainTxs: app.Observation[[]lndrpc.OnChainTx]{Err: failed}})
	if !strings.Contains(view(), "No UTXOs found.") || !strings.Contains(view(), "Transactions (stale, retrying)") || !strings.Contains(view(), "retained-tx") || s.computeTxBalances() != nil || s.ocCtx.Selection.Len() != 1 {
		t.Fatalf("partial failure lost state or invented current history: %s", view())
	}
	publishOnChain(t, &m, app.OnChainSnapshot{}.Unavailable(failed))
	if strings.Contains(view(), "No UTXOs found.") || !strings.Contains(view(), "0 UTXOs, stale") {
		t.Fatal("previous empty observation appears current")
	}
	publishOnChain(t, &m, app.OnChainSnapshot{Utxos: freshStatus([]lndrpc.UTXO(nil)), OnChainTxs: freshStatus([]lndrpc.OnChainTx(nil))})
	if !strings.Contains(view(), "No on-chain transactions.") || strings.Contains(view(), "retrying") || m.pollInterval() != 60*time.Second {
		t.Fatal("empty success did not recover in place")
	}
}

func TestOnChainCoalescingLifecycleAndClientScope(t *testing.T) {
	for _, change := range []string{"wallet", "client", "network", "owner"} {
		t.Run(change, func(t *testing.T) {
			m, _, r := onChainModel(t)
			r.next = app.OnChainSnapshot{Utxos: freshStatus([]lndrpc.UTXO{{Txid: "old"}}), OnChainTxs: freshStatus([]lndrpc.OnChainTx{{Txid: "old"}})}
			cmd := statusUpdate(&m, requestOnChainCmd())
			for range 5 {
				if statusUpdate(&m, requestOnChainCmd()) != nil {
					t.Fatal("concurrent reader admitted")
				}
			}
			old := cmd()
			switch change {
			case "wallet":
				statusUpdate(&m, walletObservationMsg(t, &m, false, nil))
			case "client":
				m.screenCtx.LndClient = &lndrpc.Client{}
			case "network":
				m.cfg.Network = "signet"
			case "owner":
				m.screenCtx.OnChain = &OnChainContext{reader: r}
			}
			next := statusUpdate(&m, old)
			if m.screenCtx.OnChain.Utxos.Known() || m.screenCtx.OnChain.OnChainTxs.Known() {
				t.Fatal("obsolete wallet observation published")
			}
			if statusUpdate(&m, old) != nil {
				t.Fatal("replayed completion scheduled work")
			}
			if change == "wallet" || change == "owner" {
				if next != nil {
					t.Fatal("absent wallet or foreign owner scheduled work")
				}
				return
			}
			r.next = app.OnChainSnapshot{Utxos: freshStatus([]lndrpc.UTXO(nil)), OnChainTxs: freshStatus([]lndrpc.OnChainTx(nil))}
			if next == nil {
				t.Fatal("scope change lost coalesced follow-up")
			}
			if statusUpdate(&m, next()) != nil || r.calls != 2 || !m.screenCtx.OnChain.Utxos.Fresh() {
				t.Fatal("follow-up did not finish once")
			}
			if change == "client" && r.source != m.screenCtx.LndClient {
				t.Fatal("follow-up used former client")
			}
		})
	}
}

func TestOnChainCoalescesWithoutDiscardingCurrentRead(t *testing.T) {
	m, _, r := onChainModel(t)
	r.next = app.OnChainSnapshot{Utxos: freshStatus([]lndrpc.UTXO{{Txid: "current"}}), OnChainTxs: freshStatus([]lndrpc.OnChainTx(nil))}
	first := statusUpdate(&m, requestOnChainCmd())
	for range 5 {
		if statusUpdate(&m, requestOnChainCmd()) != nil {
			t.Fatal("overlap admitted another reader")
		}
	}
	reply := first()
	next := statusUpdate(&m, reply)
	if !m.screenCtx.OnChain.Utxos.Fresh() || m.screenCtx.OnChain.Utxos.Value[0].Txid != "current" || next == nil {
		t.Fatal("overlap discarded a valid observation or lost the follow-up")
	}
	if statusUpdate(&m, reply) != nil {
		t.Fatal("duplicate result bypassed admission")
	}
	r.next = app.OnChainSnapshot{}.Unavailable(errors.New("retry failed"))
	if statusUpdate(&m, next()) != nil || r.calls != 2 || m.screenCtx.OnChain.Utxos.Fresh() || len(m.screenCtx.OnChain.Utxos.Value) != 1 {
		t.Fatal("coalesced follow-up did not retain stale state and drain")
	}
}

func TestOnChainPresenceFailurePreservesSelectionAndRejectsFreshness(t *testing.T) {
	m, s, r := onChainModel(t)
	m.nav.ActiveItem = secOnChain
	coin := lndrpc.UTXO{Txid: "retained", Address: "retained-address"}
	good := app.OnChainSnapshot{Utxos: freshStatus([]lndrpc.UTXO{coin}), OnChainTxs: freshStatus([]lndrpc.OnChainTx(nil))}
	publishOnChain(t, &m, good)
	s.ocCtx.Selection.Toggle(coin)
	inFlight := statusUpdate(&m, requestOnChainCmd())
	statusUpdate(&m, walletObservationMsg(t, &m, false, errors.New("presence unavailable")))
	r.next = good
	statusUpdate(&m, inFlight())
	if s.ocCtx.Utxos.Fresh() || !s.ocCtx.Utxos.Known() || !s.ocCtx.Selection.Contains(coin) {
		t.Fatal("unknown presence erased wallet intent or admitted fresh state")
	}
	if cmd := statusUpdate(&m, requestOnChainCmd()); cmd != nil {
		t.Fatal("unknown wallet admitted another list read")
	}
	refresh := statusUpdate(&m, walletObservationMsg(t, &m, true, nil))
	if refresh == nil {
		t.Fatal("presence recovery did not request visible lists")
	}
	cmd := statusUpdate(&m, refresh())
	statusUpdate(&m, cmd())
	if !s.ocCtx.Utxos.Fresh() || !s.ocCtx.Selection.Contains(coin) {
		t.Fatal("same-wallet recovery lost selection or freshness")
	}
}

func TestOnChainDetailIdentitySurvivesReordering(t *testing.T) {
	m, s, _ := onChainModel(t)
	m.nav.ActiveItem = secOnChain
	a, b := lndrpc.UTXO{Txid: "record-A", Address: "address-A"}, lndrpc.UTXO{Txid: "record-B", Address: "address-B"}
	publish := func(coins []lndrpc.UTXO, txs []lndrpc.OnChainTx) {
		publishOnChain(t, &m, app.OnChainSnapshot{Utxos: freshStatus(coins), OnChainTxs: freshStatus(txs)})
	}
	publish([]lndrpc.UTXO{a, b}, []lndrpc.OnChainTx{{Txid: "record-A", Label: "transaction-A"}, {Txid: "record-B", Label: "transaction-B"}})
	for _, zone := range []int{ocHomeZoneUtxos, ocHomeZoneTxs} {
		s.focusZone, s.utxoCursor, s.txCursor = zone, 1, 1
		_, open := s.HandleKey("enter", tea.KeyPressMsg{})
		statusUpdate(&m, open())
	}
	publish([]lndrpc.UTXO{b, a}, []lndrpc.OnChainTx{{Txid: "record-B", Label: "transaction-B"}, {Txid: "record-A", Label: "transaction-A"}})
	for _, zone := range []int{ocHomeZoneUtxos, ocHomeZoneTxs} {
		s.focusZone = zone
		_, open := s.HandleKey("enter", tea.KeyPressMsg{})
		statusUpdate(&m, open())
	}
	if len(m.tabs) != 4 {
		t.Fatal("row index reused a different detail")
	}
	for i, want := range []string{"address-B", "record-B", "address-A", "record-A"} {
		if view := renderDetail(t, &m, i+1); !strings.Contains(view, want) {
			t.Fatalf("detail %d: expected %q in %s", i+1, want, view)
		}
	}
	s.focusZone, s.utxoCursor = ocHomeZoneUtxos, 0
	_, open := s.HandleKey("enter", tea.KeyPressMsg{})
	statusUpdate(&m, open())
	if len(m.tabs) != 4 || m.effectiveTabs()[m.activeTab].Key != "record-B:0" {
		t.Fatal("first-row reopen did not select existing identity")
	}
	m.nav.ActiveItem = secSystem
	statusUpdate(&m, open())
	if len(m.tabs) != 4 {
		t.Fatal("delayed detail opened in another section")
	}
}
