package tui

import (
	"context"
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/lndrpc"
)

func walletCreationFixture(t *testing.T) (Model, *WalletCreateScreen, *int) {
	t.Helper()
	ctx := &ScreenContext{Cfg: config.Default(), State: &RuntimeState{WalletKnown: true}}
	ctx.WalletCreation = app.NewWalletCreation()
	t.Cleanup(ctx.WalletCreation.Close)
	opened := new(int)
	ctx.WalletRuntime = &stubWalletRuntime{open: func(_ context.Context, staged bool) app.WalletClientResult {
		if !staged {
			t.Error("creation initialization requested ordinary probe/repair")
		}
		*opened++
		return app.WalletClientResult{Client: &lndrpc.Client{}}
	}}
	s := NewWalletCreateScreen(ctx)
	s.attempt = &walletCreationAttempt{finalization: 1, execution: app.WalletExecution{Created: true, SeedAcknowledged: true}}
	s.step = walletFinalizing
	ctx.walletCreationOwner = s
	m := Model{cfg: ctx.Cfg, state: ctx.State, screenCtx: ctx, nav: NewNavSidebar(), activeTab: 1,
		tabs: []openTab{{Kind: tabWalletCreate, Section: secOnChain, Screen: s}}}
	m.nav.ActiveItem = secSystem
	return m, s, opened
}

func deliverWallet(t *testing.T, m *Model, msg tea.Msg) tea.Cmd {
	t.Helper()
	updated, cmd := m.Update(msg)
	*m = updated.(Model)
	return cmd
}

func TestWalletFinalizationRoutesFailureAndRetryToHiddenOwner(t *testing.T) {
	m, s, opened := walletCreationFixture(t)
	s.step = walletConfirm
	m.screenCtx.walletCreationOwner = nil
	staleRead := walletObservationMsg(t, &m, false, nil)
	s.startWaitingForLND()
	s.step = walletFinalizing
	s.attempt.finalization = 1
	failure := walletFinalizedMsg{owner: s, attempt: s.attempt, revision: 1,
		result: app.WalletCreationResult{Presence: app.WalletPresent, SeedAcknowledged: true, Err: errors.New("staging failed")}}
	deliverWallet(t, &m, failure)
	if s.step != walletResult || !m.state.WalletExists || !m.state.WalletKnown || *opened != 0 {
		t.Fatal("staging failure lost or client initialized early")
	}
	oldDone := s.closeCmd()()
	s.btnIdx = 1
	_, cmd := s.HandleKey("enter", tea.KeyPressMsg{})
	if cmd == nil || s.step != walletFinalizing || s.attempt.finalization != 2 {
		t.Fatal("retry did not schedule finalization")
	}
	// Do not run a helper here. Controlled completions prove dispatch ownership.
	deliverWallet(t, &m, failure)
	if s.step != walletFinalizing {
		t.Fatal("old failure completed a newer retry")
	}
	failure.revision = 2
	deliverWallet(t, &m, failure)
	// Deliver Done after the retry finishes so the busy guard cannot hide a
	// missing revision check. This result must stay available for recovery.
	deliverWallet(t, &m, oldDone)
	if len(m.tabs) != 1 || s.step != walletResult {
		t.Fatal("old Done discarded the newer result")
	}
	s.btnIdx = 1
	if _, cmd := s.HandleKey("enter", tea.KeyPressMsg{}); cmd == nil {
		t.Fatal("newer failure lost its recovery action")
	}
	success := walletFinalizedMsg{owner: s, attempt: s.attempt, revision: 3,
		result: app.WalletCreationResult{Presence: app.WalletPresent, CredentialsStaged: true, SeedAcknowledged: true}}
	cmd = deliverWallet(t, &m, success)
	if cmd == nil || s.step != walletInitializing || *opened != 0 {
		t.Fatal("staged initialization did not remain asynchronous")
	}
	deliverWallet(t, &m, cmd())
	if m.tabs[0].Kind != tabAutoUnlock || m.tabs[0].Section != secOnChain || *opened != 1 || m.screenCtx.walletCreationOwner != nil {
		t.Fatal("success did not transform its owning tab exactly once")
	}
	deliverWallet(t, &m, success)
	deliverWallet(t, &m, staleRead)
	if !m.state.WalletExists || !m.state.WalletKnown || *opened != 1 {
		t.Fatal("stale completion or observation changed the created wallet")
	}
}

func TestWalletAttemptsAndNavigation(t *testing.T) {
	m, s, opened := walletCreationFixture(t)
	s.step = walletConfirm
	m.screenCtx.walletCreationOwner = nil
	read := walletObservationMsg(t, &m, true, nil)
	if s.startWaitingForLND() == nil {
		t.Fatal("known absence did not admit creation")
	}
	ready := walletLNDReadyMsg{owner: s, attempt: s.attempt, network: "signet"}
	if cmd := deliverWallet(t, &m, ready); cmd == nil || s.step != walletExec {
		t.Fatal("hidden readiness result lost")
	}
	if cmd := deliverWallet(t, &m, ready); cmd != nil {
		t.Fatal("duplicate readiness started another terminal command")
	}
	if cmd := deliverWallet(t, &m, fetchWalletStateCmd(m.screenCtx)()); cmd != nil {
		t.Fatal("ordinary read admitted during wallet creation")
	}
	deliverWallet(t, &m, read)
	if m.state.WalletExists || *opened != 0 {
		t.Fatal("poll published wallet during creation")
	}
	deliverWallet(t, &m, closeWalletCreateMsg{owner: s})
	if len(m.tabs) != 1 {
		t.Fatal("active wallet tab closed")
	}
	other := NewWalletCreateScreen(m.screenCtx)
	deliverWallet(t, &m, openTabMsg{Kind: tabWalletCreate, Replace: true, Screen: other})
	if len(m.tabs) != 1 || m.tabs[0].Screen != s || m.nav.ActiveSection() != secOnChain {
		t.Fatal("another section replaced or duplicated creation")
	}
	deliverWallet(t, &m, closeTabMsg{})
	if len(m.tabs) != 1 {
		t.Fatal("tab-bar close removed active creation")
	}
	if cmd := deliverWallet(t, &m, walletExecDoneMsg{owner: other, attempt: s.attempt}); cmd != nil {
		t.Fatal("orphan result reached owner")
	}
	oldAttempt := &walletCreationAttempt{}
	if cmd := deliverWallet(t, &m, walletExecDoneMsg{owner: s, attempt: oldAttempt}); cmd != nil {
		t.Fatal("old attempt reached owner")
	}
	if cmd := deliverWallet(t, &m, walletExecDoneMsg{owner: s, attempt: s.attempt, execution: app.WalletExecution{Created: true, SeedAcknowledged: true}}); cmd == nil || s.step != walletFinalizing {
		t.Fatal("execution completion did not schedule finalization")
	}
	if cmd := deliverWallet(t, &m, walletExecDoneMsg{owner: s, attempt: s.attempt}); cmd != nil {
		t.Fatal("duplicate execution repeated staging")
	}
}

func TestWalletDelayedDoneAndTerminalFailure(t *testing.T) {
	m, s, _ := walletCreationFixture(t)
	result := app.WalletCreationResult{Presence: app.WalletPresent, CredentialsStaged: true, SeedAcknowledged: true, Err: errors.New("terminal restoration failed")}
	cmd := deliverWallet(t, &m, walletFinalizedMsg{owner: s, attempt: s.attempt, revision: 1, result: result})
	deliverWallet(t, &m, cmd())
	if s.step != walletResult || m.tabs[0].Kind != tabWalletCreate || !s.canContinue() {
		t.Fatal("terminal error hid a created wallet or automatically advanced")
	}
	_, done := s.HandleKey("enter", tea.KeyPressMsg{})
	other := NewWalletCreateScreen(m.screenCtx)
	m.tabs = append(m.tabs, openTab{Kind: tabNodeUpdates, Section: secSystem, Screen: other})
	m.activeTab = 1
	deliverWallet(t, &m, done())
	deliverWallet(t, &m, done())
	if len(m.tabs) != 1 || m.tabs[0].Screen != other || m.screenCtx.walletCreationOwner != nil {
		t.Fatal("delayed Done affected the newly active tab")
	}
}

func TestWalletClientFailureDoesNotEraseStagingSuccess(t *testing.T) {
	m, s, _ := walletCreationFixture(t)
	m.screenCtx.WalletRuntime.(*stubWalletRuntime).open = func(context.Context, bool) app.WalletClientResult {
		return app.WalletClientResult{Err: errors.New("staged file unreadable")}
	}
	r := app.WalletCreationResult{Presence: app.WalletPresent, CredentialsStaged: true, SeedAcknowledged: true}
	cmd := deliverWallet(t, &m, walletFinalizedMsg{owner: s, attempt: s.attempt, revision: 1, result: r})
	deliverWallet(t, &m, cmd())
	if !s.result.CredentialsStaged || !s.retrySetup() || s.canContinue() || s.result.Err == nil {
		t.Fatal("client initialization failure was misclassified")
	}
}

func TestWalletReadinessFailureCannotRetryCreationThroughSetup(t *testing.T) {
	m, s, _ := walletCreationFixture(t)
	s.step = walletWaiting
	s.attempt = &walletCreationAttempt{}
	deliverWallet(t, &m, walletLNDReadyMsg{owner: s, attempt: s.attempt, err: errors.New("wallet already exists")})
	if s.step != walletResult || s.hasRecoveryAction() {
		t.Fatal("readiness refusal offered a finalization retry")
	}
}
