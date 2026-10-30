package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/update/protocol"
)

// testRelease names tag on Settings' Test release form and selects Review, as
// an operator would. It returns the screen and the command Review gave.
func testRelease(t *testing.T, ctx *ScreenContext, tag string) (*SettingsScreen, tea.Cmd) {
	t.Helper()
	s := &SettingsScreen{ctx: ctx}
	s.HandleKey("enter", tea.KeyPressMsg{})
	if s.release == nil {
		t.Fatal("Test release did not open its form")
	}
	s.HandleMsg(tea.PasteMsg{Content: tag})
	s.HandleKey("enter", tea.KeyPressMsg{}) // from the field to Review
	_, cmd := s.HandleKey("enter", tea.KeyPressMsg{})
	return s, cmd
}

// openedNodeUpdates runs Review's request and returns the Node Updates
// screen the answer opens, and how it is opened.
func openedNodeUpdates(t *testing.T, s *SettingsScreen, cmd tea.Cmd) (*NodeUpdateScreen, openTabMsg) {
	t.Helper()
	if cmd == nil {
		t.Fatal("Review asked nothing")
	}
	_, next := s.HandleMsg(cmd())
	if next == nil {
		t.Fatal("Review opened nothing")
	}
	opened, ok := next().(openTabMsg)
	if !ok || opened.Kind != tabNodeUpdates {
		t.Fatal("Review did not open Node Updates")
	}
	screen, ok := opened.Screen.(*NodeUpdateScreen)
	if !ok {
		t.Fatal("Review opened the wrong screen")
	}
	return screen, opened
}

// A tag the node can review opens on Node Updates as that release's review,
// and Install there submits exactly what was reviewed. A tag that is not
// newer never reaches the node.
func TestTestReleaseOpensItsReviewOnNodeUpdates(t *testing.T) {
	client := &nodeUpdateClientStub{}
	ctx := &ScreenContext{Cfg: config.Default(), NodeUpdates: client, Version: "0.7.0", LatestVersion: "0.7.1"}
	for _, tag := range []string{"v0.7.0", "v0.6.3", "v0.7.0-rc.1", "latest"} {
		s, _ := testRelease(t, ctx, tag)
		if client.prepared != "" || s.release == nil || s.err == "" {
			t.Fatalf("%s was not refused on the form before the node was asked", tag)
		}
	}
	s, cmd := testRelease(t, ctx, "v0.7.1-rc.1")
	screen, opened := openedNodeUpdates(t, s, cmd)
	if client.prepared != "0.7.1-rc.1" || screen.review == nil || screen.review.Manifest.Version != "0.7.1-rc.1" {
		t.Fatal("Node Updates did not open on the review of the named release")
	}
	if opened.Kind != tabNodeUpdates || s.release != nil || client.starts != 0 {
		t.Fatal("the review did not open Node Updates with the form closed and nothing installed")
	}
	screen.HandleMsg(pressNodeUpdateAction(t, screen, "Install")())
	if client.starts != 1 || client.started != strings.Repeat("b", 64) {
		t.Fatal("Install did not submit the reviewed approval token")
	}
}

// An update that is still on its way, or paused at Continue, is shown, not
// reviewed over: the node is not asked for a review and Node Updates opens
// on that update.
func TestTestReleaseDuringAnUpdateOpensThatUpdate(t *testing.T) {
	id := strings.Repeat("a", 64)
	for _, st := range []protocol.Status{
		{ID: id, Version: "0.7.1", Phase: "installing", Active: true, Running: true},
		{ID: id, Version: "0.7.1", Phase: "waiting-unlock", Active: true, Running: true},
		{ID: id, Version: "0.7.1", Phase: "installing", Active: true},
		{ID: id, Version: "0.7.1", Phase: "failed", Active: true, Next: protocol.NextContinue,
			Advice: "The update was interrupted, for example by a restart.\n\nSelect Continue."},
	} {
		client := &nodeUpdateClientStub{status: st}
		ctx := &ScreenContext{Cfg: config.Default(), NodeUpdates: client, Version: "0.7.0"}
		s, cmd := testRelease(t, ctx, "v0.7.2-rc.1")
		screen, opened := openedNodeUpdates(t, s, cmd)
		if client.prepared != "" || screen.review != nil || opened.Kind != tabNodeUpdates || s.release != nil {
			t.Fatalf("%s: a review was asked for over the unfinished update", st.Phase)
		}
		screen.HandleMsg(screen.Init()())
		if screen.status.ID != id {
			t.Fatalf("%s: Node Updates did not show the unfinished update", st.Phase)
		}
	}
}

// After a failed update the node decides. Its refusal stays on the form, in
// its own words, and Node Updates does not open.
func TestTestReleaseRefusalStaysOnTheForm(t *testing.T) {
	refusal := errors.New("this release cannot repair the unfinished update, select Retry first")
	client := &nodeUpdateClientStub{prepareErr: refusal,
		status: protocol.Status{ID: strings.Repeat("a", 64), Version: "0.7.1", Phase: "failed", Active: true}}
	ctx := &ScreenContext{Cfg: config.Default(), NodeUpdates: client, Version: "0.7.0"}
	s, cmd := testRelease(t, ctx, "v0.7.2-rc.1")
	if cmd == nil {
		t.Fatal("Review asked nothing")
	}
	_, next := s.HandleMsg(cmd())
	for _, msg := range messagesWithin(next, 100*time.Millisecond) {
		if _, ok := msg.(openTabMsg); ok {
			t.Fatal("a refused release opened Node Updates")
		}
	}
	if client.prepared != "0.7.2-rc.1" || s.release == nil || !readable(strings.Split(ansi.Strip(s.View(67, 30)), "\n"), refusal.Error()) {
		t.Fatal("the node's refusal is not shown on the form")
	}
}

// Node Updates waiting for an answer, such as the start of an approved
// update, is not replaced by a review, so that answer is not lost.
func TestBusyNodeUpdatesIsNotReplaced(t *testing.T) {
	ctx := &ScreenContext{Cfg: config.Default(), NodeUpdates: &nodeUpdateClientStub{}, Version: "0.7.0"}
	busy := NewNodeUpdateScreen(ctx)
	busy.busy = "start"
	m := Model{nav: NewNavSidebar(), screenCtx: ctx, cfg: ctx.Cfg, state: &RuntimeState{},
		tabs: []openTab{{Kind: tabNodeUpdates, Section: secSystem, Screen: busy}}}
	m.nav.ActiveItem = secSystem
	review := newReviewedRelease(ctx, "0.7.1-rc.1", protocol.Review{Token: "t", Manifest: protocol.Manifest{Version: "0.7.1-rc.1"}})
	next, _ := m.Update(openTabMsg{Kind: tabNodeUpdates, Label: "Node Updates", Screen: review, Parent: tabUpdates, Replace: true})
	if next.(Model).tabs[0].Screen != busy {
		t.Fatal("a review replaced Node Updates while it waited for an answer")
	}
}

// systemTabs is a model on the System section with tabs open in order. The
// last one is shown, with the content focused.
func systemTabs(ctx *ScreenContext, tabs ...openTab) Model {
	for i := range tabs {
		tabs[i].Section = secSystem
	}
	m := Model{nav: NewNavSidebar(), screenCtx: ctx, cfg: ctx.Cfg, state: &RuntimeState{}, tabs: tabs, activeTab: len(tabs)}
	m.nav.ActiveItem, m.nav.Cursor = secSystem, secSystem
	m.focusContent()
	return m
}

// deliver hands msg to the model and returns the command it gave.
func deliver(m *Model, msg tea.Msg) tea.Cmd {
	next, cmd := m.Update(msg)
	*m = next.(Model)
	return cmd
}

// Settings only leads to Node Updates, which stays under Updates: backspace
// in Node Updates goes back to Updates, and closing Settings leaves Node
// Updates open.
func TestNodeUpdatesFromSettingsStaysUnderUpdates(t *testing.T) {
	ctx := &ScreenContext{Cfg: config.Default(), NodeUpdates: &nodeUpdateClientStub{}, Version: "0.7.0"}
	settings, cmd := testRelease(t, ctx, "v0.7.1-rc.1")
	m := systemTabs(ctx,
		openTab{Kind: tabUpdates, Screen: &UpdatesScreen{ctx: ctx, home: NewSystemHomeScreen(ctx)}},
		openTab{Kind: tabSettings, Screen: settings})
	deliver(&m, deliver(&m, cmd())())
	if node, ok := m.effectiveTabs()[m.activeTab].Screen.(*NodeUpdateScreen); !ok || node.review == nil {
		t.Fatal("the review did not open on Node Updates")
	}
	deliver(&m, deliver(&m, tea.KeyPressMsg{Code: tea.KeyBackspace})())
	if m.effectiveTabs()[m.activeTab].Kind != tabUpdates || len(m.tabs) != 3 {
		t.Fatal("backspace in Node Updates did not go back to Updates with Node Updates kept")
	}
	for i, tab := range m.effectiveTabs() {
		if tab.Kind == tabSettings {
			next, _ := m.closeTab(i)
			m = next.(Model)
		}
	}
	if len(m.tabs) != 2 {
		t.Fatal("closing Settings closed Node Updates")
	}
}

// An open Node Updates tab already shows the update on its way and is kept.
func TestRunningUpdateKeepsTheOpenNodeUpdates(t *testing.T) {
	client := &nodeUpdateClientStub{status: protocol.Status{ID: strings.Repeat("a", 64), Version: "0.7.1", Phase: "installing", Active: true, Running: true}}
	ctx := &ScreenContext{Cfg: config.Default(), NodeUpdates: client, Version: "0.7.0"}
	settings, cmd := testRelease(t, ctx, "v0.7.2-rc.1")
	open := NewNodeUpdateScreen(ctx)
	m := systemTabs(ctx,
		openTab{Kind: tabNodeUpdates, Parent: tabUpdates, Screen: open},
		openTab{Kind: tabSettings, Screen: settings})
	deliver(&m, deliver(&m, cmd())())
	if m.tabs[0].Screen != open || len(m.tabs) != 2 || m.effectiveTabs()[m.activeTab].Kind != tabNodeUpdates {
		t.Fatal("the running update did not show in the open Node Updates")
	}
}

// Preparing a review can take minutes over Tor. Cancel leaves it, and the
// node's late answer opens nothing, also on a form opened again since.
func TestCancelWhilePreparingDropsTheAnswer(t *testing.T) {
	ctx := &ScreenContext{Cfg: config.Default(), NodeUpdates: &nodeUpdateClientStub{}, Version: "0.7.0"}
	s, cmd := testRelease(t, ctx, "v0.7.1-rc.1")
	if !s.checking {
		t.Fatal("Review did not ask the node")
	}
	s.HandleKey("enter", tea.KeyPressMsg{})
	s.HandleKey("enter", tea.KeyPressMsg{}) // Test release again
	if _, next := s.HandleMsg(cmd()); next != nil || s.release == nil || s.err != "" {
		t.Fatal("an answer after Cancel reached the form opened again")
	}
}
