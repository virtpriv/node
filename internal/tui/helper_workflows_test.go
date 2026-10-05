package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/p2p"
)

func progressFixture(t *testing.T, kind app.HelperWorkflowKind, ctx *ScreenContext) *InstallProgressScreen {
	// A closed owner supplies real operation identity and step metadata without
	// sending a request. Tests deliver controlled events through Model.Update.
	w := app.NewHelperWorkflows("2.1.1")
	w.Close()
	var op *app.HelperOperation
	switch kind {
	case app.SyncthingInstall:
		op = w.InstallSyncthing()
	case app.P2PUpgrade:
		op = w.UpgradeP2P(reviewedP2PRequest(t), nil)
	}
	s := NewInstallProgressScreen(ctx, op, nil, nil)
	s.Init()
	return s
}

func TestHelperProgressRoutesToOwnerAndRejectsReplay(t *testing.T) {
	ctx := &ScreenContext{Cfg: config.Default()}
	progress := progressFixture(t, app.SyncthingInstall, ctx)
	owner := &SyncthingInstallScreen{ctx: ctx, step: syncInstallProgress, progress: progress}
	idle := &P2PUpgradeScreen{ctx: ctx, step: p2pConfirm}
	m := Model{nav: NewNavSidebar(), screenCtx: ctx, tabs: []openTab{
		{Kind: tabP2PUpgrade, Section: secSystem, Screen: idle},
		{Kind: tabSyncthingInstall, Section: secAddons, Screen: owner},
	}}
	m.nav.ActiveItem = secWallet
	completed := 0
	progress.onDone = func() tea.Cmd { completed++; return nil }
	send := func(index int, result *app.HelperWorkflowResult) tea.Cmd {
		t.Helper()
		updated, cmd := m.Update(helperProgressMsg{operation: progress.operation, event: app.HelperProgress{Index: index, Result: result}})
		m = updated.(Model)
		return cmd
	}
	if send(0, &app.HelperWorkflowResult{HelperSucceeded: true}) != nil || progress.done {
		t.Fatal("success accepted before progress completed")
	}
	if send(-1, nil) != nil || send(1, nil) != nil {
		t.Fatal("out-of-order event scheduled another reader")
	}
	if send(0, nil) == nil || progress.current != 1 {
		t.Fatal("hidden owner lost progress to idle confirmation")
	}
	if send(0, nil) != nil || progress.current != 1 {
		t.Fatal("duplicate progress scheduled a second reader")
	}
	for i := 1; i < len(progress.steps); i++ {
		send(i, nil)
	}
	result := &app.HelperWorkflowResult{HelperSucceeded: true}
	send(len(progress.steps), result)
	send(len(progress.steps), result)
	if !progress.done || completed != 1 || idle.progress != nil {
		t.Fatal("completion ownership or replay guard failed")
	}
	orphan := progressFixture(t, app.SyncthingInstall, ctx)
	_, cmd := m.Update(helperProgressMsg{operation: orphan.operation, event: app.HelperProgress{Index: 0}})
	if cmd != nil {
		t.Fatal("removed operation reached another screen")
	}
}

func TestHelperActiveTabAndDelayedClose(t *testing.T) {
	ctx := &ScreenContext{Cfg: config.Default()}
	progress := progressFixture(t, app.SyncthingInstall, ctx)
	owner := &SyncthingInstallScreen{ctx: ctx, step: syncInstallProgress, progress: progress}
	parent := NewAddonsHomeScreen(ctx)
	m := Model{nav: NewNavSidebar(), screenCtx: ctx, activeTab: 2, tabs: []openTab{
		{Kind: tabSyncthing, Section: secAddons, Screen: parent},
		{Kind: tabSyncthingInstall, Parent: tabSyncthing, Section: secAddons, Screen: owner},
	}}
	m.nav.ActiveItem = secAddons
	for _, index := range []int{1, 2} {
		updated, _ := m.closeTab(index)
		if len(updated.(Model).tabs) != 2 {
			t.Fatal("active operation was closed directly or through its parent")
		}
	}
	updated, _ := m.Update(openTabMsg{Kind: tabSyncthingInstall, Replace: true, Screen: NewSyncthingInstallScreen(ctx)})
	m = updated.(Model)
	if m.tabs[1].Screen != owner {
		t.Fatal("active operation was replaced")
	}
	progress.HandleMsg(helperProgressMsg{operation: progress.operation, event: app.HelperProgress{Result: &app.HelperWorkflowResult{Err: errors.New("lost connection")}}})
	_, closeCmd := progress.HandleKey("enter", tea.KeyPressMsg{})
	// The user navigates elsewhere before the Done command is delivered.
	m.nav.ActiveItem = secSystem
	m.activeTab = 1
	other := NewNodeUpdateScreen(ctx)
	m.tabs = append(m.tabs, openTab{Kind: tabNodeUpdates, Section: secSystem, Screen: other})
	updated, _ = m.Update(closeCmd())
	m = updated.(Model)
	if len(m.tabs) != 2 || m.tabs[1].Screen != other {
		t.Fatal("delayed Done closed the newly focused tab")
	}
	updated, _ = m.Update(closeCmd())
	if len(updated.(Model).tabs) != 2 {
		t.Fatal("duplicate Done removed another tab")
	}
}

func TestHelperConfigurationPublicationPreservesNewerSettings(t *testing.T) {
	ctx := &ScreenContext{Cfg: config.Default()}
	progress := progressFixture(t, app.SyncthingInstall, ctx)
	fresh := *ctx.Cfg
	fresh.SyncthingEnabled = true
	// This newer auto-unlock result arrives after the application read its file.
	ctx.Cfg.AutoUnlock = true
	for i := range progress.steps {
		progress.HandleMsg(helperProgressMsg{operation: progress.operation, event: app.HelperProgress{Index: i}})
	}
	progress.HandleMsg(helperProgressMsg{operation: progress.operation, event: app.HelperProgress{Index: len(progress.steps), Result: &app.HelperWorkflowResult{HelperSucceeded: true, Config: &fresh}}})
	if !ctx.Cfg.AutoUnlock || !ctx.Cfg.SyncthingEnabled {
		t.Fatal("delayed reload replaced an unrelated newer setting")
	}
	ctx.Cfg.SyncthingEnabled = false
	progress.HandleMsg(helperProgressMsg{operation: progress.operation, event: app.HelperProgress{Index: len(progress.steps), Result: &app.HelperWorkflowResult{Config: &fresh}}})
	if ctx.Cfg.SyncthingEnabled {
		t.Fatal("replayed completion republished stale configuration")
	}
}

func TestHelperFailureDoesNotClaimRollback(t *testing.T) {
	for _, succeeded := range []bool{false, true} {
		ctx := &ScreenContext{Cfg: config.Default()}
		s := progressFixture(t, app.P2PUpgrade, ctx)
		called := 0
		s.onFail = func() tea.Cmd { called++; return nil }
		message := helperProgressMsg{operation: s.operation, event: app.HelperProgress{Result: &app.HelperWorkflowResult{HelperSucceeded: succeeded, Err: errors.New("injected failure")}}}
		s.HandleMsg(message)
		s.HandleMsg(message)
		view := s.View(82, 34)
		want := "Changes may have been applied"
		if succeeded {
			want = "helper completed the change"
		}
		if !s.failed || !s.done || called != 1 || !strings.Contains(view, want) || ctx.Cfg.P2PMode != "tor" {
			t.Fatalf("failure lost its outcome or repeated completion: %s", view)
		}
	}
}

func reviewedP2PRequest(t *testing.T) p2p.UpgradeRequest {
	t.Helper()
	request, err := p2p.NewUpgradeRequest("203.0.113.7")
	if err != nil {
		t.Fatal(err)
	}
	return request
}
