package tui

import (
	"errors"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/virtualprivatenode/vpn/internal/app"
)

type rebootsStub struct {
	calls   int
	results chan app.RebootResult
}

func (s *rebootsStub) Request() <-chan app.RebootResult {
	s.calls++
	s.results = make(chan app.RebootResult, 1)
	return s.results
}
func (*rebootsStub) Close() {}

func TestRebootHiddenCompletionRetryAndDuplicateAdmission(t *testing.T) {
	m, _ := statusModelFixture(t)
	fake := &rebootsStub{}
	m.screenCtx.Reboots = fake
	m.nav.ActiveItem, m.nav.Cursor = secSystem, secSystem
	m.focusContent()
	s := NewSystemHomeScreen(m.screenCtx)
	m.sectionScreens[secSystem] = s
	m.screenCtx.Status = &app.StatusSnapshot{Reboot: freshStatus(true)}
	m.statusScope = m.currentStatusScope()
	s.btnIdx = slices.Index(s.buttonActions(), sysBtnReboot)
	m.sectionScreens[secAddons] = NewAddonsHomeScreen(m.screenCtx)
	press := func(code rune) tea.Cmd { return statusUpdate(&m, tea.KeyPressMsg{Code: code}) }
	press(tea.KeyEnter)
	confirm := press('y')
	if confirm == nil {
		t.Fatal("confirmation did not construct request")
	}
	// Admission must close before Bubble Tea executes the command.
	press(tea.KeyEnter)
	if press('y') != nil || fake.calls != 0 {
		t.Fatal("repeated confirmation queued another request before dispatch")
	}
	request := confirm()
	resultCmd := statusUpdate(&m, request)
	if resultCmd == nil || fake.calls != 1 {
		t.Fatal("confirmation did not start reboot")
	}
	if statusUpdate(&m, request) != nil || fake.calls != 1 {
		t.Fatal("replayed request started twice")
	}
	if !strings.Contains(ansi.Strip(m.viewMain()), "Requesting reboot...") {
		t.Fatalf("pending reboot hidden: %s", ansi.Strip(m.viewMain()))
	}
	for _, binding := range s.HelpBindings() {
		if slices.Contains(binding.Keys(), "enter") {
			t.Fatal("pending help advertises blocked action")
		}
	}
	press(tea.KeyEnter)
	if press('y') != nil || fake.calls != 1 {
		t.Fatal("duplicate reboot accepted while pending")
	}
	// Navigate through root dispatch while the same System screen stays mounted.
	statusUpdate(&m, press(tea.KeyBackspace)())
	press(tea.KeyUp)
	press(tea.KeyEnter)
	if m.nav.ActiveSection() == secSystem {
		t.Fatal("System did not hide")
	}
	fake.results <- app.RebootResult{Outcome: app.RebootUnconfirmed, Err: errors.New("injected lost reboot reply")}
	oldResult := resultCmd()
	statusUpdate(&m, oldResult)
	if s.rebootPending != nil {
		t.Fatal("hidden owner did not complete")
	}
	statusUpdate(&m, press(tea.KeyBackspace)())
	press(tea.KeyDown)
	press(tea.KeyEnter)
	if m.nav.ActiveSection() != secSystem {
		t.Fatal("did not return to System")
	}
	view := ansi.Strip(m.viewMain())
	if !strings.Contains(view, "Reboot outcome unconfirmed") || !strings.Contains(view, "administrator") {
		t.Fatalf("retained outcome missing: %s", view)
	}
	if strings.Contains(view, "Reboot completed") {
		t.Fatal("failure presented as success")
	}
	press(tea.KeyEnter)
	retry := press('y')
	if retry == nil {
		t.Fatal("explicit retry unavailable")
	}
	newResult := statusUpdate(&m, retry())
	statusUpdate(&m, oldResult)
	if s.rebootPending == nil || fake.calls != 2 {
		t.Fatal("old completion cleared new attempt")
	}
	fake.results <- app.RebootResult{Outcome: app.RebootAccepted}
	statusUpdate(&m, newResult())
	view = ansi.Strip(m.viewMain())
	if s.rebootPending != nil || !strings.Contains(view, "Reboot requested.") || strings.Contains(view, "unconfirmed") {
		t.Fatalf("new outcome not visible: %s", view)
	}
	press(tea.KeyEnter)
	if press('y') != nil || fake.calls != 2 {
		t.Fatal("accepted request was submitted again")
	}
	for _, binding := range s.HelpBindings() {
		if slices.Contains(binding.Keys(), "enter") {
			t.Fatal("accepted help advertises another reboot")
		}
	}
}

func TestRebootRejectsPredatingStatusAndRefreshesOpenView(t *testing.T) {
	m, reader := statusModelFixture(t)
	fake := &rebootsStub{}
	m.screenCtx.Reboots = fake
	m.nav.ActiveItem = secSystem
	m.focusContent()
	s := NewSystemHomeScreen(m.screenCtx)
	m.sectionScreens[secSystem] = s
	m.statusScope = m.currentStatusScope()
	m.screenCtx.Status = &app.StatusSnapshot{Reboot: freshStatus(true), Services: map[string]app.Observation[bool]{"tor": freshStatus(true)}}
	s.btnIdx = slices.Index(s.buttonActions(), sysBtnReboot)
	before, beforeDone := startStatusCommand(t, statusUpdate(&m, refreshStatusMsg{}), reader)
	statusUpdate(&m, tea.KeyPressMsg{Code: tea.KeyEnter})
	confirm := statusUpdate(&m, tea.KeyPressMsg{Code: 'y'})
	resultCmd := statusUpdate(&m, confirm())
	if m.screenCtx.Status.Reboot.Fresh() || m.screenCtx.Status.Services["tor"].Fresh() {
		t.Fatal("pre-request status remained fresh")
	}
	if tor := m.screenCtx.Status.Services["tor"]; !tor.Known() || !tor.Value {
		t.Fatal("invalidation discarded the last observed service state")
	}
	before.result <- app.StatusSnapshot{Reboot: freshStatus(true), Services: map[string]app.Observation[bool]{"tor": freshStatus(true)}}
	during, duringDone := startStatusCommand(t, statusUpdate(&m, <-beforeDone), reader)
	if m.screenCtx.Status.Reboot.Fresh() {
		t.Fatal("pre-request read published")
	}
	fake.results <- app.RebootResult{Outcome: app.RebootAccepted}
	if statusUpdate(&m, resultCmd()) != nil {
		t.Fatal("completion started overlapping status collector")
	}
	during.result <- app.StatusSnapshot{Reboot: freshStatus(true), Services: map[string]app.Observation[bool]{"tor": freshStatus(true)}}
	after, afterDone := startStatusCommand(t, statusUpdate(&m, <-duringDone), reader)
	if m.screenCtx.Status.Reboot.Fresh() {
		t.Fatal("read predating completion published")
	}
	after.result <- app.StatusSnapshot{Reboot: freshStatus(false), Services: map[string]app.Observation[bool]{"tor": freshStatus(false)}}
	if statusUpdate(&m, <-afterDone) != nil {
		t.Fatal("refresh did not coalesce")
	}
	if !m.screenCtx.Status.Reboot.Fresh() || m.screenCtx.Status.Reboot.Value ||
		!m.screenCtx.Status.Services["tor"].Fresh() || m.screenCtx.Status.Services["tor"].Value {
		t.Fatal("post-request observation did not recover")
	}
	view := ansi.Strip(m.viewMain())
	if strings.Contains(view, "Reboot required") || !strings.Contains(view, "Reboot requested.") {
		t.Fatalf("existing view did not refresh: %s", view)
	}
}

func TestRebootRejectsReplacedScreenRequestsAndResults(t *testing.T) {
	m, _ := statusModelFixture(t)
	fake := &rebootsStub{}
	m.screenCtx.Reboots = fake
	old := NewSystemHomeScreen(m.screenCtx)
	old.rebootPending = &rebootAttempt{}
	request := rebootRequestMsg{owner: old, attempt: old.rebootPending}
	replacement := NewSystemHomeScreen(m.screenCtx)
	m.sectionScreens[secSystem] = replacement
	if statusUpdate(&m, request) != nil || fake.calls != 0 {
		t.Fatal("unmounted owner started reboot")
	}
	m.sectionScreens[secSystem] = old
	resultCmd := statusUpdate(&m, request)
	if resultCmd == nil || fake.calls != 1 {
		t.Fatal("mounted owner did not start")
	}
	m.sectionScreens[secSystem] = replacement
	m.statusScope = m.currentStatusScope()
	m.screenCtx.Status = &app.StatusSnapshot{Reboot: freshStatus(false)}
	fake.results <- app.RebootResult{Outcome: app.RebootAccepted}
	if statusUpdate(&m, resultCmd()) != nil || !m.screenCtx.Status.Reboot.Fresh() {
		t.Fatal("retired owner result invalidated current status")
	}
}
