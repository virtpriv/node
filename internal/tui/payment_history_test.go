package tui

import (
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/lndrpc"
	"github.com/virtpriv/node/internal/theme"
)

type historyTestReader struct {
	next   app.PaymentHistorySnapshot
	calls  int
	source app.PaymentHistorySource
}

func (r *historyTestReader) Collect(source app.PaymentHistorySource) app.PaymentHistorySnapshot {
	r.calls++
	r.source = source
	return r.next
}
func (*historyTestReader) Close() {}

func historyModel(t *testing.T) (Model, *WalletHomeScreen, *historyTestReader) {
	t.Helper()
	theme.Init(true)
	m, _ := statusModelFixture(t)
	r := &historyTestReader{}
	m.screenCtx.PaymentHistory = &paymentHistoryContext{reader: r, scope: m.screenCtx.walletObservationScope()}
	m.screenCtx.Status = &app.StatusSnapshot{Channels: freshStatus(app.ChannelStatus{}), Node: freshStatus(lndrpc.NodeInfo{})}
	m.state.KeyVerificationKnown = true
	s := NewWalletHomeScreen(m.screenCtx)
	m.sectionScreens[secWallet] = s
	return m, s, r
}
func historySnapshot(invoices, payments []lndrpc.PaymentEntry) app.PaymentHistorySnapshot {
	return app.PaymentHistorySnapshot{Invoices: freshStatus(invoices), Payments: freshStatus(payments)}
}
func publishHistory(t *testing.T, m *Model, snapshot app.PaymentHistorySnapshot) {
	t.Helper()
	m.screenCtx.PaymentHistory.reader.(*historyTestReader).next = snapshot
	cmd := statusUpdate(m, requestPaymentHistoryCmd())
	if cmd == nil {
		t.Fatal("history read not admitted")
	}
	if statusUpdate(m, cmd()) != nil {
		t.Fatal("unexpected history follow-up")
	}
}
func historyView(t *testing.T, s *WalletHomeScreen) string {
	t.Helper()
	view := ansi.Strip(s.View(67, 36))
	if len(strings.Split(view, "\n")) > 36 {
		t.Fatal("history exceeds pane height")
	}
	for _, line := range strings.Split(view, "\n") {
		if ansi.StringWidth(line) > 67 {
			t.Fatalf("history exceeds pane width: %s", view)
		}
	}
	return view
}

func TestHistoryEntryFailurePartialRecoveryAndEmpty(t *testing.T) {
	m, home, r := historyModel(t)
	if view := historyView(t, home); !strings.Contains(view, "Loading...") || strings.Contains(view, "No payments") {
		t.Fatal("unread history appears empty")
	}
	m.nav.Cursor, m.nav.Focused = secWallet, true
	entry := statusUpdate(&m, tea.KeyPressMsg{Code: tea.KeyEnter})
	r.next = app.PaymentHistorySnapshot{}.Unavailable(errors.New("disconnected"))
	read := statusUpdate(&m, entry())
	statusUpdate(&m, read())
	if view := historyView(t, home); !strings.Contains(view, "History unavailable") || strings.Contains(view, "No payments") {
		t.Fatalf("failed first read appears empty: %s", view)
	}
	good := historySnapshot([]lndrpc.PaymentEntry{{Index: 1, IsIncoming: true, Memo: "receipt", Status: "SETTLED", AmountSats: 1200}}, []lndrpc.PaymentEntry{{Index: 1, Memo: "sent", Status: "SUCCEEDED", AmountSats: 500}})
	publishHistory(t, &m, good)
	partial := historySnapshot(nil, nil)
	partial.Payments = app.Observation[[]lndrpc.PaymentEntry]{Err: errors.New("payment outage")}
	publishHistory(t, &m, partial)
	view := historyView(t, home)
	if !strings.Contains(view, "Payments stale") || !strings.Contains(view, "sent") || strings.Contains(view, "receipt") || !strings.Contains(view, "N/A") {
		t.Fatalf("partial read lost retained history or derived balances: %s", view)
	}
	publishHistory(t, &m, historySnapshot(nil, nil))
	if view := historyView(t, home); !strings.Contains(view, "No payments in the recent history window.") || strings.Contains(view, "Retrying") || m.pollInterval() != 60*time.Second {
		t.Fatalf("empty success did not recover: %s", view)
	}
}

func TestHistoryTimerUpdatesMountedDetailAndCaption(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, home, r := historyModel(t)
		m.nav.ActiveItem = secWallet
		invoice := lndrpc.PaymentEntry{Index: 7, IsIncoming: true, Memo: "first", Status: "OPEN", AmountSats: 1000}
		publishHistory(t, &m, historySnapshot([]lndrpc.PaymentEntry{invoice}, nil))
		home.focusZone = walletHomeZoneList
		_, open := home.HandleKey("enter", tea.KeyPressMsg{})
		statusUpdate(&m, open())
		if !strings.Contains(renderDetail(t, &m, 1), "Pending Invoice") {
			t.Fatal("initial detail missing")
		}
		// Keep a dashboard read outstanding to prove list refresh is independent.
		m.statusScope = m.currentStatusScope()
		statusUpdate(&m, requestStatusCmd())
		var tick tea.Msg = tickMsg(time.Now())
		refresh := func(snapshot app.PaymentHistorySnapshot) {
			t.Helper()
			r.next = snapshot
			before := r.calls
			batch := statusUpdate(&m, tick)().(tea.BatchMsg)
			tick = nil
			for _, cmd := range batch {
				switch msg := cmd().(type) {
				case refreshPaymentHistoryMsg:
					read := statusUpdate(&m, msg)
					statusUpdate(&m, read())
				case tickMsg:
					tick = msg
				case refreshStatusMsg, refreshSSHVerificationMsg:
				default:
					t.Fatalf("unexpected timer command %T", msg)
				}
			}
			if r.calls != before+1 || tick == nil {
				t.Fatal("visible timer did not read history or schedule its successor")
			}
		}
		invoice.Status, invoice.Memo, invoice.AmountSats = "SETTLED", "paid", 1200
		good := historySnapshot([]lndrpc.PaymentEntry{invoice}, nil)
		refresh(good)
		if view := renderDetail(t, &m, 1); !strings.Contains(view, "Received Payment") || !strings.Contains(view, "1,200 sats") || m.effectiveTabs()[1].Label != "paid" {
			t.Fatalf("mounted detail did not follow current receipt: %s", view)
		}
		refresh(app.PaymentHistorySnapshot{}.Unavailable(errors.New("offline")))
		if view := renderDetail(t, &m, 1); !strings.Contains(view, "Showing stale data") || !strings.Contains(view, "paid") {
			t.Fatalf("open detail hid history failure: %s", view)
		}
		refresh(historySnapshot(nil, nil))
		if view := renderDetail(t, &m, 1); !strings.Contains(view, "Not in the latest successful list.") || strings.Contains(view, "1,200") || m.effectiveTabs()[1].Label != "Payment" {
			t.Fatal("missing record kept copied data or caption")
		}
		refresh(good)
		if !strings.Contains(renderDetail(t, &m, 1), "Received Payment") {
			t.Fatal("same open detail failed to recover")
		}
		m.nav.ActiveItem = secSystem
		for _, cmd := range statusUpdate(&m, tickMsg(time.Now()))().(tea.BatchMsg) {
			if _, ok := cmd().(refreshPaymentHistoryMsg); ok {
				t.Fatal("hidden Wallet polled history")
			}
		}
	})
}

func TestHistoryCoalescesAndRejectsPreMutationResults(t *testing.T) {
	m, _, r := historyModel(t)
	r.next = historySnapshot([]lndrpc.PaymentEntry{{Index: 1, Memo: "before"}}, nil)
	first := statusUpdate(&m, requestPaymentHistoryCmd())
	for range 5 {
		if statusUpdate(&m, requestPaymentHistoryCmd()) != nil {
			t.Fatal("overlap admitted parallel reads")
		}
	}
	result := first()
	next := statusUpdate(&m, result)
	if next == nil || !m.screenCtx.PaymentHistory.Fresh() {
		t.Fatal("ordinary overlap discarded valid result or lost follow-up")
	}
	if statusUpdate(&m, result) != nil {
		t.Fatal("replay scheduled work")
	}
	if statusUpdate(&m, paymentHistoryChangedCmd()) != nil {
		t.Fatal("mutation bypassed current admission guard")
	}
	if m.screenCtx.PaymentHistory.Fresh() {
		t.Fatal("acknowledgement left old history current")
	}
	r.next = historySnapshot([]lndrpc.PaymentEntry{{Index: 1, Memo: "obsolete"}}, nil)
	next = statusUpdate(&m, next())
	if next == nil || m.screenCtx.PaymentHistory.Fresh() || m.screenCtx.PaymentHistory.Invoices.Value[0].Memo != "before" {
		t.Fatal("pre-mutation read published after acknowledgement")
	}
	r.next = historySnapshot([]lndrpc.PaymentEntry{{Index: 1, Memo: "after"}}, nil)
	if statusUpdate(&m, next()) != nil || r.calls != 3 || !m.screenCtx.PaymentHistory.Fresh() || m.screenCtx.PaymentHistory.Invoices.Value[0].Memo != "after" {
		t.Fatal("current follow-up failed to drain or publish")
	}
}

func TestHistoryScopeRejectsOldResultsAndOpenCommands(t *testing.T) {
	for _, change := range []string{"wallet", "client", "network", "owner"} {
		t.Run(change, func(t *testing.T) {
			m, home, r := historyModel(t)
			m.nav.ActiveItem = secWallet
			good := historySnapshot([]lndrpc.PaymentEntry{{Index: 1, IsIncoming: true, Memo: "old-wallet", Status: "OPEN"}}, nil)
			publishHistory(t, &m, good)
			home.focusZone = walletHomeZoneList
			_, open := home.HandleKey("enter", tea.KeyPressMsg{})
			oldOpen := open()
			statusUpdate(&m, oldOpen)
			inFlight := statusUpdate(&m, requestPaymentHistoryCmd())
			old := inFlight()
			switch change {
			case "wallet":
				m.screenCtx.invalidateWalletObservations()
			case "client":
				m.screenCtx.LndClient = &lndrpc.Client{}
			case "network":
				m.cfg.Network = "signet"
			case "owner":
				m.screenCtx.PaymentHistory = &paymentHistoryContext{reader: r}
			}
			next := statusUpdate(&m, old)
			if m.screenCtx.paymentHistory().Invoices.Known() {
				t.Fatal("old wallet data published")
			}
			if view := renderDetail(t, &m, 1); !strings.Contains(view, "previous wallet session") || strings.Contains(view, "old-wallet") {
				t.Fatalf("old detail crossed scope: %s", view)
			}
			if statusUpdate(&m, old) != nil {
				t.Fatal("old result scheduled more work")
			}
			if change == "owner" {
				next = statusUpdate(&m, requestPaymentHistoryCmd())
			}
			if next == nil {
				t.Fatal("scope change did not recover collection")
			}
			r.next = good
			statusUpdate(&m, next())
			if change == "client" && r.source != m.screenCtx.LndClient {
				t.Fatal("follow-up used old client")
			}
			if !strings.Contains(renderDetail(t, &m, 1), "previous wallet session") {
				t.Fatal("new observation silently retargeted old detail")
			}
			_, open = home.HandleKey("enter", tea.KeyPressMsg{})
			statusUpdate(&m, open())
			if len(m.tabs) != 1 || !strings.Contains(renderDetail(t, &m, 1), "Pending Invoice") {
				t.Fatal("explicit reopen did not rebind current identity")
			}
			statusUpdate(&m, oldOpen)
			if len(m.tabs) != 1 || !strings.Contains(renderDetail(t, &m, 1), "Pending Invoice") {
				t.Fatal("delayed old open displaced the current detail")
			}
		})
	}
}

func TestHistoryPresenceRecovery(t *testing.T) {
	m, home, r := historyModel(t)
	m.nav.ActiveItem = secWallet
	good := historySnapshot([]lndrpc.PaymentEntry{{Index: 1, IsIncoming: true, Memo: "retained", Status: "OPEN"}}, nil)
	publishHistory(t, &m, good)
	home.focusZone = walletHomeZoneList
	_, open := home.HandleKey("enter", tea.KeyPressMsg{})
	statusUpdate(&m, open())
	active := statusUpdate(&m, requestPaymentHistoryCmd())
	statusUpdate(&m, walletObservationMsg(t, &m, false, errors.New("presence unavailable")))
	statusUpdate(&m, active())
	if m.screenCtx.PaymentHistory.Fresh() || !strings.Contains(renderDetail(t, &m, 1), "Showing stale data") {
		t.Fatal("unknown presence accepted a fresh list")
	}
	if statusUpdate(&m, requestPaymentHistoryCmd()) != nil {
		t.Fatal("unknown presence admitted history")
	}
	refresh := statusUpdate(&m, walletObservationMsg(t, &m, true, nil))
	if refresh == nil || !strings.Contains(renderDetail(t, &m, 1), "Showing stale data") || strings.Contains(historyView(t, home), "No payments") {
		t.Fatal("presence recovery erased intermediate error state")
	}
	r.next = good
	read := statusUpdate(&m, refresh())
	statusUpdate(&m, read())
	if !m.screenCtx.PaymentHistory.Fresh() || strings.Contains(renderDetail(t, &m, 1), "stale") {
		t.Fatal("same-wallet history failed to recover in place")
	}

}

func TestHistoryIdentityAcrossReorderAndPaymentStates(t *testing.T) {
	m, home, _ := historyModel(t)
	m.nav.ActiveItem = secWallet
	a := lndrpc.PaymentEntry{Index: 1, IsIncoming: true, Memo: "領収😀é領収😀é領収😀é", Status: "OPEN", CreationDate: 3, PaymentHash: "same"}
	b := lndrpc.PaymentEntry{Index: 1, Memo: "pending", Status: "IN_FLIGHT", CreationDate: 2, PaymentHash: "same"}
	c := lndrpc.PaymentEntry{Index: 2, Memo: "failed", Status: "FAILED", CreationDate: 1, PaymentHash: "same"}
	publishHistory(t, &m, historySnapshot([]lndrpc.PaymentEntry{a}, []lndrpc.PaymentEntry{b, c}))
	home.focusZone = walletHomeZoneList
	for _, cursor := range []int{0, 1, 2, 0} {
		home.cursor = cursor
		_, cmd := home.HandleKey("enter", tea.KeyPressMsg{})
		statusUpdate(&m, cmd())
	}
	if len(m.tabs) != 3 || m.activeTab != 1 {
		t.Fatal("first row, directions or retry indices collided")
	}
	a.CreationDate, b.CreationDate = 1, 3
	publishHistory(t, &m, historySnapshot([]lndrpc.PaymentEntry{a}, []lndrpc.PaymentEntry{b, c}))
	home.cursor = 0
	_, cmd := home.HandleKey("enter", tea.KeyPressMsg{})
	statusUpdate(&m, cmd())
	if len(m.tabs) != 3 || m.activeTab != 2 || !strings.Contains(renderDetail(t, &m, 2), "Pending Payment") || !strings.Contains(renderDetail(t, &m, 3), "Failed Payment") {
		t.Fatal("reordered row selected a different record or invented failure")
	}
	if view := historyView(t, home); !strings.Contains(view, "(in flight)") || !strings.Contains(view, "(failed)") {
		t.Fatalf("list states not distinguished: %s", view)
	}
	b.Status = "FUTURE_STATUS"
	publishHistory(t, &m, historySnapshot([]lndrpc.PaymentEntry{a}, []lndrpc.PaymentEntry{b, c}))
	if !strings.Contains(renderDetail(t, &m, 2), "Payment Status Unknown") {
		t.Fatal("unknown state became pending or failed")
	}
	m.nav.ActiveItem = secSystem
	statusUpdate(&m, cmd())
	if len(m.tabs) != 3 {
		t.Fatal("delayed detail command crossed sections")
	}
}
