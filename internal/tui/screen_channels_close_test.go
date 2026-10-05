package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/lndrpc"
	"github.com/virtpriv/node/internal/theme"
)

type screenCloseClient struct {
	sent   []lndrpc.ChannelCloseRequest
	result lndrpc.ChannelCloseResult
}

func (c *screenCloseClient) ChannelCloseState(string) (lndrpc.ChannelCloseState, error) {
	return lndrpc.ChannelCloseState{Found: true, Active: true, StatusFlags: "ChanStatusDefault"}, nil
}
func (c *screenCloseClient) CloseChannel(req lndrpc.ChannelCloseRequest) lndrpc.ChannelCloseResult {
	c.sent = append(c.sent, req)
	return c.result
}
func closeScreenFixture(point string) (*ChannelDetailScreen, *screenCloseClient) {
	ctx := &ScreenContext{Cfg: config.Default(), State: &RuntimeState{WalletKnown: true, WalletExists: true}, HasTabs: true, ContentFocused: true}
	ch := channelInfo{Channel: lndrpc.Channel{ChannelPoint: point, PeerAlias: "same peer", RemotePubkey: strings.Repeat("a", 66), Capacity: 30000, LocalBalance: 20000, Active: true}}
	ctx.Status = &statusSnapshot{Channels: freshStatus(app.ChannelStatus{Channels: []channelInfo{ch}})}
	detail := NewChannelDetailScreen(ctx, ch)
	detail.launchClose()
	// This fixture does not execute the initial fee command.
	detail.closeScreen.fees.cancel()
	c := &screenCloseClient{result: lndrpc.ChannelCloseResult{Submitted: true, ClosingTxid: strings.Repeat("c", 64)}}
	detail.closeScreen.client = c
	return detail, c
}
func reviewClose(t *testing.T, s *ChannelCloseScreen, force bool, rate int64) {
	t.Helper()
	s.focusZone = closeTypeZoneButtons
	s.typeBtnIdx = 1
	if force {
		s.typeIdx = 1
	}
	s.HandleKey("enter", tea.KeyPressMsg{})
	if !force {
		if s.step != closeStepFee {
			t.Fatal("cooperative close skipped fee form")
		}
		s.feeInput.SetSats(rate)
		s.focusZone = closeZoneButtons
		s.confirmBtnIdx = 1
		s.HandleKey("enter", tea.KeyPressMsg{})
	}
	if s.step != closeStepReview || s.attempt == nil {
		t.Fatalf("no immutable review: %s", s.error)
	}
}
func submitClose(t *testing.T, s *ChannelCloseScreen) tea.Cmd {
	t.Helper()
	s.confirmBtnIdx = 1
	_, cmd := s.HandleKey("enter", tea.KeyPressMsg{})
	if cmd == nil || s.step != closeStepClosing {
		t.Fatal("close was not scheduled")
	}
	return cmd
}
func TestChannelCloseFrozenReviewAndCopy(t *testing.T) {
	theme.Init(true)
	for _, tc := range []struct {
		name  string
		force bool
		rate  int64
	}{{"automatic", false, 0}, {"manual", false, 14}, {"force", true, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			detail, c := closeScreenFixture(strings.Repeat("a", 64) + ":7")
			s := detail.closeScreen
			reviewClose(t, s, tc.force, tc.rate)
			before := s.View(67, 30)
			want := s.attempt.prepared.Request()
			if !strings.Contains(before, "Funding outpoint:") || !strings.Contains(before, "not a payout quote") {
				t.Fatal("missing confirmation identity/accounting")
			}
			if tc.force {
				if !strings.Contains(before, "Force close") || !strings.Contains(before, "sweeps add fees") {
					t.Fatal("force costs not disclosed")
				}
			} else if tc.rate == 0 && !strings.Contains(before, "auto (LND default)") {
				t.Fatal("automatic policy hidden")
			}
			if lipgloss.Width(before) > 67 || len(strings.Split(before, "\n")) > 30 {
				t.Fatal("review exceeds supported pane")
			}
			s.feeInput.SetSats(900)
			s.force = !tc.force
			s.chanPoint = strings.Repeat("b", 64) + ":2"
			s.peerAlias = "changed"
			s.localBal = 1
			s.HandleMsg(feeReply(t, &s.fees, s.ctx, 99))
			s.HandleMsg(tea.PasteMsg{Content: "999"})
			if s.View(67, 30) != before {
				t.Fatal("late input changed approved review")
			}
			cmd := submitClose(t, s)
			if _, again := s.HandleKey("enter", tea.KeyPressMsg{}); again != nil {
				t.Fatal("double confirmation scheduled another close")
			}
			s.HandleMsg(cmd())
			if len(c.sent) != 1 || c.sent[0] != want {
				t.Fatal("execution differed from review")
			}
		})
	}
}
func TestChannelCloseBackRequiresNewReview(t *testing.T) {
	for _, force := range []bool{false, true} {
		detail, _ := closeScreenFixture(strings.Repeat("a", 64) + ":0")
		s := detail.closeScreen
		reviewClose(t, s, force, 10)
		old := s.attempt
		s.HandleKey("backspace", tea.KeyPressMsg{})
		if s.attempt != nil || s.step == closeStepReview {
			t.Fatal("back retained approval")
		}
		if force {
			if s.step != closeStepType {
				t.Fatal("force back did not return to type")
			}
		} else {
			s.feeInput.SetSats(20)
			s.confirmBtnIdx = 1
			s.HandleKey("enter", tea.KeyPressMsg{})
			if s.step != closeStepReview || s.attempt == old || s.attempt.prepared.Request().SatPerVbyte != 20 {
				t.Fatal("edited policy did not get a new review")
			}
		}
	}
}
func TestChannelTabIdentitySurvivesReorderAndRemoval(t *testing.T) {
	a := channelInfo{Channel: lndrpc.Channel{ChannelPoint: strings.Repeat("a", 64) + ":0", RemotePubkey: strings.Repeat("c", 66), PeerAlias: "same peer", Capacity: 30000}}
	b := a
	b.ChannelPoint = strings.Repeat("b", 64) + ":0"
	ctx := &ScreenContext{Cfg: config.Default(), State: &RuntimeState{WalletKnown: true, WalletExists: true}, Status: &statusSnapshot{Channels: app.Observation[app.ChannelStatus]{Value: app.ChannelStatus{Channels: []channelInfo{a, b}}, ObservedAt: time.Now()}}}
	home := NewChannelsHomeScreen(ctx)
	home.focusZone = chanHomeZoneList
	m := Model{nav: NewNavSidebar(), screenCtx: ctx}
	m.nav.ActiveItem = secChannels
	open := func(row int) {
		t.Helper()
		home.cursor = row
		_, cmd := home.handleEnter()
		updated, _ := m.Update(cmd())
		m = updated.(Model)
	}
	open(1)
	first := m.tabs[0].Screen
	open(0)
	if len(m.tabs) != 2 || m.tabs[1].Key != a.ChannelPoint {
		t.Fatal("row zero reused a different channel")
	}
	ctx.Status.Channels.Value.Channels = []channelInfo{b, a}
	open(0)
	if len(m.tabs) != 2 || m.effectiveTabs()[m.activeTab].Screen != first {
		t.Fatal("reorder changed channel tab identity")
	}
	ctx.Status.Channels.Value.Channels = []channelInfo{a}
	stale := first.(*ChannelDetailScreen)
	if !strings.Contains(stale.View(67, 30), "Not in the latest successful list.") {
		t.Fatal("vanished channel still presented as available")
	}
	stale.launchClose()
	if stale.closeScreen != nil {
		t.Fatal("vanished channel launched a close")
	}
	updated, _ := m.closeTab(m.activeTab)
	m = updated.(Model)
	if len(m.tabs) != 1 || m.tabs[0].Key != a.ChannelPoint {
		t.Fatal("closing one channel tab removed its sibling")
	}
}
func TestChannelCloseOwnershipAcrossTabsAndSections(t *testing.T) {
	a, _ := closeScreenFixture(strings.Repeat("a", 64) + ":0")
	b, secondClient := closeScreenFixture(strings.Repeat("b", 64) + ":0")
	b.ctx, b.closeScreen.ctx = a.ctx, a.ctx
	secondClient.result = lndrpc.ChannelCloseResult{Submitted: true, Err: errors.New("close stream: deadline exceeded")}
	m := Model{nav: NewNavSidebar(), screenCtx: a.ctx, activeTab: 1, tabs: []openTab{
		{Kind: tabChannel, Key: a.point, Section: secChannels, Screen: a},
		{Kind: tabChannel, Key: b.point, Section: secChannels, Screen: b},
	}}
	m.nav.ActiveItem = secChannels
	m.Update(feeReply(t, &b.closeScreen.fees, b.ctx, 13))
	if a.closeScreen.fees.defaultRate(a.ctx.Cfg.Network) != 0 || b.closeScreen.fees.defaultRate(b.ctx.Cfg.Network) != 13 {
		t.Fatal("fee suggestion reached wrong tab")
	}
	reviewClose(t, a.closeScreen, false, 2)
	reviewClose(t, b.closeScreen, true, 0)
	first := submitClose(t, a.closeScreen)
	closed, _ := m.closeTab(1)
	if len(closed.(Model).tabs) != 2 {
		t.Fatal("submitted close tab could be removed")
	}
	replacement, _ := closeScreenFixture(a.point)
	a.ctx.invalidateWalletObservations()
	replacement.ctx, replacement.scope = a.ctx, a.ctx.walletObservationScope()
	updated, _ := m.Update(openTabMsg{Kind: tabChannel, Key: a.point, Screen: replacement, Replace: true})
	m = updated.(Model)
	if len(m.tabs) != 2 || m.tabs[0].Screen != a {
		t.Fatal("submitted close tab was replaced")
	}
	// Hide Channels while the first attempt finishes and another is only reviewed.
	m.nav.ActiveItem = secWallet
	result := first()
	updated, refresh := m.Update(result)
	m = updated.(Model)
	if a.closeScreen.step != closeStepResult || b.closeScreen.step != closeStepReview || refresh == nil {
		t.Fatal("hidden completion lost ownership")
	}
	second := submitClose(t, b.closeScreen)
	if _, cmd := m.Update(result); cmd != nil || b.closeScreen.step != closeStepClosing {
		t.Fatal("old completion affected another attempt")
	}
	// Done must remove its owning detail tab even after navigation changes.
	_, done := a.closeScreen.HandleKey("enter", tea.KeyPressMsg{})
	updated, _ = m.Update(done())
	m = updated.(Model)
	if len(m.tabs) != 1 || m.tabs[0].Screen != b {
		t.Fatal("Done removed the wrong tab")
	}
	updated, _ = m.Update(done())
	m = updated.(Model)
	if len(m.tabs) != 1 {
		t.Fatal("duplicate Done removed another tab")
	}
	updated, refresh = m.Update(second())
	m = updated.(Model)
	if b.closeScreen.step != closeStepResult || b.closeScreen.result.State != app.CloseOutcomeUnknown || refresh == nil {
		t.Fatal("second close lost completion")
	}
}
func TestChannelCloseResultsAreHonestAndFit(t *testing.T) {
	theme.Init(true)
	for _, tc := range []struct {
		name  string
		state app.ChannelCloseOutcome
		force bool
		want  string
	}{
		{"candidate", app.ClosePending, false, "Candidate closing TX"},
		{"confirmed", app.CloseConfirmed, true, "not full fund recovery"},
		{"unknown", app.CloseOutcomeUnknown, false, "repeating a close"},
		{"force unknown", app.CloseOutcomeUnknown, true, "repeating a close"},
		{"not submitted", app.CloseNotSubmitted, false, "Not Submitted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			detail, _ := closeScreenFixture(strings.Repeat("a", 64) + ":0")
			s := detail.closeScreen
			reviewClose(t, s, tc.force, 0)
			submitClose(t, s)
			result := app.ChannelCloseResult{State: tc.state, Txid: strings.Repeat("c", 64)}
			if tc.state == app.CloseOutcomeUnknown {
				result.Err = errors.New("close stream: deadline exceeded")
			}
			s.HandleMsg(channelCloseResultMsg{attempt: s.attempt, result: result})
			view := s.View(67, 30)
			if !strings.Contains(view, tc.want) || strings.Contains(view, "2,016") || strings.Contains(view, "Close Failed") {
				t.Fatalf("misleading close result: %s", view)
			}
			if lipgloss.Width(view) > 67 || len(strings.Split(view, "\n")) > 30 {
				t.Fatal("result exceeds supported pane")
			}
		})
	}
}
