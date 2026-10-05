package tui

import (
	"errors"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/virtpriv/node/internal/app"
)

type packageUpdatesStub struct {
	calls   int
	results chan app.PackageUpdateResult
}

func (s *packageUpdatesStub) Update() <-chan app.PackageUpdateResult {
	s.calls++
	s.results = make(chan app.PackageUpdateResult, 1)
	return s.results
}
func (*packageUpdatesStub) Close() {}

func TestPackageUpdateHiddenCompletionAndRetryIdentity(t *testing.T) {
	m, _ := statusModelFixture(t)
	fake := &packageUpdatesStub{}
	m.screenCtx.PackageUpdates = fake
	m.nav.ActiveItem, m.nav.Cursor = secSystem, secSystem
	m.focusContent()
	s := NewSystemHomeScreen(m.screenCtx)
	m.sectionScreens[secSystem] = s
	m.sectionScreens[secAddons] = NewAddonsHomeScreen(m.screenCtx)
	press := func(code rune) tea.Cmd { return statusUpdate(&m, tea.KeyPressMsg{Code: code}) }
	press(tea.KeyEnter)
	confirm := press('y')
	if confirm == nil {
		t.Fatal("confirmation did not construct request")
	}
	request := confirm()
	resultCmd := statusUpdate(&m, request)
	if resultCmd == nil || fake.calls != 1 {
		t.Fatal("confirmation did not start update")
	}
	if statusUpdate(&m, request) != nil || fake.calls != 1 {
		t.Fatal("replayed request started twice")
	}
	if !strings.Contains(ansi.Strip(m.viewMain()), "Updating...") {
		t.Fatal("pending update hidden")
	}
	for _, binding := range s.HelpBindings() {
		if slices.Contains(binding.Keys(), "enter") {
			t.Fatal("pending help advertises blocked action")
		}
	}
	press(tea.KeyEnter)
	if press('y') != nil || fake.calls != 1 {
		t.Fatal("duplicate update accepted while pending")
	}
	// Navigate through root dispatch while the same System screen stays mounted.
	statusUpdate(&m, press(tea.KeyLeft)())
	press(tea.KeyUp)
	press(tea.KeyEnter)
	if m.nav.ActiveSection() == secSystem {
		t.Fatal("System did not hide")
	}
	fake.results <- app.PackageUpdateResult{Outcome: app.PackageUpdateUnconfirmed, Err: errors.New("injected dpkg failure")}
	oldResult := resultCmd()
	statusUpdate(&m, oldResult)
	if s.pkgPending != nil {
		t.Fatal("hidden owner did not complete")
	}
	statusUpdate(&m, press(tea.KeyLeft)())
	press(tea.KeyDown)
	press(tea.KeyEnter)
	if m.nav.ActiveSection() != secSystem {
		t.Fatal("did not return to System")
	}
	view := ansi.Strip(m.viewMain())
	if !strings.Contains(view, "Package update outcome unconfirmed") || !strings.Contains(view, "administrator") {
		t.Fatalf("retained outcome missing: %s", view)
	}
	if strings.Contains(view, "Package update completed") {
		t.Fatal("failure presented as success")
	}
	press(tea.KeyEnter)
	retry := press('y')
	if retry == nil {
		t.Fatal("explicit retry unavailable")
	}
	newResult := statusUpdate(&m, retry())
	statusUpdate(&m, oldResult)
	if s.pkgPending == nil || fake.calls != 2 {
		t.Fatal("old completion cleared new attempt")
	}
	fake.results <- app.PackageUpdateResult{Outcome: app.PackageUpdateCompleted}
	statusUpdate(&m, newResult())
	view = ansi.Strip(m.viewMain())
	if s.pkgPending != nil || !strings.Contains(view, "Package update completed.") || strings.Contains(view, "unconfirmed") {
		t.Fatalf("new outcome not visible: %s", view)
	}
}

func TestPackageUpdateRejectsPredatingStatusAndRefreshesOpenView(t *testing.T) {
	m, reader := statusModelFixture(t)
	fake := &packageUpdatesStub{}
	m.screenCtx.PackageUpdates = fake
	m.nav.ActiveItem = secSystem
	m.focusContent()
	s := NewSystemHomeScreen(m.screenCtx)
	m.sectionScreens[secSystem] = s
	m.statusScope = m.currentStatusScope()
	m.screenCtx.Status = &app.StatusSnapshot{Reboot: freshStatus(false), Services: map[string]app.Observation[bool]{"tor": freshStatus(true)}}
	before, beforeDone := startStatusCommand(t, statusUpdate(&m, refreshStatusMsg{}), reader)
	statusUpdate(&m, tea.KeyPressMsg{Code: tea.KeyEnter})
	confirm := statusUpdate(&m, tea.KeyPressMsg{Code: 'y'})
	resultCmd := statusUpdate(&m, confirm())
	if m.screenCtx.Status.Reboot.Fresh() || m.screenCtx.Status.Services["tor"].Fresh() {
		t.Fatal("pre-update status remained fresh")
	}
	if tor := m.screenCtx.Status.Services["tor"]; !tor.Known() || !tor.Value {
		t.Fatal("invalidation discarded the last observed service state")
	}
	before.result <- app.StatusSnapshot{Reboot: freshStatus(false), Services: map[string]app.Observation[bool]{"tor": freshStatus(true)}}
	during, duringDone := startStatusCommand(t, statusUpdate(&m, <-beforeDone), reader)
	if m.screenCtx.Status.Reboot.Fresh() {
		t.Fatal("pre-update read published")
	}
	fake.results <- app.PackageUpdateResult{Outcome: app.PackageUpdateCompleted}
	if statusUpdate(&m, resultCmd()) != nil {
		t.Fatal("completion started overlapping status collector")
	}
	during.result <- app.StatusSnapshot{Reboot: freshStatus(false), Services: map[string]app.Observation[bool]{"tor": freshStatus(true)}}
	after, afterDone := startStatusCommand(t, statusUpdate(&m, <-duringDone), reader)
	if m.screenCtx.Status.Reboot.Fresh() {
		t.Fatal("read predating completion published")
	}
	after.result <- app.StatusSnapshot{Reboot: freshStatus(true), Services: map[string]app.Observation[bool]{"tor": freshStatus(false)}}
	if statusUpdate(&m, <-afterDone) != nil {
		t.Fatal("refresh did not coalesce")
	}
	if !m.screenCtx.Status.Reboot.Fresh() || !m.screenCtx.Status.Reboot.Value ||
		!m.screenCtx.Status.Services["tor"].Fresh() || m.screenCtx.Status.Services["tor"].Value {
		t.Fatal("post-update observation did not recover")
	}
	view := ansi.Strip(m.viewMain())
	if !strings.Contains(view, "Reboot required") || !strings.Contains(view, "Package update completed.") {
		t.Fatalf("existing view did not refresh: %s", view)
	}
}
