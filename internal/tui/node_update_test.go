package tui

import (
	"errors"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/update/protocol"
)

func TestSavedUpdateCanBeResumedWithoutReleaseDiscovery(t *testing.T) {
	client := &nodeUpdateClientStub{status: protocol.Status{ID: strings.Repeat("a", 64), Version: "0.7.1-rc.1", Phase: "failed", Active: true}}
	ctx := &ScreenContext{Cfg: config.Default(), NodeUpdates: client, Version: "0.7.0"}
	home := NewSystemHomeScreen(ctx)
	home.btnIdx = slices.Index(home.buttonActions(), sysBtnUpdateNode)
	if home.btnIdx < 0 {
		t.Fatal("saved update is unreachable without release discovery")
	}
	_, cmd := home.HandleKey("enter", tea.KeyPressMsg{})
	if cmd == nil {
		t.Fatal("Node Updates did not open")
	}
	opened, ok := cmd().(openTabMsg)
	if !ok {
		t.Fatal("Node Updates did not open a tab")
	}
	s, ok := opened.Screen.(*NodeUpdateScreen)
	if !ok {
		t.Fatal("Node Updates opened the wrong screen")
	}
	s.HandleMsg(s.Init()())
	s.button = slices.Index(s.buttons(), "Retry")
	if s.button < 0 {
		t.Fatal("saved failure cannot be retried")
	}
	_, cmd = s.HandleKey("enter", tea.KeyPressMsg{})
	if cmd == nil {
		t.Fatal("Retry did not submit the saved job")
	}
	s.HandleMsg(cmd())
	if client.resumed != client.status.ID || client.starts != 0 {
		t.Fatal("Retry did not use the saved update identity")
	}
}

type nodeUpdateClientStub struct {
	prepared, started string
	starts            int
	resumed           string
	status            protocol.Status
	prepareErr        error
	statusErr         error
	// updateFirst maps a selected release to the one the helper names first.
	updateFirst map[string]string
}

func (s *nodeUpdateClientStub) Prepare(v string) (protocol.Review, error) {
	s.prepared = v
	if first := s.updateFirst[v]; first != "" {
		return protocol.Review{UpdateFirst: first}, nil
	}
	return protocol.Review{Token: strings.Repeat("b", 64), Digest: strings.Repeat("a", 64), Manifest: protocol.Manifest{Version: v}}, s.prepareErr
}
func (s *nodeUpdateClientStub) Start(id string) (protocol.Status, error) {
	s.started = id
	s.starts++
	return protocol.Status{ID: id, Active: true, Running: true}, nil
}
func (s *nodeUpdateClientStub) Status() (protocol.Status, error) { return s.status, s.statusErr }
func (s *nodeUpdateClientStub) Resume(id string) (protocol.Status, error) {
	s.resumed = id
	return s.status, nil
}
func (s *nodeUpdateClientStub) Cancel(string) (protocol.Status, error) {
	return protocol.Status{}, nil
}

func TestNodeUpdateSubmitsReviewedTokenAndRejectsStalePreparation(t *testing.T) {
	for _, selection := range []struct {
		target   string
		explicit bool
	}{
		{"0.7.1", false}, {"0.7.1-rc.1", true}, {"0.7.2", true},
	} {
		t.Run(selection.target, func(t *testing.T) {
			target := selection.target
			client := &nodeUpdateClientStub{}
			ctx := &ScreenContext{Cfg: config.Default(), NodeUpdates: client, Version: "0.7.0", LatestVersion: "0.7.1"}
			s := NewNodeUpdateScreen(ctx)
			var cmd tea.Cmd
			if selection.explicit {
				cmd = reviewSelectedRelease(t, s, "v"+target)
			} else {
				cmd = pressNodeUpdateAction(t, s, "Review update")
			}
			ctx.LatestVersion = "0.7.3"
			s.HandleMsg(cmd())
			if client.prepared != target || s.review == nil {
				t.Fatal("background discovery changed the release being reviewed")
			}
			cmd = pressNodeUpdateAction(t, s, "Install")
			if again := s.request("start"); again != nil {
				t.Fatal("duplicate start command")
			}
			msg := cmd()
			s.HandleMsg(msg)
			if client.starts != 1 || client.started != strings.Repeat("b", 64) {
				t.Fatal("installation did not submit the reviewed approval token")
			}
			s.HandleMsg(nodeUpdateMsg{owner: s, attempt: s.attempt - 1, kind: "prepare", review: protocol.Review{Digest: strings.Repeat("b", 64)}})
			if s.review != nil {
				t.Fatal("late preparation replaced an accepted update")
			}
			if next := NewNodeUpdateScreen(ctx); next.target != "0.7.3" {
				t.Fatal("one-time candidate selection changed normal discovery")
			}
		})
	}
}

func TestCandidateRequiresExplicitSelection(t *testing.T) {
	client := &nodeUpdateClientStub{}
	ctx := &ScreenContext{Cfg: config.Default(), NodeUpdates: client, Version: "0.7.0", LatestVersion: "0.7.1-rc.1"}
	s := NewNodeUpdateScreen(ctx)
	if NewSystemHomeScreen(ctx).hasUpdate() || s.hasTarget() {
		t.Fatal("normal discovery offered a release candidate")
	}
	cmd := reviewSelectedRelease(t, s, "v0.7.1-rc.1")
	s.HandleMsg(cmd())
	if client.prepared != "0.7.1-rc.1" || s.review == nil || client.starts != 0 {
		t.Fatal("explicit candidate selection did not require review before installation")
	}
}

func TestNodeUpdateReviewFailureSurvivesStatusRefresh(t *testing.T) {
	failure := errors.New("cannot read installed component version")
	client := &nodeUpdateClientStub{prepareErr: failure}
	s := NewNodeUpdateScreen(&ScreenContext{Cfg: config.Default(), NodeUpdates: client, Version: "0.7.0-rc.1"})
	cmd := reviewSelectedRelease(t, s, "v0.7.0-rc.2")
	_, poll := s.HandleMsg(cmd())
	if poll == nil {
		t.Fatal("failed preparation stopped background status polling")
	}
	if s.review != nil || !strings.Contains(s.View(100, 40), failure.Error()) {
		t.Fatal("failed preparation did not show its error instead of a review")
	}
	// A status outage and recovery must preserve the action failure and polling.
	outage := errors.New("helper status unavailable")
	for _, statusErr := range []error{nil, outage, nil} {
		client.statusErr = statusErr
		_, cmd := s.HandleMsg(nodeUpdateTick{owner: s})
		if cmd == nil || !strings.Contains(s.View(100, 40), failure.Error()) {
			t.Fatal("starting a background check hid the preparation failure")
		}
		_, poll = s.HandleMsg(cmd())
		if poll == nil {
			t.Fatal("status response stopped background status polling")
		}
		view := s.View(100, 40)
		if !strings.Contains(view, failure.Error()) {
			t.Fatal("status refresh erased the preparation failure")
		}
		if strings.Contains(view, outage.Error()) != (statusErr != nil) {
			t.Fatal("status error did not track the outage and recovery")
		}
	}
	client.prepareErr = nil
	cmd = pressNodeUpdateAction(t, s, "Review update")
	if strings.Contains(s.View(100, 40), failure.Error()) {
		t.Fatal("explicit retry kept the previous preparation failure")
	}
	s.HandleMsg(cmd())
	if s.review == nil || s.review.Manifest.Version != "0.7.0-rc.2" || client.starts != 0 {
		t.Fatal("successful retry did not return to review without installation")
	}
}

func reviewSelectedRelease(t *testing.T, s *NodeUpdateScreen, tag string) tea.Cmd {
	t.Helper()
	s.button = slices.Index(s.buttons(), "Select release")
	if s.button < 0 {
		t.Fatal("candidate selection is unreachable")
	}
	s.HandleKey("enter", tea.KeyPressMsg{})
	s.HandleMsg(tea.PasteMsg{Content: tag})
	s.HandleKey("enter", tea.KeyPressMsg{})
	_, cmd := s.HandleKey("enter", tea.KeyPressMsg{})
	return cmd
}

func pressNodeUpdateAction(t *testing.T, s *NodeUpdateScreen, action string) tea.Cmd {
	t.Helper()
	s.button = slices.Index(s.buttons(), action)
	if s.button < 0 {
		t.Fatalf("action %q is unavailable", action)
	}
	_, cmd := s.HandleKey("enter", tea.KeyPressMsg{})
	if cmd == nil {
		t.Fatalf("action %q produced no command", action)
	}
	return cmd
}

func TestNodeUpdateMessagesReachOnlyTheirLiveOwner(t *testing.T) {
	client := &nodeUpdateClientStub{status: protocol.Status{ID: strings.Repeat("a", 64), Active: true, Phase: "failed"}}
	ctx := &ScreenContext{Cfg: config.Default(), NodeUpdates: client, Version: "0.7.0"}
	owner, other := NewNodeUpdateScreen(ctx), NewNodeUpdateScreen(ctx)
	m := Model{nav: NewNavSidebar(), screenCtx: ctx, activeTab: 1, tabs: []openTab{
		{Kind: tabNodeUpdates, Section: secSystem, Screen: other},
		{Kind: tabNodeUpdates, Section: secSystem, Screen: owner},
	}}
	m.nav.ActiveItem = secSystem
	response := owner.Init()()
	updated, poll := m.Update(response)
	m = updated.(Model)
	if owner.status.ID != client.status.ID || owner.busy != "" || other.status.ID != "" || poll == nil {
		t.Fatal("hidden owner lost its update status or polling")
	}
	// Deliver the tick directly; the timer's duration is not this contract.
	updated, request := m.Update(nodeUpdateTick{owner: owner})
	m = updated.(Model)
	if request == nil || owner.busy != "status" || other.busy != "" {
		t.Fatal("poll was not delivered to its hidden owner")
	}
	pending := request()
	for _, replacement := range []bool{false, true} {
		t.Run(map[bool]string{false: "closed", true: "replaced"}[replacement], func(t *testing.T) {
			m.tabs = m.tabs[:1]
			if replacement {
				m.tabs = append(m.tabs, openTab{Kind: tabNodeUpdates, Section: secSystem, Screen: NewNodeUpdateScreen(ctx)})
			}
			for _, msg := range []tea.Msg{pending, nodeUpdateTick{owner: owner}} {
				updated, cmd := m.Update(msg)
				m = updated.(Model)
				if cmd != nil || owner.busy != "status" || other.status.ID != "" {
					t.Fatal("closed owner handled a response or restarted polling")
				}
			}
			if replacement && m.tabs[1].Screen.(*NodeUpdateScreen).status.ID != "" {
				t.Fatal("old response changed the replacement screen")
			}
		})
	}
}

// A node that skipped releases is told which release to install first. That
// answer is guidance only and must never be installable as it stands.
func TestSkippedReleaseIsReviewedThroughItsBridge(t *testing.T) {
	client := &nodeUpdateClientStub{updateFirst: map[string]string{"0.9.0": "0.8.0"}}
	ctx := &ScreenContext{Cfg: config.Default(), NodeUpdates: client, Version: "0.7.0", LatestVersion: "0.9.0"}
	s := NewNodeUpdateScreen(ctx)
	s.HandleMsg(pressNodeUpdateAction(t, s, "Review update")())
	if s.review != nil || slices.Contains(s.buttons(), "Install") || client.starts != 0 {
		t.Fatal("guidance was offered as an installable review")
	}
	if view := s.View(80, 24); !strings.Contains(view, "v0.8.0") || !strings.Contains(view, "v0.9.0") {
		t.Fatal("operator is not told which release comes first:", view)
	}
	s.HandleMsg(pressNodeUpdateAction(t, s, "Review update")())
	if client.prepared != "0.8.0" || s.review == nil {
		t.Fatal("the bridge release was not reviewed next")
	}
	s.HandleMsg(pressNodeUpdateAction(t, s, "Install")())
	if client.starts != 1 || client.started != strings.Repeat("b", 64) {
		t.Fatal("bridge installation did not submit its reviewed approval")
	}
}
