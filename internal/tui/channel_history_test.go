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

type channelHistoryTestReader struct {
	next   app.ChannelHistorySnapshot
	calls  int
	source app.ChannelHistorySource
}

func (r *channelHistoryTestReader) Collect(source app.ChannelHistorySource) app.ChannelHistorySnapshot {
	r.source = source
	r.calls++
	return r.next
}
func (*channelHistoryTestReader) Close() {}

func channelHistoryModel(t *testing.T) (Model, *ChannelsHomeScreen, *channelHistoryTestReader) {
	t.Helper()
	m, _ := statusModelFixture(t)
	r := &channelHistoryTestReader{}
	m.screenCtx.ChannelHistory = &channelHistoryContext{reader: r}
	home := NewChannelsHomeScreen(m.screenCtx)
	m.sectionScreens[secChannels] = home
	m.nav.ActiveItem = secChannels
	return m, home, r
}

func mountChannelHistory(t *testing.T, m *Model, home *ChannelsHomeScreen) (*ChannelHistoryScreen, tea.Cmd, tea.Cmd) {
	t.Helper()
	_, open := home.openHistory()
	msg, ok := open().(openTabMsg)
	if !ok {
		t.Fatal("history read can run before the owning tab is mounted")
	}
	init := statusUpdate(m, msg)
	if init == nil || len(m.tabs) == 0 {
		t.Fatal("mount did not schedule initial observations")
	}
	var read, status tea.Cmd
	for _, cmd := range init().(tea.BatchMsg) {
		switch msg := cmd().(type) {
		case refreshChannelHistoryMsg:
			read = statusUpdate(m, msg)
		case refreshStatusMsg:
			// Admit the real status command without contacting the helper.
			status = statusUpdate(m, msg)
		default:
			t.Fatalf("unexpected initial command %T", msg)
		}
	}
	if read == nil {
		t.Fatal("mount did not request closed channels")
	}
	return msg.Screen.(*ChannelHistoryScreen), read, status
}

func TestChannelHistoryMountHiddenCompletionAndCoalescing(t *testing.T) {
	m, home, reader := channelHistoryModel(t)
	history, first, _ := mountChannelHistory(t, &m, home)
	for range 3 {
		if statusUpdate(&m, requestChannelHistoryCmd()) != nil {
			t.Fatal("overlapping requests scheduled concurrent collection")
		}
	}
	m.nav.ActiveItem = secWallet
	reader.next.Closed = freshStatus([]lndrpc.ClosedChannel{{ChannelPoint: "old:0", PeerAlias: "first closed"}})
	result := first()
	next := statusUpdate(&m, result)
	if next == nil || !strings.Contains(history.View(67, 30), "first closed") {
		t.Fatal("hidden completion was dropped or coalesced refresh lost")
	}
	reader.next.Closed = freshStatus([]lndrpc.ClosedChannel{{ChannelPoint: "new:0", PeerAlias: "latest closed"}})
	if statusUpdate(&m, result) != nil {
		t.Fatal("replayed result affected the follow-up read")
	}
	if statusUpdate(&m, next()) != nil || reader.calls != 2 {
		t.Fatal("coalescing created more than one follow-up")
	}
	statusUpdate(&m, result)
	if view := history.View(67, 30); !strings.Contains(view, "latest closed") || strings.Contains(view, "first closed") {
		t.Fatal("obsolete completion replaced newer history")
	}
}

func TestMountedChannelViewsFollowTimersFailureAndRecovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		theme.Init(true)
		m, home, reader := channelHistoryModel(t)
		statusReader := m.statusCollector.(*controlledStatusReader)
		channel := channelInfo{Channel: lndrpc.Channel{ChannelPoint: strings.Repeat("a", 64) + ":0", PeerAlias: "original peer", Capacity: 30000, LocalBalance: 10000, RemoteBalance: 20000, Active: true}}
		good := app.StatusSnapshot{Channels: freshStatus(app.ChannelStatus{Channels: []channelInfo{channel}})}
		call, done := startStatusCommand(t, statusUpdate(&m, requestStatusCmd()), statusReader)
		call.result <- good
		statusUpdate(&m, <-done)
		home.focusZone = chanHomeZoneList
		_, open := home.handleEnter()
		statusUpdate(&m, open())
		detail := m.tabs[0].Screen.(*ChannelDetailScreen)
		history, read, initialStatus := mountChannelHistory(t, &m, home)
		reader.next.Closed.Err = errors.New("closed read failed")
		statusUpdate(&m, read())
		if view := history.View(67, 30); !strings.Contains(view, "Closed channels unavailable") || strings.Contains(view, "No channel history") || !strings.Contains(view, "original peer") {
			t.Fatal("cold failure hid current channels or claimed empty history")
		}
		m.activeTab = 1
		call, done = startStatusCommand(t, initialStatus, statusReader)
		var tick tea.Msg = tickMsg(time.Now())
		refresh := func(status app.StatusSnapshot, closed app.Observation[[]lndrpc.ClosedChannel]) {
			t.Helper()
			reader.next.Closed = closed
			// Keep a status read pending; leave the independent SSH trigger unhandled.
			// Complete its real request, then hold the coalesced successor.
			batch := statusUpdate(&m, tick)().(tea.BatchMsg)
			tick = nil
			for _, cmd := range batch {
				switch msg := cmd().(type) {
				case refreshChannelHistoryMsg:
					read := statusUpdate(&m, msg)
					if read == nil {
						t.Fatal("timer omitted history collection")
					}
					statusUpdate(&m, read())
				case refreshStatusMsg:
					statusUpdate(&m, msg)
				case refreshSSHVerificationMsg:
				case tickMsg:
					tick = msg
				default:
					t.Fatalf("unexpected timer command %T", msg)
				}
			}
			if tick == nil {
				t.Fatal("timer omitted its successor")
			}
			call.result <- status
			next := statusUpdate(&m, <-done)
			call, done = startStatusCommand(t, next, statusReader)
		}
		closed := freshStatus([]lndrpc.ClosedChannel{{ChannelPoint: "closed:0", PeerAlias: "closed peer"}})
		channel.PeerAlias, channel.LocalBalance = "updated peer", 15000
		good.Channels = freshStatus(app.ChannelStatus{Channels: []channelInfo{channel}})
		refresh(good, closed)
		view := ansi.Strip(m.renderActiveTabContent(67, 36))
		if !strings.Contains(view, "15,000 sats") || strings.Contains(view, "10,000 sats") || m.effectiveTabs()[1].Label != "updated peer" || !strings.Contains(history.View(67, 30), "updated peer") {
			t.Fatal("timer publication left a copied detail, caption or history row")
		}
		detail.launchClose()
		reviewClose(t, detail.closeScreen, true, 0)
		review := detail.View(67, 36)
		channel.PeerAlias, channel.LocalBalance = "changed during review", 17000
		refresh(app.StatusSnapshot{Channels: freshStatus(app.ChannelStatus{Channels: []channelInfo{channel}})}, closed)
		if detail.View(67, 36) != review || m.effectiveTabs()[1].Label == "updated peer" {
			t.Fatal("changed channel observation was not published or altered reviewed intent")
		}
		// Cancel through the screen's input path and recover the live detail.
		detail.HandleKey("backspace", tea.KeyPressMsg{})
		detail.HandleKey("backspace", tea.KeyPressMsg{})
		if view := detail.View(67, 36); !strings.Contains(view, "17,000") || strings.Contains(view, "Confirm Channel Close") {
			t.Fatal("canceling review did not return to the updated detail")
		}
		refresh(good, closed)
		failed := errors.New("RPC failed")
		refresh(app.StatusSnapshot{Channels: app.Observation[app.ChannelStatus]{Err: failed}}, app.Observation[[]lndrpc.ClosedChannel]{Err: failed})
		if view := detail.View(67, 36); !strings.Contains(view, "Showing stale data") || !strings.Contains(view, "15,000") {
			t.Fatal("failed read erased retained detail or its stale marker")
		}
		if view := history.View(67, 30); !strings.Contains(view, "Current channels stale") || !strings.Contains(view, "Closed channels stale") || !strings.Contains(view, "closed peer") {
			t.Fatal("independent stale history evidence lost")
		}
		refresh(app.StatusSnapshot{Channels: freshStatus(app.ChannelStatus{})}, app.Observation[[]lndrpc.ClosedChannel]{Err: failed})
		if view := detail.View(67, 36); !strings.Contains(view, "Not in the latest successful list.") || strings.Contains(view, "15,000") {
			t.Fatal("removed channel retained facts or lacked absence evidence")
		}
		if view := history.View(67, 30); strings.Contains(view, "updated peer") || strings.Contains(view, "Current channels stale") || !strings.Contains(view, "closed peer") {
			t.Fatal("current recovery discarded independent stale closed rows")
		}
		refresh(app.StatusSnapshot{Channels: app.Observation[app.ChannelStatus]{Err: failed}}, closed)
		if !strings.Contains(detail.View(67, 36), "List unavailable") {
			t.Fatal("failure after removal claims current absence")
		}
		refresh(good, freshStatus([]lndrpc.ClosedChannel(nil)))
		if view := detail.View(67, 36); strings.Contains(view, "stale") || !strings.Contains(view, "15,000") {
			t.Fatal("existing detail did not recover")
		}
		refresh(app.StatusSnapshot{Channels: freshStatus(app.ChannelStatus{})}, freshStatus([]lndrpc.ClosedChannel(nil)))
		if !strings.Contains(history.View(67, 30), "No channel history.") || len(m.tabs) != 2 || m.tabs[0].Screen != detail || m.activeTab != 1 {
			t.Fatal("empty recovery failed or refresh replaced navigation")
		}
		call.result <- app.StatusSnapshot{}
		<-done
	})
}

func TestChannelViewsRejectOldScopesAndAllowExplicitReopen(t *testing.T) {
	for _, change := range []string{"wallet", "client", "network", "history owner"} {
		t.Run(change, func(t *testing.T) {
			m, home, reader := channelHistoryModel(t)
			client := &lndrpc.Client{}
			m.lndClient, m.screenCtx.LndClient = client, client
			channel := channelInfo{Channel: lndrpc.Channel{ChannelPoint: "funding:0", PeerAlias: "old peer", Capacity: 30000}}
			m.screenCtx.Status = &statusSnapshot{Channels: freshStatus(app.ChannelStatus{Channels: []channelInfo{channel}})}
			m.statusScope = m.currentStatusScope()
			home.focusZone = chanHomeZoneList
			_, delayedDetail := home.handleEnter()
			statusUpdate(&m, delayedDetail())
			detail := m.tabs[0].Screen.(*ChannelDetailScreen)
			history, first, _ := mountChannelHistory(t, &m, home)
			_, delayedHistory := home.openHistory()
			switch change {
			case "wallet":
				m.screenCtx.invalidateWalletObservations()
			case "client":
				m.screenCtx.LndClient = &lndrpc.Client{}
			case "network":
				m.cfg.Network = "public-signet"
			case "history owner":
				m.screenCtx.ChannelHistory = &channelHistoryContext{reader: reader}
			}
			reader.next.Closed = freshStatus([]lndrpc.ClosedChannel{{ChannelPoint: "old:0", PeerAlias: "obsolete closed"}})
			statusUpdate(&m, first())
			if reader.source != client {
				t.Fatal("delayed command read a different client from the one admitted")
			}
			if strings.Contains(history.View(67, 30), "obsolete closed") || m.screenCtx.ChannelHistory.Closed.Known() {
				t.Fatal("previous scope or owner published late history")
			}
			if statusUpdate(&m, delayedHistory()) != nil || m.tabs[1].Screen != history {
				t.Fatal("delayed history open adopted a new scope")
			}
			if change != "history owner" {
				if statusUpdate(&m, delayedDetail()) != nil || !strings.Contains(detail.View(67, 30), "previous wallet session") {
					t.Fatal("detail silently adopted a new wallet scope")
				}
				channel.PeerAlias = "current peer"
				m.screenCtx.Status = &statusSnapshot{Channels: freshStatus(app.ChannelStatus{Channels: []channelInfo{channel}})}
				home.cursor = 0
				_, reopen := home.handleEnter()
				statusUpdate(&m, reopen())
				if m.tabs[0].Screen == detail || !strings.Contains(m.tabs[0].Screen.View(67, 30), "current peer") {
					t.Fatal("explicit current-scope detail reopen failed")
				}
			}
			_, reopen := home.openHistory()
			if statusUpdate(&m, reopen()) == nil || m.tabs[1].Screen == history || len(m.tabs) != 2 {
				t.Fatal("explicit history reopen failed or duplicated the tab")
			}
		})
	}
}

func TestChannelMutationCompletionInvalidatesEarlierReads(t *testing.T) {
	for _, operation := range []string{"open", "close"} {
		t.Run(operation, func(t *testing.T) {
			m, statusReader := statusModelFixture(t)
			var mutation tea.Cmd
			var submitted func() int
			var screen Screen
			kind := tabChannel
			if operation == "open" {
				s, client := channelScreen(t)
				m.screenCtx, screen, kind = s.ctx, s, tabOpenChannel
				mutation = confirmChannel(t, s)
				submitted = func() int { return len(client.sent) }
			} else {
				detail, client := closeScreenFixture(strings.Repeat("a", 64) + ":0")
				m.screenCtx, screen = detail.ctx, detail
				reviewClose(t, detail.closeScreen, false, 2)
				mutation = submitClose(t, detail.closeScreen)
				submitted = func() int { return len(client.sent) }
			}
			m.cfg, m.state = m.screenCtx.Cfg, m.screenCtx.State
			m.statusScope = m.currentStatusScope()
			m.tabs = []openTab{{Kind: kind, Section: secChannels, Screen: screen}}
			m.screenCtx.Status = &statusSnapshot{Channels: freshStatus(app.ChannelStatus{})}
			reader := &channelHistoryTestReader{next: app.ChannelHistorySnapshot{Closed: freshStatus([]lndrpc.ClosedChannel{{ChannelPoint: "old:0"}})}}
			m.screenCtx.ChannelHistory = &channelHistoryContext{reader: reader}
			call, done := startStatusCommand(t, statusUpdate(&m, requestStatusCmd()), statusReader)
			oldHistory := statusUpdate(&m, requestChannelHistoryCmd())
			refresh := statusUpdate(&m, mutation())
			if refresh == nil {
				t.Fatal("mutation completion omitted observation refresh")
			}
			resultView := screen.View(67, 36)
			requestStatus := statusUpdate(&m, refresh())
			if m.screenCtx.Status.Channels.Fresh() {
				t.Fatal("pre-mutation channel evidence still claims freshness")
			}
			if requestStatus != nil {
				statusUpdate(&m, requestStatus())
			}
			call.result <- app.StatusSnapshot{Channels: freshStatus(app.ChannelStatus{Channels: []channelInfo{{Channel: lndrpc.Channel{ChannelPoint: "obsolete:0"}}}})}
			nextStatus := statusUpdate(&m, <-done)
			nextHistory := statusUpdate(&m, oldHistory())
			if nextStatus == nil || nextHistory == nil || m.screenCtx.ChannelHistory.Closed.Known() || len(m.screenCtx.Status.Channels.Value.Channels) != 0 {
				t.Fatal("pre-mutation completion published or failed to schedule a current read")
			}
			call, done = startStatusCommand(t, nextStatus, statusReader)
			call.result <- app.StatusSnapshot{Channels: freshStatus(app.ChannelStatus{})}
			statusUpdate(&m, <-done)
			reader.next.Closed = freshStatus([]lndrpc.ClosedChannel(nil))
			statusUpdate(&m, nextHistory())
			if !m.screenCtx.Status.Channels.Fresh() || !m.screenCtx.ChannelHistory.Closed.Fresh() || screen.View(67, 36) != resultView || submitted() != 1 {
				t.Fatal("observation recovery altered the result or retried the mutation")
			}
		})
	}
}

func TestHistorySelectionKeepsFundingIdentityAndObservationSource(t *testing.T) {
	theme.Init(true)
	m, home, reader := channelHistoryModel(t)
	a := channelInfo{Channel: lndrpc.Channel{ChannelPoint: "first:0", PeerAlias: strings.Repeat("界", 20), Capacity: 10000}}
	b := a
	b.ChannelPoint, b.Capacity = "second:1", 20000
	m.screenCtx.Status = &statusSnapshot{Channels: freshStatus(app.ChannelStatus{Channels: []channelInfo{a, b}})}
	m.statusScope = m.currentStatusScope()
	history, read, _ := mountChannelHistory(t, &m, home)
	m.screenCtx.ContentFocused = true
	reader.next.Closed = freshStatus([]lndrpc.ClosedChannel{{ChannelPoint: b.ChannelPoint, PeerAlias: b.PeerAlias, Capacity: b.Capacity}})
	statusUpdate(&m, read())
	selected := func() string {
		t.Helper()
		selected := ""
		for _, line := range strings.Split(ansi.Strip(history.View(67, 30)), "\n") {
			if ansi.StringWidth(line) > 67 {
				t.Fatal("history alias or state exceeds supported pane")
			}
			if strings.HasPrefix(line, "▸") {
				selected = line
			}
		}
		if selected == "" {
			t.Fatal("history lost visible selection")
		}
		return selected
	}
	history.HandleKey("down", tea.KeyPressMsg{})
	m.screenCtx.Status.Channels.Value.Channels = []channelInfo{b, a}
	if line := selected(); !strings.Contains(line, "20k") || strings.Contains(line, "closed") {
		t.Fatal("same-peer reorder changed the selected funding outpoint")
	}
	history.HandleKey("down", tea.KeyPressMsg{})
	history.HandleKey("down", tea.KeyPressMsg{})
	m.screenCtx.Status.Channels.Value.Channels = []channelInfo{a, b}
	m.screenCtx.Status.Channels.Err = errors.New("current read failed")
	if !strings.Contains(selected(), "closed") {
		t.Fatal("same outpoint in independent sources retargeted selection")
	}
	m.screenCtx.Status.Channels.Value.Pending.WaitingCloseChannels = []lndrpc.WaitingCloseChannel{{ChannelPoint: "waiting:0", PeerAlias: "waiting peer"}}
	view := history.View(67, 30)
	if strings.Contains(view, "unconfirmed") || !strings.Contains(view, "closing") {
		t.Fatal("waiting-close observation claims an unobserved confirmation state")
	}
	if !strings.Contains(selected(), "closed") {
		t.Fatal("new current row displaced selected closed row")
	}
}
