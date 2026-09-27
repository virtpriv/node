package tui

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/virtualprivatenode/vpn/internal/app"
	"github.com/virtualprivatenode/vpn/internal/sshkeys"
	"github.com/virtualprivatenode/vpn/internal/theme"
)

type screenSSHAuth struct {
	desired []bool
	enabled bool
	err     error
}

func (f *screenSSHAuth) PasswordAuth() (bool, error) { return f.enabled, f.err }
func (f *screenSSHAuth) SetPasswordAuthDisabled(disabled bool) error {
	f.desired = append(f.desired, disabled)
	return f.err
}
func sshScreenContext(t *testing.T) (*ScreenContext, *screenSSHAuth) {
	t.Helper()
	auth := &screenSSHAuth{enabled: true}
	ctx := &ScreenContext{State: &RuntimeState{SSHPasswordAuthKnown: true}, SSHAccess: &app.SSHAccess{Keys: sshkeys.Store{Path: filepath.Join(t.TempDir(), "authorized_keys")}, Auth: auth}, HasTabs: true, ContentFocused: true}
	return ctx, auth
}

const screenKeyA = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEB"
const screenKeyB = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgIC"

func TestSSHPasswordConfirmationFreezesIntentAndRejectsOldResults(t *testing.T) {
	theme.Init(true)
	for _, tc := range []struct {
		name                          string
		initialDisabled, wantDisabled bool
		heading                       string
	}{
		{"disable", false, true, "Disable password auth?"},
		{"enable", true, false, "Enable password auth?"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, auth := sshScreenContext(t)
			ctx.State.SSHPasswordAuthDisabled = tc.initialDisabled
			auth.enabled = !tc.initialDisabled
			s := NewSSHPasswordAuthScreen(ctx)
			s.viewBtnIdx = 1
			s.HandleKey("enter", tea.KeyPressMsg{})
			before := s.View(67, 30)
			if s.step != sshPwAuthStepConfirm || !strings.Contains(before, tc.heading) {
				t.Fatal("missing intended review")
			}
			ctx.State.SSHPasswordAuthDisabled = tc.wantDisabled
			if s.View(67, 30) != before {
				t.Fatal("late observation changed confirmed intent")
			}
			s.confirmIdx = 1
			_, cmd := s.HandleKey("enter", tea.KeyPressMsg{})
			if cmd == nil || !sshAccessBusy(s) {
				t.Fatal("not submitted")
			}
			if _, again := s.HandleKey("enter", tea.KeyPressMsg{}); again != nil {
				t.Fatal("double submit")
			}
			s.HandleMsg(sshPwAuthDoneMsg{owner: s, attempt: s.attempt - 1})
			if !sshAccessBusy(s) {
				t.Fatal("old result completed operation")
			}
			result := cmd()
			s.HandleMsg(result)
			if len(auth.desired) != 1 || auth.desired[0] != tc.wantDisabled || s.step != sshPwAuthStepResult || !ctx.State.SSHPasswordAuthKnown || ctx.State.SSHPasswordAuthDisabled != tc.wantDisabled {
				t.Fatal("request/result did not match confirmed intent")
			}
			s.HandleMsg(sshPwAuthDoneMsg{owner: s, attempt: s.attempt, err: errors.New("late duplicate")})
			if s.resultErr != "" {
				t.Fatal("duplicate completion changed result")
			}
		})
	}
}

func TestSSHReadRevisionCannotOverwriteMutationOrNewerRead(t *testing.T) {
	ctx, _ := sshScreenContext(t)
	s := NewSSHPasswordAuthScreen(ctx)
	m := Model{nav: NewNavSidebar(), screenCtx: ctx, state: ctx.State, tabs: []openTab{{Kind: tabSSHPasswordAuth, Section: secSystem, Screen: s}}}
	updated, read1 := m.Update(refreshSSHAuthMsg{})
	m = updated.(Model)
	updated, read2 := m.Update(refreshSSHAuthMsg{})
	m = updated.(Model)
	latest := read2().(sshPwAuthStateMsg)
	latest.disabled = true
	updated, _ = m.Update(latest)
	m = updated.(Model)
	updated, _ = m.Update(read1())
	m = updated.(Model)
	if !ctx.State.SSHPasswordAuthDisabled {
		t.Fatal("older read won")
	}
	s.step = sshPwAuthStepWorking
	s.targetDisabled = true
	command := s.setCommand()
	updated, _ = m.Update(latest)
	m = updated.(Model)
	if ctx.State.SSHPasswordAuthKnown {
		t.Fatal("pre-mutation observation became current")
	}
	m.Update(command())
	if !ctx.State.SSHPasswordAuthKnown || s.step != sshPwAuthStepResult {
		t.Fatal("hidden-tab completion was lost")
	}
}

func TestSSHListOwnsReadsAndSelectionByFingerprint(t *testing.T) {
	ctx, _ := sshScreenContext(t)
	a, _ := app.ParseSSHKey(screenKeyA)
	b, _ := app.ParseSSHKey(screenKeyB)
	s := NewSSHKeysScreen(ctx)
	s.keys = []app.SSHKey{a, b}
	s.keyCursor = 1
	s.focusZone = sshZoneKeys
	s.refresh()
	s.refresh()
	s.HandleMsg(sshKeysListMsg{owner: s, request: s.request, keys: []app.SSHKey{b, a}})
	if s.keyCursor != 0 || s.keys[s.keyCursor].Fingerprint != b.Fingerprint {
		t.Fatal("reorder changed selected key")
	}
	s.HandleMsg(sshKeysListMsg{owner: s, request: s.request - 1, keys: []app.SSHKey{a}})
	if len(s.keys) != 2 {
		t.Fatal("old list replaced current keys")
	}
	// The same request number on a different screen cannot refresh this one.
	s.HandleMsg(sshKeysListMsg{owner: NewSSHKeysScreen(ctx), request: s.request})
	if len(s.keys) != 2 {
		t.Fatal("foreign list result accepted")
	}
	s.refresh()
	s.HandleMsg(sshKeysListMsg{owner: s, request: s.request, keys: []app.SSHKey{a}})
	if s.focusZone != sshZoneButtons {
		t.Fatal("removed selection silently became another key")
	}
}

func TestSSHKeyTabsUseFingerprintAndRouteRemovalToOwner(t *testing.T) {
	ctx, _ := sshScreenContext(t)
	for _, line := range []string{screenKeyA, screenKeyB} {
		if err := ctx.SSHAccess.AddKey(line); err != nil {
			t.Fatal(err)
		}
	}
	a, _ := app.ParseSSHKey(screenKeyA)
	b, _ := app.ParseSSHKey(screenKeyB)
	home := NewSSHKeysScreen(ctx)
	home.keys = []app.SSHKey{a, b}
	m := Model{nav: NewNavSidebar(), screenCtx: ctx, state: ctx.State}
	m.nav.ActiveItem = secSystem
	open := func(row int) {
		t.Helper()
		home.keyCursor = row
		_, cmd := home.openDetailTab()
		updated, _ := m.Update(cmd())
		m = updated.(Model)
	}
	open(1)
	second := m.tabs[0].Screen.(*SSHKeyDetailScreen)
	open(0)
	first := m.tabs[1].Screen.(*SSHKeyDetailScreen)
	home.keys = []app.SSHKey{b, a}
	open(0)
	if len(m.tabs) != 2 || m.tabs[0].Screen != second || m.tabs[0].Key != b.Fingerprint {
		t.Fatal("row reorder reused wrong tab")
	}
	second.step = sshKeyDetailStepConfirm
	second.confirmIdx = 1
	_, command := second.HandleKey("enter", tea.KeyPressMsg{})
	m.nav.ActiveItem = secOnChain
	updated, done := m.Update(command())
	m = updated.(Model)
	if !second.removed || first.removed || first.step != sshKeyDetailStepView {
		t.Fatal("result did not reach the exact hidden tab")
	}
	if done == nil {
		t.Fatal("missing close/refresh")
	}
	// Deliver the actual completion commands after navigation has changed.
	batch, ok := done().(tea.BatchMsg)
	if !ok {
		t.Fatal("missing close/refresh batch")
	}
	refreshed := false
	for _, cmd := range batch {
		msg := cmd()
		if _, ok := msg.(refreshSSHKeysMsg); ok {
			refreshed = true
		}
		updated, _ = m.Update(msg)
		m = updated.(Model)
	}
	if !refreshed {
		t.Fatal("parent refresh was not requested")
	}
	if len(m.tabs) != 1 || m.tabs[0].Screen != first {
		t.Fatal("wrong key tab closed")
	}
	updated, _ = m.Update(sshKeyRemoveMsg{owner: second, attempt: second.attempt, err: errors.New("late")})
	m = updated.(Model)
	if first.removeErr != "" {
		t.Fatal("closed-screen result reached another key")
	}
	keys, err := ctx.SSHAccess.ListKeys()
	if err != nil || len(keys) != 1 || keys[0].Fingerprint != a.Fingerprint {
		t.Fatal("wrong credential removed")
	}
}

func TestSSHBusyTabsSurviveCloseParentAndReplacement(t *testing.T) {
	ctx, _ := sshScreenContext(t)
	for _, screen := range []Screen{&ChangePasswordScreen{ctx: ctx, step: changePwStepWorking}, &SSHKeyAddScreen{ctx: ctx, step: sshAddStepWorking}, &SSHKeyDetailScreen{ctx: ctx, step: sshKeyDetailStepWorking}, &SSHPasswordAuthScreen{ctx: ctx, step: sshPwAuthStepWorking}} {
		parent := NewSSHKeysScreen(ctx)
		m := Model{nav: NewNavSidebar(), screenCtx: ctx, state: ctx.State, tabs: []openTab{{Kind: tabAccounts, Section: secSystem, Screen: NewAccountsScreen(ctx)}, {Kind: tabSSHKeys, Section: secSystem, Parent: tabAccounts, Screen: parent}, {Kind: tabSSHKeyAdd, Section: secSystem, Parent: tabSSHKeys, Screen: screen}}}
		m.nav.ActiveItem = secSystem
		for _, index := range []int{1, 2, 3} {
			updated, _ := m.closeTab(index)
			m = updated.(Model)
			if len(m.tabs) != 3 {
				t.Fatal("busy child discarded")
			}
		}
		updated, _ := m.Update(openTabMsg{Kind: tabSSHKeys, Screen: NewSSHKeysScreen(ctx), Replace: true})
		m = updated.(Model)
		if m.tabs[1].Screen != parent {
			t.Fatal("busy parent replaced")
		}
		updated, _ = m.Update(closeSSHScreenMsg{screen: screen})
		m = updated.(Model)
		if len(m.tabs) != 3 {
			t.Fatal("owned close discarded pending operation")
		}
		if add, ok := screen.(*SSHKeyAddScreen); ok {
			updated, _ = m.Update(sshKeyAddMsg{owner: add, attempt: add.attempt})
			m = updated.(Model)
			updated, _ = m.closeTab(1)
			if len(updated.(Model).tabs) != 0 {
				t.Fatal("closing Accounts left orphaned key tabs after completion")
			}
		}
	}
}

func TestSSHAddFreezesInputAndRoutesOnlyItsAttempt(t *testing.T) {
	ctx, _ := sshScreenContext(t)
	s := NewSSHKeyAddScreen(ctx)
	s.keyInput.SetValue(screenKeyA)
	_, cmd := s.submit()
	s.keyInput.SetValue(screenKeyB)
	result := cmd().(sshKeyAddMsg)
	other := NewSSHKeyAddScreen(ctx)
	other.step = sshAddStepWorking
	other.attempt = result.attempt
	other.HandleMsg(result)
	if other.step != sshAddStepWorking {
		t.Fatal("foreign add result accepted")
	}
	s.HandleMsg(sshKeyAddMsg{owner: s, attempt: s.attempt - 1})
	if s.step != sshAddStepWorking {
		t.Fatal("old add result accepted")
	}
	s.HandleMsg(result)
	keys, err := ctx.SSHAccess.ListKeys()
	a, _ := app.ParseSSHKey(screenKeyA)
	if err != nil || len(keys) != 1 || keys[0].Fingerprint != a.Fingerprint || s.step != sshAddStepResult {
		t.Fatal("input changed pending add")
	}
	// A new input screen must reject multi-key paste without retaining an old key.
	input := NewSSHKeyAddScreen(ctx)
	input.keyInput.SetValue(screenKeyA)
	input.HandleMsg(tea.PasteMsg{Content: screenKeyA + "\n" + screenKeyB})
	if input.keyInput.Value() != "" || input.addErr == "" {
		t.Fatal("multi-key paste kept an actionable old key")
	}
}
