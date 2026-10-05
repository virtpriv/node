package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/autounlock"
	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/lndrpc"
)

func autoUnlockTestContext(enabled bool) *ScreenContext {
	cfg := config.Default()
	cfg.AutoUnlock = enabled
	return &ScreenContext{
		Cfg:            cfg,
		State:          &RuntimeState{WalletKnown: true, WalletExists: true},
		ContentFocused: true,
	}
}

func TestAutoUnlockVerificationFailureReturnsToExistingForm(t *testing.T) {
	s := NewAutoUnlockScreen(autoUnlockTestContext(false))
	deliverAutoUnlock(t, s, app.AutoUnlockResult{Observation: app.AutoUnlockObserved, Transition: autounlock.Result{
		Outcome: autounlock.VerificationFailed,
	}})
	if s.state != auStateForm || s.pw1.Value() != "" || s.pw2.Value() != "" {
		t.Fatalf("retry form state=%v pw1=%q pw2=%q", s.state, s.pw1.Value(), s.pw2.Value())
	}
	view := s.View(82, 34)
	for _, want := range []string{
		"Configure Auto-Unlock", "VPN could not verify that password.",
		"LND is locked.", "Skip", "Confirm",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("retry view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Try Again") || strings.Contains(view, "Leave LND Locked") {
		t.Fatal("retry flow introduced unapproved replacement buttons")
	}
}

func TestAutoUnlockTimeoutCopyIsExplicitAndInconclusive(t *testing.T) {
	s := NewAutoUnlockScreen(autoUnlockTestContext(false))
	deliverAutoUnlock(t, s, app.AutoUnlockResult{Observation: app.AutoUnlockObserved, Transition: autounlock.Result{
		Outcome: autounlock.VerificationTimedOut,
	}})
	view := s.View(82, 34)
	for _, want := range []string{
		"did not become ready within 120 seconds",
		"could not determine whether the",
		"password was correct",
		"returned to the locked state",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("timeout view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "password was accepted") {
		t.Fatal("timeout copy makes an unproved password claim")
	}
}

func TestAutoUnlockRepairRequiredUsesApprovedMessage(t *testing.T) {
	s := NewAutoUnlockScreen(autoUnlockTestContext(false))
	deliverAutoUnlock(t, s, app.AutoUnlockResult{Observation: app.AutoUnlockObserved, Transition: autounlock.Result{
		Outcome:    autounlock.RepairRequired,
		FailedStep: "restore normal restart policy",
	}})
	view := s.View(82, 34)
	for _, want := range []string{
		"Repair Required",
		"VPN could not prove the auto-unlock state due to a system failure.",
		"Do not assume",
		"LND is online or that auto-unlock is correctly configured.",
		"Failed step: restore normal restart policy",
		"Done",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("repair view missing %q:\n%s", want, view)
		}
	}
}

func TestAutoUnlockHelperResponseFailureIsUnknownWithoutRawError(t *testing.T) {
	s := NewAutoUnlockScreen(autoUnlockTestContext(false))
	deliverAutoUnlock(t, s, app.AutoUnlockResult{Observation: app.AutoUnlockUnknown,
		Err: errors.New("injected socket detail"),
	})
	view := s.View(82, 34)
	for _, want := range []string{"Auto-Unlock Change Not Confirmed", "may still finish", "before retrying"} {
		if !strings.Contains(view, want) {
			t.Fatalf("helper failure view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "injected socket detail") {
		t.Fatal("raw helper error leaked into the user-facing repair message")
	}
}

func TestDisableRollbackReportsStillEnabled(t *testing.T) {
	ctx := autoUnlockTestContext(true)
	s := NewAutoUnlockScreen(ctx)
	deliverAutoUnlock(t, s, app.AutoUnlockResult{Observation: app.AutoUnlockObserved, Transition: autounlock.Result{
		Outcome: autounlock.StillEnabled,
	}})
	if !ctx.Cfg.AutoUnlock || s.state != auStateDoneOK {
		t.Fatalf("rollback state cfg=%v screen=%v", ctx.Cfg.AutoUnlock, s.state)
	}
	view := s.View(82, 34)
	for _, want := range []string{
		"Auto-Unlock Still Enabled", "previous enabled state was restored",
		"LND is online", "Done",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("rollback view missing %q:\n%s", want, view)
		}
	}
}

func TestSuccessfulDisableOffersReenableOnSameScreen(t *testing.T) {
	ctx := autoUnlockTestContext(true)
	s := NewAutoUnlockScreen(ctx)
	deliverAutoUnlock(t, s, app.AutoUnlockResult{Observation: app.AutoUnlockObserved, Transition: autounlock.Result{
		Outcome: autounlock.Disabled,
	}})
	view := s.View(82, 34)
	for _, want := range []string{"Auto-Unlock Disabled", "Done", "Re-enable"} {
		if !strings.Contains(view, want) {
			t.Fatalf("disabled view missing %q:\n%s", want, view)
		}
	}
	_, _ = s.HandleKey("right", tea.KeyPressMsg{})
	_, _ = s.HandleKey("enter", tea.KeyPressMsg{})
	if s.mode != autoUnlockEnable || s.state != auStateForm ||
		s.focusZone != auZoneInput1 {
		t.Fatalf("re-enable did not return to enable form: mode=%v state=%v focus=%v",
			s.mode, s.state, s.focusZone)
	}
}

func TestSystemServiceRowShowsLockedLND(t *testing.T) {
	ctx := autoUnlockTestContext(false)
	ctx.Status = &statusSnapshot{
		Services:    map[string]app.Observation[bool]{"lnd": {Value: true, ObservedAt: time.Now()}},
		WalletState: app.Observation[lndrpc.WalletState]{Value: lndrpc.WalletStateLocked, ObservedAt: time.Now()},
	}
	view := NewSystemHomeScreen(ctx).View(82, 34)
	if !strings.Contains(view, "lnd") || !strings.Contains(view, "locked") {
		t.Fatalf("LND service row does not expose locked state:\n%s", view)
	}
}

// Complete the actual screen submission using a local application stub.
func deliverAutoUnlock(t *testing.T, s *AutoUnlockScreen, result app.AutoUnlockResult) {
	t.Helper()
	fake := &autoUnlockStub{results: make(chan app.AutoUnlockResult, 1)}
	s.ctx.AutoUnlock = fake
	var cmd tea.Cmd
	if s.mode == autoUnlockEnable {
		s.pw1.SetValue("test wallet password")
		s.pw2.SetValue("test wallet password")
		_, cmd = s.tryConfirm()
	} else {
		_, cmd = s.HandleKey("enter", tea.KeyPressMsg{})
	}
	if cmd == nil || fake.calls != 1 {
		t.Fatal("operation did not start")
	}
	fake.results <- result
	s.HandleMsg(cmd())
}

type autoUnlockStub struct {
	calls    int
	enable   bool
	password autounlock.Password
	results  chan app.AutoUnlockResult
}

func (f *autoUnlockStub) Enable(password autounlock.Password) <-chan app.AutoUnlockResult {
	f.calls++
	f.enable = true
	f.password = password
	return f.results
}
func (f *autoUnlockStub) Disable() <-chan app.AutoUnlockResult {
	f.calls++
	f.enable = false
	return f.results
}
func (*autoUnlockStub) Close() {}

func TestAutoUnlockOwnsSubmissionAndHiddenCompletion(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "enable", true: "disable"}[enabled], func(t *testing.T) {
			ctx := autoUnlockTestContext(enabled)
			fake := &autoUnlockStub{results: make(chan app.AutoUnlockResult, 1)}
			ctx.AutoUnlock = fake
			s := NewAutoUnlockScreen(ctx)
			var cmd tea.Cmd
			if enabled {
				_, cmd = s.HandleKey("enter", tea.KeyPressMsg{})
			} else {
				s.pw1.SetValue("test wallet password")
				s.pw2.SetValue("different")
				if _, cmd := s.tryConfirm(); cmd != nil || fake.calls != 0 {
					t.Fatal("mismatched password submitted")
				}
				s.pw1.SetValue("test wallet password")
				s.pw2.SetValue("test wallet password")
				_, cmd = s.tryConfirm()
			}
			if cmd == nil || fake.calls != 1 || fake.enable == enabled ||
				(!enabled && fake.password.Text() != "test wallet password") ||
				s.pw1.Value() != "" || s.pw2.Value() != "" {
				t.Fatal("submission did not preserve the operation and clear the form")
			}
			s.HandleKey("enter", tea.KeyPressMsg{})
			if fake.calls != 1 {
				t.Fatal("duplicate confirmation submitted")
			}
			other := NewAutoUnlockScreen(ctx)
			other.state, other.attempt = auStateRunning, s.attempt
			m := Model{nav: NewNavSidebar(), screenCtx: ctx, tabs: []openTab{
				{Kind: tabAutoUnlock, Section: secSystem, Screen: s},
				{Kind: tabAutoUnlock, Section: secSystem, Screen: other},
			}}
			m.nav.ActiveItem = secSystem
			_, navigate := s.HandleKey("left", tea.KeyPressMsg{})
			if navigate == nil {
				t.Fatal("running screen blocks navigation")
			}
			updated, _ := m.Update(navigate())
			m = updated.(Model)
			m.nav.Cursor = secWallet
			updated, _ = m.handleSidebarKey("enter")
			m = updated.(Model)
			m.Update(autoUnlockDoneMsg{owner: s, attempt: s.attempt - 1})
			if !autoUnlockBusy(s) {
				t.Fatal("stale attempt completed the operation")
			}
			outcome := autounlock.Enabled
			if enabled {
				outcome = autounlock.Disabled
			}
			fake.results <- app.AutoUnlockResult{Observation: app.AutoUnlockObserved, Transition: autounlock.Result{Outcome: outcome}}
			result := cmd().(autoUnlockDoneMsg)
			other.HandleMsg(result)
			ctx.Cfg.P2PMode = "hybrid"
			updated, _ = m.Update(result)
			m = updated.(Model)
			if s.state != auStateDoneOK || ctx.Cfg.AutoUnlock == enabled || !autoUnlockBusy(other) || ctx.Cfg.P2PMode != "hybrid" {
				t.Fatal("hidden result lost, misrouted or overwrote unrelated settings")
			}
			result.result = app.AutoUnlockResult{Observation: app.AutoUnlockUnknown}
			m.Update(result)
			if s.state != auStateDoneOK || s.result.Outcome != outcome {
				t.Fatal("duplicate completion changed the result")
			}
			s.btnIdx = 0
			_, done := s.HandleKey("enter", tea.KeyPressMsg{})
			updated, _ = m.Update(done())
			m = updated.(Model)
			if len(m.tabs) != 1 || m.tabs[0].Screen != other {
				t.Fatal("Done closed another tab after navigation")
			}
			for _, late := range []tea.Msg{result, done()} {
				updated, _ = m.Update(late)
				m = updated.(Model)
				if len(m.tabs) != 1 || m.tabs[0].Screen != other || !autoUnlockBusy(other) {
					t.Fatal("closed owner's message changed a replacement")
				}
			}
		})
	}
}

func TestAutoUnlockBusyTabAndDelayedDone(t *testing.T) {
	ctx := autoUnlockTestContext(true)
	fake := &autoUnlockStub{results: make(chan app.AutoUnlockResult, 1)}
	ctx.AutoUnlock = fake
	s := NewAutoUnlockScreen(ctx)
	cancel := s.closeCommand()
	_, cmd := s.HandleKey("enter", tea.KeyPressMsg{})
	m := Model{nav: NewNavSidebar(), screenCtx: ctx, tabs: []openTab{{Kind: tabAutoUnlock, Section: secOnChain, Screen: s}}}
	m.nav.ActiveItem = secOnChain
	updated, _ := m.closeTab(1)
	m = updated.(Model)
	if len(m.tabs) != 1 {
		t.Fatal("running tab can be closed")
	}
	updated, _ = m.Update(cancel())
	m = updated.(Model)
	if len(m.tabs) != 1 {
		t.Fatal("delayed cancel discarded submitted operation")
	}
	m.nav.ActiveItem = secSystem
	updated, _ = m.Update(openTabMsg{Kind: tabAutoUnlock, Replace: true, Screen: NewAutoUnlockScreen(ctx)})
	m = updated.(Model)
	if len(m.tabs) != 1 || m.tabs[0].Screen != s || m.nav.ActiveSection() != secOnChain {
		t.Fatal("another entry point replaced or duplicated the owning tab")
	}
	fake.results <- app.AutoUnlockResult{Observation: app.AutoUnlockObserved, Transition: autounlock.Result{Outcome: autounlock.Disabled}}
	result := cmd().(autoUnlockDoneMsg)
	updated, _ = m.Update(result)
	m = updated.(Model)
	_, done := s.HandleKey("enter", tea.KeyPressMsg{})
	s.HandleKey("right", tea.KeyPressMsg{})
	s.HandleKey("enter", tea.KeyPressMsg{})
	updated, _ = m.Update(done())
	m = updated.(Model)
	updated, _ = m.Update(result)
	m = updated.(Model)
	if len(m.tabs) != 1 || m.tabs[0].Screen != s || s.state != auStateForm || s.mode != autoUnlockEnable {
		t.Fatal("old Done or result consumed the same-screen re-enable flow")
	}
}

func TestAutoUnlockEntryReturnsToUnfinishedWalletCreation(t *testing.T) {
	m, owner, _ := walletCreationFixture(t)
	deliverWallet(t, &m, walletFinalizedMsg{owner: owner, attempt: owner.attempt, revision: 1,
		result: app.WalletCreationResult{Presence: app.WalletPresent, SeedAcknowledged: true, Err: errors.New("staging unavailable")}})
	if !m.screenCtx.walletExists() || owner.step != walletResult {
		t.Fatal("fixture did not reach a created wallet with incomplete setup")
	}
	deliverWallet(t, &m, openTabMsg{Kind: tabAutoUnlock, Screen: NewAutoUnlockScreen(m.screenCtx)})
	if len(m.tabs) != 1 || m.tabs[0].Screen != owner || m.nav.ActiveSection() != secOnChain || m.activeTab != 1 {
		t.Fatal("auto-unlock entry bypassed the unfinished wallet owner")
	}
}

func TestAutoUnlockPasteAndByteValidation(t *testing.T) {
	for _, tc := range []struct {
		name, text, want string
	}{
		{"password manager LF", "wallet password\n", "wallet password"},
		{"spaces", " wallet password ", " wallet password "},
		{"multiline", "wallet\npassword", ""},
		{"CRLF", "wallet password\r\n", ""},
		{"tab", "wallet\tpassword", ""},
		{"NUL", "wallet\x00password", ""},
		{"truncation", strings.Repeat("x", 257), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, zone := range []int{auZoneInput1, auZoneInput2} {
				s := NewAutoUnlockScreen(autoUnlockTestContext(false))
				s.focusZone = zone
				s.HandleMsg(tea.PasteMsg{Content: tc.text})
				got := s.pw1.Value()
				if zone == auZoneInput2 {
					got = s.pw2.Value()
				}
				if got != tc.want || (s.errMsg != "") != (tc.want == "") {
					t.Fatal("masked paste silently changed or was incorrectly rejected")
				}
			}
		})
	}
	fake := &autoUnlockStub{results: make(chan app.AutoUnlockResult, 1)}
	ctx := autoUnlockTestContext(false)
	ctx.AutoUnlock = fake
	s := NewAutoUnlockScreen(ctx)
	s.pw1.SetValue(strings.Repeat("界", 171))
	s.pw2.SetValue(s.pw1.Value())
	if _, cmd := s.tryConfirm(); cmd != nil || fake.calls != 0 || s.errMsg == "" {
		t.Fatal("password exceeding the helper's byte limit was submitted")
	}
}
