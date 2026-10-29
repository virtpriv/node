package tui

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/theme"
	"github.com/virtpriv/node/internal/update/protocol"
)

func TestSavedUpdateCanBeResumedWithoutReleaseDiscovery(t *testing.T) {
	client := &nodeUpdateClientStub{status: protocol.Status{ID: strings.Repeat("a", 64), Version: "0.7.1-rc.1", Phase: "failed", Active: true}}
	ctx := &ScreenContext{Cfg: config.Default(), NodeUpdates: client, Version: "0.7.0"}
	home := NewSystemHomeScreen(ctx)
	home.btnIdx = slices.Index(home.buttonActions(), sysBtnUpdates)
	if home.btnIdx < 0 {
		t.Fatal("saved update is unreachable without release discovery")
	}
	_, cmd := home.HandleKey("enter", tea.KeyPressMsg{})
	if cmd == nil {
		t.Fatal("Updates did not open")
	}
	opened, ok := cmd().(openTabMsg)
	if !ok || opened.Kind != tabUpdates {
		t.Fatal("Updates did not open a tab")
	}
	updates, ok := opened.Screen.(*UpdatesScreen)
	if !ok {
		t.Fatal("Updates opened the wrong screen")
	}
	updates.button = slices.Index(updates.buttons(), "Node Updates")
	if updates.button < 0 {
		t.Fatal("Updates has no Node Updates button")
	}
	_, cmd = updates.HandleKey("enter", tea.KeyPressMsg{})
	if cmd == nil {
		t.Fatal("Node Updates did not open")
	}
	opened, ok = cmd().(openTabMsg)
	if !ok || opened.Parent != tabUpdates {
		t.Fatal("Node Updates did not open a tab under Updates")
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

func TestContinueResumesAnExpectedStop(t *testing.T) {
	for _, f := range []protocol.Failure{
		{Code: protocol.FailureStopFailed, Service: protocol.LND},
		{Code: protocol.FailureStartInterrupted, Service: protocol.LND},
	} {
		client := &nodeUpdateClientStub{status: protocol.Status{ID: strings.Repeat("a", 64), Version: "0.7.1", Phase: "failed", Active: true, Error: "stopped", Failure: &f}}
		s := NewNodeUpdateScreen(&ScreenContext{NodeUpdates: client, Version: "0.7.0"})
		s.HandleMsg(s.Init()())
		s.button = slices.Index(s.buttons(), "Continue")
		if s.button < 0 {
			t.Fatalf("%s offers no Continue", f.Code)
		}
		_, cmd := s.HandleKey("enter", tea.KeyPressMsg{})
		if cmd == nil {
			t.Fatalf("Continue did nothing after %s", f.Code)
		}
		s.HandleMsg(cmd())
		if client.resumed != client.status.ID {
			t.Fatalf("Continue did not resume the update after %s", f.Code)
		}
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
				cmd = pressNodeUpdateAction(t, s, "Review")
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
	cmd = pressNodeUpdateAction(t, s, "Review")
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
	// Deliver the tick directly. The timer's duration is not this contract.
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
	s.HandleMsg(pressNodeUpdateAction(t, s, "Review")())
	if s.review != nil || slices.Contains(s.buttons(), "Install") || client.starts != 0 {
		t.Fatal("guidance was offered as an installable review")
	}
	if view := s.View(80, 24); !strings.Contains(view, "v0.8.0") || !strings.Contains(view, "v0.9.0") {
		t.Fatal("operator is not told which release comes first:", view)
	}
	s.HandleMsg(pressNodeUpdateAction(t, s, "Review")())
	if client.prepared != "0.8.0" || s.review == nil {
		t.Fatal("the bridge release was not reviewed next")
	}
	s.HandleMsg(pressNodeUpdateAction(t, s, "Install")())
	if client.starts != 1 || client.started != strings.Repeat("b", 64) {
		t.Fatal("bridge installation did not submit its reviewed approval")
	}
}

// drawNodeUpdates draws the whole fixed-size frame with the screen in its tab.
// It returns the frame as the terminal receives it and the text of each
// content row.
func drawNodeUpdates(t *testing.T, ctx *ScreenContext, s *NodeUpdateScreen) (string, []string) {
	t.Helper()
	m := Model{
		cfg: ctx.Cfg, state: &RuntimeState{}, screenCtx: ctx, nav: NewNavSidebar(),
		width: tuiWidth, height: theme.FrameHeight + 1, contentFocused: true, activeTab: 1,
		tabs: []openTab{{Kind: tabNodeUpdates, Section: secSystem, Screen: s}},
	}
	m.nav.ActiveItem, m.nav.Cursor = secSystem, secSystem
	raw := m.viewMain()
	rows := strings.Split(ansi.Strip(raw), "\n")
	if len(rows) != theme.FrameHeight+1 || !strings.HasPrefix(rows[0], "╭") || !strings.HasPrefix(rows[theme.FrameHeight-1], "╰") {
		t.Fatalf("frame is not %d rows:\n%s", theme.FrameHeight, strings.Join(rows, "\n"))
	}
	// The top border, the two rows of the top bar and the line under them
	// come before the content rows.
	var content []string
	for _, row := range rows[2+theme.TopBarRows : 2+theme.TopBarRows+theme.ContentRows] {
		if lipgloss.Width(row) != tuiWidth {
			t.Fatalf("frame row is not %d columns: %q", tuiWidth, row)
		}
		cells := []rune(row)
		content = append(content, string(cells[theme.SidebarWidth+2:len(cells)-1]))
	}
	return raw, content
}

// readable reports whether text can be read in full on the drawn rows,
// wherever its lines were broken.
func readable(content []string, text string) bool {
	squash := func(s string) string { return strings.Join(strings.Fields(s), "") }
	return strings.Contains(squash(strings.Join(content, " ")), squash(text))
}

// The screen has a fixed size. Whatever a failure says, the operator must be
// able to read its start, the next step and how to open the update log, and
// must see every action the screen accepts.
func TestNodeUpdateScreenKeepsButtonsAndFailureTextReadable(t *testing.T) {
	id := strings.Repeat("a", 64)
	failed := func(text string, cancellable bool) protocol.Status {
		return protocol.Status{ID: id, Version: "0.7.1", Phase: "failed", Step: "Stop LND", Error: text, Active: true, Cancellable: cancellable}
	}
	// A failed stop followed by a failed cleanup, as the worker joins them.
	stop := "stop lnd: systemctl stop lnd.service: exit status 1: Job for lnd.service failed because the control process exited with error code.\n" +
		"See \"systemctl status lnd.service\" and \"journalctl -xeu lnd.service\" for details.\n\n" +
		"bitcoind did not stop cleanly, no binaries were replaced"
	refusal := "this installed combination and network have no tested transition in the release plan; install the release it names first and review again"
	outage := "update-status: helper unreachable: dial unix /run/vpn-helperd.sock: connect: no such file or directory; ask the host administrator to check the helper"
	var endless strings.Builder
	for i := range 40 {
		fmt.Fprintf(&endless, "step %d: the unit reported a failure that fills most of one row of this screen\n", i)
	}
	summary := strings.TrimSpace(strings.Repeat("Updates LND for a channel-closing fix and restarts it. ", 9))
	firstStop := strings.SplitN(stop, "\n", 2)[0]
	changed := []string{firstStop, "Services may have been changed."}
	unchanged := []string{"not enough free space", "No services were changed."}

	cases := []struct {
		name    string
		latest  string
		prepare func(*testing.T, *NodeUpdateScreen)
		buttons []string
		shown   []string
		// absent lists byte sequences that must never reach the terminal.
		absent []string
	}{
		{name: "failure with line breaks after host changes", latest: "0.7.1",
			prepare: func(_ *testing.T, s *NodeUpdateScreen) { s.status = failed(stop, false) },
			buttons: []string{"Close", "Retry", "Select release"}, shown: changed},
		{name: "cancellable failure", latest: "0.7.1",
			prepare: func(_ *testing.T, s *NodeUpdateScreen) { s.status = failed("not enough free space", true) },
			buttons: []string{"Close", "Retry", "Cancel update", "Select release"}, shown: unchanged},
		{name: "cancellable failure with a newer release", latest: "0.7.2",
			prepare: func(_ *testing.T, s *NodeUpdateScreen) { s.status = failed("not enough free space", true) },
			buttons: []string{"Close", "Retry", "Cancel update", "Review", "Select release"}, shown: unchanged},
		{name: "long failure on one line", latest: "0.7.1",
			prepare: func(_ *testing.T, s *NodeUpdateScreen) { s.status = failed(refusal, false) },
			buttons: []string{"Close", "Retry", "Select release"}, shown: []string{refusal}},
		{name: "failure longer than the screen", latest: "0.7.1",
			prepare: func(_ *testing.T, s *NodeUpdateScreen) { s.status = failed(endless.String(), false) },
			buttons: []string{"Close", "Retry", "Select release"},
			shown:   []string{"step 0: the unit reported a failure"}},
		{name: "saved failure, action error and status error together", latest: "0.7.2",
			prepare: func(_ *testing.T, s *NodeUpdateScreen) {
				s.status, s.err, s.statusErr = failed(stop, true), errors.New(refusal), errors.New(outage)
			},
			buttons: []string{"Close", "Retry", "Cancel update", "Review", "Select release"},
			shown:   []string{firstStop, refusal, "update-status: helper unreachable"}},
		{name: "refused review without a job", latest: "0.7.1",
			prepare: func(_ *testing.T, s *NodeUpdateScreen) { s.err = errors.New(refusal) },
			buttons: []string{"Close", "Review", "Select release"}, shown: []string{refusal}},
		{name: "review with a long summary", latest: "0.7.1",
			prepare: func(_ *testing.T, s *NodeUpdateScreen) {
				s.review = &protocol.Review{Token: id, KeyServerUnreachable: true, Source: protocol.Versions{VPN: "0.7.0"},
					Manifest: protocol.Manifest{Version: "0.7.1", Summary: summary, HostSteps: []string{"lnd-service-v1", "lnd-service-v2", "lnd-service-v3"}}}
			},
			buttons: []string{"Cancel", "Install"}, shown: []string{summary, "lnd-service-v3", "Key server not reached"}},
		{name: "failure with control characters", latest: "0.7.1",
			prepare: func(_ *testing.T, s *NodeUpdateScreen) {
				s.status = failed("first\rsecond\tthird\a \x1b[2Jfourth", false)
			},
			buttons: []string{"Close", "Retry", "Select release"}, shown: []string{"first", "second", "third", "fourth"},
			absent: []string{"\r", "\t", "\a", "\x1b[2J"}},
		{name: "long list with a failure late in it", latest: "0.7.1",
			prepare: func(_ *testing.T, s *NodeUpdateScreen) {
				s.status = failed("disk full while installing new files", false)
				for i := range 24 {
					s.status.Steps = append(s.status.Steps, fmt.Sprintf("Step %d of the update", i+1))
				}
				s.status.StepNumber = 20
			},
			buttons: []string{"Close", "Retry", "Select release"},
			shown:   []string{"Step 20 of the update", "disk full while installing new files", "Select Retry to try again."}},
		{name: "release to install first, with long names",
			prepare: func(t *testing.T, s *NodeUpdateScreen) {
				s.ctx.NodeUpdates = &nodeUpdateClientStub{updateFirst: map[string]string{"0.7.2-rc.12": "0.7.2-rc.11"}}
				s.HandleMsg(reviewSelectedRelease(t, s, "v0.7.2-rc.12")())
			},
			buttons: []string{"Close", "Review", "Select release"},
			shown:   []string{"v0.7.2-rc.12 can be installed after v0.7.2-rc.11. Review v0.7.2-rc.11 first."}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := &ScreenContext{Cfg: config.Default(), Version: "0.7.0", LatestVersion: c.latest}
			s := NewNodeUpdateScreen(ctx)
			c.prepare(t, s)
			if got := s.buttons(); !slices.Equal(got, c.buttons) {
				t.Fatalf("screen offers %q, want %q", got, c.buttons)
			}
			raw, content := drawNodeUpdates(t, ctx, s)
			drawn := strings.Join(content, "\n")
			last := content[len(content)-1]
			at := 0
			for _, label := range c.buttons {
				i := strings.Index(last[at:], label)
				if i < 0 {
					t.Fatalf("button %q is not drawn in full on the last row:\n%s", label, drawn)
				}
				at += i + len(label)
			}
			for _, text := range c.shown {
				if !readable(content, text) {
					t.Errorf("cannot read %q on the screen:\n%s", text, drawn)
				}
			}
			for _, bytes := range c.absent {
				if strings.Contains(raw, bytes) {
					t.Errorf("%q reached the terminal", bytes)
				}
			}
		})
	}
}

// A record without a usable list shows the step's own text, which comes
// from the update program and must not control the terminal. The list's
// names are the shared step list's.
func TestStatusTextCannotControlTheTerminal(t *testing.T) {
	s := NewNodeUpdateScreen(&ScreenContext{Version: "0.7.0"})
	s.status = protocol.Status{ID: strings.Repeat("a", 64), Version: "0.7.1", Phase: "running", Active: true, Running: true, Step: "Install\x1b[2J"}
	if strings.Contains(s.View(80, 40), "\x1b[2J") {
		t.Fatal("the step text reached the terminal with its control characters")
	}
}

// messagesWithin runs cmd as Bubble Tea would and returns the messages that
// arrive within d. Commands that take longer, such as the 2 second poll,
// are left running.
func messagesWithin(cmd tea.Cmd, d time.Duration) []tea.Msg {
	out := make(chan tea.Msg, 64)
	var run func(tea.Cmd)
	run = func(c tea.Cmd) {
		if c == nil {
			return
		}
		go func() {
			msg := c()
			if batch, ok := msg.(tea.BatchMsg); ok {
				for _, b := range batch {
					run(b)
				}
				return
			}
			out <- msg
		}()
	}
	run(cmd)
	var msgs []tea.Msg
	deadline := time.After(d)
	for {
		select {
		case msg := <-out:
			msgs = append(msgs, msg)
		case <-deadline:
			return msgs
		}
	}
}

// spinTick is the first message in msgs that is s's spinner tick.
func spinTick(s *NodeUpdateScreen, msgs []tea.Msg) tea.Msg {
	for _, msg := range msgs {
		if s.spin.Owns(msg) {
			return msg
		}
	}
	return nil
}

// A step list's spinner ticks only while its screen is on screen. A tick
// for a hidden screen starts no next tick, so nothing redraws for it, and
// the spinner starts again once its screen shows.
func TestStepSpinnerTicksOnlyWhileItsScreenShows(t *testing.T) {
	running := protocol.Status{ID: strings.Repeat("a", 64), Version: "0.7.1", Phase: "installing", Active: true, Running: true,
		Steps: []string{"Check the node", "Stop lnd"}, StepNumber: 2}
	ctx := &ScreenContext{Cfg: config.Default(), NodeUpdates: &nodeUpdateClientStub{status: running}, Version: "0.7.0"}
	s := NewNodeUpdateScreen(ctx)
	m := Model{cfg: ctx.Cfg, state: &RuntimeState{}, screenCtx: ctx, nav: NewNavSidebar(), activeTab: 1,
		tabs: []openTab{{Kind: tabNodeUpdates, Section: secSystem, Screen: s}}}
	show := func(on bool) {
		m.nav.ActiveItem, m.nav.Cursor, m.activeTab = secChannels, secChannels, 0
		if on {
			m.nav.ActiveItem, m.nav.Cursor, m.activeTab = secSystem, secSystem, 1
		}
	}
	update := func(msg tea.Msg) tea.Msg {
		t.Helper()
		next, cmd := m.Update(msg)
		m = next.(Model)
		return spinTick(s, messagesWithin(cmd, 400*time.Millisecond))
	}
	show(true)
	tick := update(s.Init()())
	if tick == nil {
		t.Fatal("a running step on screen started no spinner")
	}
	if tick = update(tick); tick == nil {
		t.Fatal("the spinner stopped while its screen shows")
	}
	show(false)
	if update(tick) != nil {
		t.Fatal("the spinner kept ticking for a hidden screen")
	}
	show(true)
	if update(tea.WindowSizeMsg{Width: 100, Height: 40}) == nil {
		t.Fatal("the spinner did not start again when its screen showed")
	}
}

func TestRefreshDoesNotMoveFocus(t *testing.T) {
	// Focus moves when a state appears. A refresh of the same state leaves
	// the operator's own choice alone, so Enter never does what they did not
	// choose.
	f := protocol.Failure{Code: protocol.FailureStopFailed, Service: protocol.LND}
	client := &nodeUpdateClientStub{status: protocol.Status{ID: strings.Repeat("a", 64), Version: "0.7.1", Phase: "failed", Active: true, Error: "stopped", Failure: &f}}
	s := NewNodeUpdateScreen(&ScreenContext{NodeUpdates: client, Version: "0.7.0"})
	s.HandleMsg(s.Init()())
	s.button = 0
	s.HandleMsg(s.request("status")())
	if s.button != 0 {
		t.Fatal("a refresh moved the focus")
	}
}

func TestNextFromTheUpdateProgramLeadsAnUnknownStop(t *testing.T) {
	// A later release's own stop of normal use, unknown to this screen,
	// reaches it with its next step and words.
	client := &nodeUpdateClientStub{status: protocol.Status{ID: strings.Repeat("a", 64), Version: "0.7.1", Phase: "failed", Active: true,
		Error: "syncthing was busy", Failure: &protocol.Failure{Code: "syncthing_busy", Service: protocol.Syncthing},
		Next: protocol.NextContinue, Advice: "Syncthing was busy and has stopped now.\n\nSelect Continue."}}
	s := NewNodeUpdateScreen(&ScreenContext{NodeUpdates: client, Version: "0.7.0"})
	s.HandleMsg(s.Init()())
	s.button = slices.Index(s.buttons(), "Continue")
	if s.button < 0 {
		t.Fatal("next continue offers no Continue")
	}
	_, cmd := s.HandleKey("enter", tea.KeyPressMsg{})
	s.HandleMsg(cmd())
	if client.resumed != client.status.ID {
		t.Fatal("Continue did not resume the update")
	}
}

func TestNextWithoutItsAdviceIsNotFollowed(t *testing.T) {
	// The helper drops advice it cannot show. A next step left alone would
	// pair the update program's button with this screen's own words, so
	// the screen then uses its own pair.
	client := &nodeUpdateClientStub{status: protocol.Status{ID: strings.Repeat("a", 64), Version: "0.7.1", Phase: "failed", Active: true,
		Error: "syncthing was busy", Failure: &protocol.Failure{Code: "syncthing_busy", Service: protocol.Syncthing},
		Next: protocol.NextContinue}}
	s := NewNodeUpdateScreen(&ScreenContext{NodeUpdates: client, Version: "0.7.0"})
	s.HandleMsg(s.Init()())
	if slices.Contains(s.buttons(), "Continue") {
		t.Fatal("a next step without its advice was followed")
	}
}
