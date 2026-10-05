package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/virtpriv/node/internal/theme"
)

func TestPasswordFailureDistinguishesUnchangedFromUnconfirmed(t *testing.T) {
	theme.Init(true)
	for _, started := range []bool{false, true} {
		t.Run(map[bool]string{false: "not started", true: "started but unconfirmed"}[started], func(t *testing.T) {
			s := NewChangePasswordScreen(&ScreenContext{})
			s.changeCommand()
			s.HandleMsg(changePwDoneMsg{owner: s, attempt: s.attempt,
				result: passwordExecution{started: started, err: errors.New("command failed")}})
			view := s.View(75, 25)
			want, forbidden := "Password not changed", "Password change not confirmed"
			if started {
				want, forbidden = forbidden, want
			}
			if !strings.Contains(view, want) || strings.Contains(view, forbidden) || strings.Contains(view, "successfully") {
				t.Fatal("password failure misrepresented whether the credential may have changed")
			}
		})
	}
}

func TestPasswordCommandResultsBelongToOriginalAttempt(t *testing.T) {
	theme.Init(true)
	ctx := &ScreenContext{}
	s := NewChangePasswordScreen(ctx)
	oldCancel := s.closeCommand()
	if s.changeCommand() == nil || s.changeCommand() != nil {
		t.Fatal("missing or duplicate native command")
	}
	other := NewChangePasswordScreen(ctx)
	other.step, other.attempt = changePwStepWorking, s.attempt
	m := Model{nav: NewNavSidebar(), screenCtx: ctx, tabs: []openTab{
		{Kind: tabSSHChangePassword, Section: secSystem, Screen: s},
		{Kind: tabSSHChangePassword, Section: secSystem, Screen: other},
	}}
	m.nav.ActiveItem = secWallet
	stale := changePwDoneMsg{owner: s, attempt: s.attempt - 1, result: passwordExecution{started: true, changed: true}}
	m.Update(stale)
	if !sshAccessBusy(s) {
		t.Fatal("stale result completed password command")
	}
	done := changePwDoneMsg{owner: s, attempt: s.attempt, result: passwordExecution{started: true, changed: true, err: errors.New("terminal restore failed")}}
	other.HandleMsg(done)
	m.Update(done)
	if !s.result.changed || !sshAccessBusy(other) {
		t.Fatal("lost hidden result or changed wrong screen")
	}
	done.result = passwordExecution{started: true, err: errors.New("late failure")}
	m.Update(done)
	if !s.result.changed {
		t.Fatal("duplicate result replaced confirmed success")
	}
	updated, _ := m.Update(oldCancel())
	m = updated.(Model)
	if len(m.tabs) != 2 {
		t.Fatal("stale cancel removed newer attempt")
	}
	_, closeCmd := s.HandleKey("enter", tea.KeyPressMsg{})
	updated, _ = m.Update(closeCmd())
	m = updated.(Model)
	if len(m.tabs) != 1 || m.tabs[0].Screen != other {
		t.Fatal("closed the wrong password tab")
	}
	if view := s.View(75, 25); !strings.Contains(view, "Password changed successfully") || !strings.Contains(view, "terminal restore failed") {
		t.Fatal("terminal failure hid confirmed password change")
	}
}
