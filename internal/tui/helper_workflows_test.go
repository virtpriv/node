package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/virtpriv/node/internal/api"
	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/p2p"
)

func progressFixture(t *testing.T, kind app.WorkflowKind, ctx *ScreenContext) *InstallProgressScreen {
	// A closed owner supplies real operation identity and step names without
	// sending a request. Tests deliver controlled events through Model.Update.
	w := app.NewWorkflows("2.1.1")
	w.Close()
	var op *app.Operation
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

// look is the record of a running Syncthing install at step n.
func look(n int) api.Job {
	now := time.Now()
	return api.Job{ID: "20261109-100000-aaaaaa", Kind: api.KindSyncthingInstall, Title: "Install Syncthing", State: api.JobStarted,
		Steps: api.SyncthingInstallStepNames("2.1.1"), StepNumber: n, StepStarts: []time.Time{now}}
}

func TestProgressRoutesToOwnerAndRejectsReplay(t *testing.T) {
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
	send := func(event app.WorkflowProgress) tea.Cmd {
		t.Helper()
		updated, cmd := m.Update(workflowProgressMsg{operation: progress.operation, event: event})
		m = updated.(Model)
		return cmd
	}
	if send(app.WorkflowProgress{Job: look(3)}) == nil || progress.job.StepNumber != 3 {
		t.Fatal("hidden owner lost progress to idle confirmation")
	}
	result := &app.WorkflowResult{JobDone: true, Outcome: app.JobCompleted}
	send(app.WorkflowProgress{Result: result})
	send(app.WorkflowProgress{Result: result})
	if !progress.done || completed != 1 || idle.progress != nil {
		t.Fatal("completion ownership or replay guard failed")
	}
	orphan := progressFixture(t, app.SyncthingInstall, ctx)
	m.Update(workflowProgressMsg{operation: orphan.operation, event: app.WorkflowProgress{Job: look(5)}})
	if progress.job.StepNumber != 3 {
		t.Fatal("removed operation reached another screen")
	}
}

// S6: a Syncthing install or P2P switch runs in its own job, so its tab may
// be closed while it runs. Replacing the screen is still refused.
func TestProgressTabClosesWhileItRunsAndDelayedDone(t *testing.T) {
	ctx := &ScreenContext{Cfg: config.Default()}
	progress := progressFixture(t, app.SyncthingInstall, ctx)
	owner := &SyncthingInstallScreen{ctx: ctx, step: syncInstallProgress, progress: progress}
	parent := NewAddonsHomeScreen(ctx)
	m := Model{nav: NewNavSidebar(), screenCtx: ctx, activeTab: 2, tabs: []openTab{
		{Kind: tabSyncthing, Section: secAddons, Screen: parent},
		{Kind: tabSyncthingInstall, Parent: tabSyncthing, Section: secAddons, Screen: owner},
	}}
	m.nav.ActiveItem = secAddons
	updated, _ := m.Update(openTabMsg{Kind: tabSyncthingInstall, Replace: true, Screen: NewSyncthingInstallScreen(ctx)})
	if updated.(Model).tabs[1].Screen != owner {
		t.Fatal("running operation was replaced")
	}
	closed, _ := m.closeTab(2)
	if len(closed.(Model).tabs) != 1 {
		t.Fatal("a running job's tab could not be closed")
	}
	progress.HandleMsg(workflowProgressMsg{operation: progress.operation, event: app.WorkflowProgress{Result: &app.WorkflowResult{Outcome: app.JobUnconfirmed, Err: errors.New("lost connection")}}})
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

func TestProgressConfigurationPublicationPreservesNewerSettings(t *testing.T) {
	ctx := &ScreenContext{Cfg: config.Default()}
	progress := progressFixture(t, app.SyncthingInstall, ctx)
	fresh := *ctx.Cfg
	fresh.SyncthingEnabled = true
	// This newer auto-unlock result arrives after the application read its file.
	ctx.Cfg.AutoUnlock = true
	progress.HandleMsg(workflowProgressMsg{operation: progress.operation, event: app.WorkflowProgress{Result: &app.WorkflowResult{JobDone: true, Outcome: app.JobCompleted, Config: &fresh}}})
	if !ctx.Cfg.AutoUnlock || !ctx.Cfg.SyncthingEnabled {
		t.Fatal("delayed reload replaced an unrelated newer setting")
	}
	ctx.Cfg.SyncthingEnabled = false
	progress.HandleMsg(workflowProgressMsg{operation: progress.operation, event: app.WorkflowProgress{Result: &app.WorkflowResult{Config: &fresh}}})
	if ctx.Cfg.SyncthingEnabled {
		t.Fatal("replayed completion republished stale configuration")
	}
}

// A failure says what is known about the node: the job completed, nothing
// was changed, the change was not confirmed, or changes may have been
// applied. Only the record's word that nothing changed lifts the warning.
func TestProgressFailureDoesNotClaimRollback(t *testing.T) {
	for _, tc := range []struct {
		result app.WorkflowResult
		want   string
	}{
		{app.WorkflowResult{Outcome: app.JobFailed}, "Changes may have been applied"},
		{app.WorkflowResult{JobDone: true, Outcome: app.JobCompleted}, "could not read the node"},
		{app.WorkflowResult{Outcome: app.JobNotStarted}, "stopped before changing anything"},
		{app.WorkflowResult{Outcome: app.JobUnconfirmed}, "was not confirmed"},
	} {
		ctx := &ScreenContext{Cfg: config.Default()}
		s := progressFixture(t, app.P2PUpgrade, ctx)
		called := 0
		s.onFail = func() tea.Cmd { called++; return nil }
		tc.result.Err = errors.New("injected failure")
		message := workflowProgressMsg{operation: s.operation, event: app.WorkflowProgress{Result: &tc.result}}
		s.HandleMsg(message)
		s.HandleMsg(message)
		view := s.View(82, 34)
		if !s.failed() || !s.done || called != 1 || !strings.Contains(view, tc.want) || ctx.Cfg.P2PMode != "tor" {
			t.Fatalf("failure lost its outcome or repeated completion: %s", view)
		}
		if tc.result.Outcome == app.JobNotStarted && strings.Contains(view, "may have been applied") {
			t.Fatalf("a refusal before any change still warns of changes: %s", view)
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
