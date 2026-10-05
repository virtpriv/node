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
)

var detailTxid = strings.Repeat("a", 64)

func detailSnapshot(confs int32, label string) app.OnChainSnapshot {
	return app.OnChainSnapshot{
		Utxos:      freshStatus([]lndrpc.UTXO{{Txid: detailTxid, Vout: 1, AmountSats: 10000, Confirmations: int64(confs), Address: "test-address"}}),
		OnChainTxs: freshStatus([]lndrpc.OnChainTx{{Txid: detailTxid, Amount: 10000, Confirmations: confs, Label: label, Timestamp: 1789272000}}),
	}
}

func openDetailModel(t *testing.T) (Model, *OnChainHomeScreen, *onChainTestReader) {
	t.Helper()
	m, home, reader := onChainModel(t)
	reader.next = detailSnapshot(0, "first")
	m.nav.Cursor, m.nav.Focused = secOnChain, true
	entry := statusUpdate(&m, tea.KeyPressMsg{Code: tea.KeyEnter})
	read := statusUpdate(&m, entry())
	statusUpdate(&m, read())
	for _, zone := range []int{ocHomeZoneUtxos, ocHomeZoneTxs} {
		home.focusZone = zone
		_, open := home.HandleKey("enter", tea.KeyPressMsg{})
		statusUpdate(&m, open())
	}
	if len(m.tabs) != 2 {
		t.Fatal("real row commands did not open both details")
	}
	return m, home, reader
}

func renderDetail(t *testing.T, m *Model, index int) string {
	t.Helper()
	m.activeTab = index
	if m.activateTab() != nil {
		t.Fatal("detail activation scheduled independent work")
	}
	view := ansi.Strip(m.renderActiveTabContent(67, 36))
	for _, line := range strings.Split(view, "\n") {
		if ansi.StringWidth(line) > 67 {
			t.Fatalf("detail state exceeds pane width: %s", view)
		}
	}
	if len(strings.Split(view, "\n")) > 36 {
		t.Fatal("detail state exceeds pane height")
	}
	return view
}

func TestOpenDetailsFollowAutomaticListObservations(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, home, reader := openDetailModel(t)
		home.ocCtx.Selection.Toggle(home.ocCtx.Utxos.Value[0])
		home.openLabelPopup()
		home.labelInput.SetValue("unsaved draft")
		// Hold a follow-up dashboard read while retaining its scoped snapshot.
		// This exercises list timers without contacting the host helper.
		m.statusScope = m.currentStatusScope()
		statusUpdate(&m, requestStatusCmd())
		var tick tea.Msg = tickMsg(time.Now())
		refresh := func(next app.OnChainSnapshot) {
			t.Helper()
			reader.next = next
			calls := reader.calls
			batch := statusUpdate(&m, tick)().(tea.BatchMsg)
			tick = nil
			for _, command := range batch {
				switch msg := command().(type) {
				case refreshOnChainMsg:
					read := statusUpdate(&m, msg)
					if read == nil {
						t.Fatal("timer did not admit list collection")
					}
					statusUpdate(&m, read())
				case tickMsg:
					tick = msg
				case refreshStatusMsg, refreshSSHVerificationMsg:
				default:
					t.Fatalf("unexpected timer result %T", msg)
				}
			}
			if reader.calls != calls+1 || tick == nil {
				t.Fatal("timer did not read lists or schedule its successor")
			}
		}
		for _, index := range []int{1, 2} {
			if !strings.Contains(renderDetail(t, &m, index), "unconfirmed") {
				t.Fatal("initial observation missing")
			}
		}
		refresh(detailSnapshot(7, "updated"))
		for _, index := range []int{1, 2} {
			view := renderDetail(t, &m, index)
			if strings.Contains(view, "unconfirmed") || !strings.Contains(view, []string{"Confs:     7", "Confs:   7"}[index-1]) {
				t.Fatal("mounted detail kept original confirmations")
			}
		}
		if !strings.Contains(renderDetail(t, &m, 1), "updated") || m.effectiveTabs()[2].Label != "updated" {
			t.Fatal("label remains copied in detail or tab")
		}
		refresh(app.OnChainSnapshot{}.Unavailable(errors.New("read failed")))
		for _, index := range []int{1, 2} {
			view := renderDetail(t, &m, index)
			if !strings.Contains(view, "Showing stale data") || !strings.Contains(view, "10,000") {
				t.Fatal("read failure erased retained detail or hid stale state")
			}
		}
		empty := app.OnChainSnapshot{Utxos: freshStatus([]lndrpc.UTXO(nil)), OnChainTxs: freshStatus([]lndrpc.OnChainTx(nil))}
		refresh(empty)
		for _, index := range []int{1, 2} {
			view := renderDetail(t, &m, index)
			if !strings.Contains(view, "Not in the latest successful list.") || strings.Contains(view, "10,000") || strings.Contains(view, "spent") {
				t.Fatal("disappearance kept copied facts or inferred spending")
			}
		}
		refresh(app.OnChainSnapshot{}.Unavailable(errors.New("read failed after removal")))
		for _, index := range []int{1, 2} {
			view := renderDetail(t, &m, index)
			if !strings.Contains(view, "List unavailable") || strings.Contains(view, "Not in the latest") {
				t.Fatal("failed read claims current absence")
			}
		}
		refresh(detailSnapshot(11, "returned"))
		for _, index := range []int{1, 2} {
			view := renderDetail(t, &m, index)
			if strings.Contains(view, "stale") || !strings.Contains(view, []string{"Confs:     11", "Confs:   11"}[index-1]) || !strings.Contains(view, "10,000") {
				t.Fatal("same identity failed to recover in its open tab")
			}
		}
		if len(m.tabs) != 2 || home.ocCtx.Selection.Len() != 1 || !home.labelEditing || home.labelInput.Value() != "unsaved draft" {
			t.Fatal("refresh replaced tabs, selection or unsaved edit")
		}
	})
}

func TestOpenDetailsRejectPreviousScopeAndDelayedOpens(t *testing.T) {
	for _, change := range []string{"wallet", "client", "network", "owner"} {
		t.Run(change, func(t *testing.T) {
			m, home, _ := openDetailModel(t)
			var delayed []tea.Cmd
			for _, zone := range []int{ocHomeZoneUtxos, ocHomeZoneTxs} {
				home.focusZone = zone
				_, open := home.HandleKey("enter", tea.KeyPressMsg{})
				delayed = append(delayed, open)
			}
			original := []Screen{m.tabs[0].Screen, m.tabs[1].Screen}
			switch change {
			case "wallet":
				statusUpdate(&m, walletObservationMsg(t, &m, false, nil))
			case "client":
				m.screenCtx.LndClient = &lndrpc.Client{}
			case "network":
				m.cfg.Network = "public-signet"
			case "owner":
				m.screenCtx.OnChain = &OnChainContext{}
			}
			for i, command := range delayed {
				if statusUpdate(&m, command()) != nil || m.tabs[i].Screen != original[i] {
					t.Fatal("previous-scope open replaced current tab")
				}
				view := renderDetail(t, &m, i+1)
				if !strings.Contains(view, "previous wallet session") || strings.Contains(view, "10,000") {
					t.Fatal("previous-scope detail appears current")
				}
			}
			if change == "client" {
				publishOnChain(t, &m, detailSnapshot(23, "new scope"))
				if !strings.Contains(renderDetail(t, &m, 1), "previous wallet session") {
					t.Fatal("matching identity silently adopted another scope")
				}
				home.focusZone = ocHomeZoneUtxos
				_, reopen := home.HandleKey("enter", tea.KeyPressMsg{})
				statusUpdate(&m, reopen())
				if len(m.tabs) != 2 || strings.Contains(renderDetail(t, &m, 1), "previous wallet session") {
					t.Fatal("explicit current-scope reopen failed")
				}
			}
		})
	}
}

func TestDetailMetadataHasIndependentFreshness(t *testing.T) {
	m, _, _ := openDetailModel(t)
	known := detailSnapshot(5, "known label")
	publishOnChain(t, &m, known)
	publishOnChain(t, &m, app.OnChainSnapshot{Utxos: known.Utxos, OnChainTxs: app.Observation[[]lndrpc.OnChainTx]{Err: errors.New("history failed")}})
	view := renderDetail(t, &m, 1)
	if !strings.Contains(view, "known label (stale)") || strings.Contains(view, "Showing stale data") {
		t.Fatal("UTXO and transaction freshness were conflated")
	}
	publishOnChain(t, &m, app.OnChainSnapshot{Utxos: known.Utxos, OnChainTxs: freshStatus([]lndrpc.OnChainTx(nil))})
	view = renderDetail(t, &m, 1)
	if !strings.Contains(view, "Date:      unavailable") || !strings.Contains(view, "Label:     unavailable") {
		t.Fatal("missing metadata became an authoritative date or empty label")
	}
	known.OnChainTxs.Value[0].TxType = "channel_close"
	publishOnChain(t, &m, known)
	pending := func(blocks int32) app.Observation[app.ChannelStatus] {
		return freshStatus(app.ChannelStatus{Pending: lndrpc.PendingChannelInfo{PendingForceCloseChannels: []lndrpc.PendingForceCloseChannel{{ClosingTxid: detailTxid, BlocksRemaining: blocks}}}})
	}
	m.screenCtx.Status.Channels = pending(12)
	if !strings.Contains(renderDetail(t, &m, 2), "~12 blocks remaining") {
		t.Fatal("current lock metadata not rendered")
	}
	m.screenCtx.Status.Channels = pending(3)
	if !strings.Contains(renderDetail(t, &m, 2), "~3 blocks remaining") {
		t.Fatal("lock countdown stayed copied")
	}
	m.screenCtx.Status.Channels.Err = errors.New("pending channels failed")
	view = renderDetail(t, &m, 2)
	if !strings.Contains(view, "Locked:  unavailable") || strings.Contains(view, "blocks remaining") {
		t.Fatal("stale lock metadata appears current")
	}
	m.screenCtx.Status.Channels = pending(2)
	if !strings.Contains(renderDetail(t, &m, 2), "~2 blocks remaining") {
		t.Fatal("lock metadata did not recover")
	}
}
