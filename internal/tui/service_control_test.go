package tui

import (
	"errors"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/virtpriv/node/internal/api"
	"github.com/virtpriv/node/internal/app"
)

func TestServiceActionIdentityAndCompletion(t *testing.T) {
	m, _ := statusModelFixture(t)
	fake := &fakeJobs{}
	m.screenCtx.Jobs = fake
	m.nav.ActiveItem = secSystem
	m.focusContent()
	s := NewSystemHomeScreen(m.screenCtx)
	m.sectionScreens[secSystem] = s
	press := func(code rune) tea.Cmd { return statusUpdate(&m, tea.KeyPressMsg{Code: code}) }
	press(tea.KeyDown)
	press('r')
	requestCmd := press('y')
	if requestCmd == nil {
		t.Fatal("confirmation did not construct request")
	}
	requestMsg := requestCmd()
	resultCmd := statusUpdate(&m, requestMsg)
	if fake.calls != 1 || fake.service(0) != (api.ServiceActionParams{Unit: "tor", Action: "restart"}) {
		t.Fatalf("wrong command: %v", fake.params)
	}
	if statusUpdate(&m, requestMsg) != nil || fake.calls != 1 {
		t.Fatal("replayed admission started twice")
	}
	// Assert content through the actual pane allocation. Live screenshots still
	// establish visual quality, and operator reports establish interaction timing.
	view := ansi.Strip(m.viewMain())
	if !slices.ContainsFunc(strings.Split(view, "\n"), func(line string) bool {
		return strings.Contains(line, " tor ") && strings.Contains(line, "restarting...")
	}) {
		t.Fatalf("pending action hidden: %s", view)
	}
	for _, binding := range s.HelpBindings() {
		for _, key := range binding.Keys() {
			if key == "r" || key == "s" || key == "a" {
				t.Fatal("busy help advertises blocked action")
			}
		}
	}
	press('r')
	if press('y') != nil || fake.calls != 1 {
		t.Fatal("accepted a second action while busy")
	}
	fake.results <- done(app.JobUnconfirmed, errors.New("injected read failure"))
	oldResult := resultCmd()
	statusUpdate(&m, oldResult)
	if s.svcPending != nil {
		t.Fatal("owner did not complete")
	}
	view = ansi.Strip(m.viewMain())
	if !strings.Contains(view, "tor restart outcome unconfirmed") || !strings.Contains(view, "injected read failure") {
		t.Fatalf("outcome lost: %s", view)
	}
	press('r')
	newRequest := press('y')()
	newResult := statusUpdate(&m, newRequest)
	statusUpdate(&m, oldResult)
	if s.svcPending == nil || fake.calls != 2 || fake.service(1).Unit != "tor" {
		t.Fatal("old completion cleared newer action")
	}
	fake.results <- fake.serviceDone()
	statusUpdate(&m, newResult())
	if s.svcPending != nil || !strings.Contains(s.svcResult, "tor restart completed") {
		t.Fatal("new action did not complete")
	}
}

func TestServiceMutationRejectsPredatingStatusAtAdmissionAndCompletion(t *testing.T) {
	m, reader := statusModelFixture(t)
	fake := &fakeJobs{}
	m.screenCtx.Jobs = fake
	m.nav.ActiveItem = secSystem
	m.focusContent()
	s := NewSystemHomeScreen(m.screenCtx)
	m.sectionScreens[secSystem] = s
	m.screenCtx.Status = &app.StatusSnapshot{Services: map[string]app.Observation[bool]{"tor": freshStatus(true)}}
	before, beforeDone := startStatusCommand(t, statusUpdate(&m, refreshStatusMsg{}), reader)
	statusUpdate(&m, tea.KeyPressMsg{Code: tea.KeyDown})
	statusUpdate(&m, tea.KeyPressMsg{Code: 's'})
	confirm := statusUpdate(&m, tea.KeyPressMsg{Code: 'y'})
	resultCmd := statusUpdate(&m, confirm())
	if m.screenCtx.Status.Services["tor"].Fresh() {
		t.Fatal("pre-action state remained fresh")
	}
	before.result <- app.StatusSnapshot{Services: map[string]app.Observation[bool]{"tor": freshStatus(true)}}
	during, duringDone := startStatusCommand(t, statusUpdate(&m, <-beforeDone), reader)
	if m.screenCtx.Status.Services["tor"].Fresh() {
		t.Fatal("pre-action read published")
	}
	fake.results <- fake.serviceDone()
	if statusUpdate(&m, resultCmd()) != nil {
		t.Fatal("completion started overlapping collector")
	}
	during.result <- app.StatusSnapshot{Services: map[string]app.Observation[bool]{"tor": freshStatus(true)}}
	after, afterDone := startStatusCommand(t, statusUpdate(&m, <-duringDone), reader)
	if m.screenCtx.Status.Services["tor"].Fresh() {
		t.Fatal("read predating completion published")
	}
	after.result <- app.StatusSnapshot{Services: map[string]app.Observation[bool]{"tor": freshStatus(false)}}
	if statusUpdate(&m, <-afterDone) != nil {
		t.Fatal("refresh did not coalesce")
	}
	if !m.screenCtx.Status.Services["tor"].Fresh() || m.screenCtx.Status.Services["tor"].Value {
		t.Fatal("post-action observation did not recover")
	}
	view := ansi.Strip(m.viewMain())
	found := false
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "● tor") {
			found = true
			if strings.Contains(line, "stale") || strings.Contains(line, "unavailable") {
				t.Fatalf("existing service row did not recover: %s", line)
			}
		}
	}
	if !found {
		t.Fatalf("recovered service row missing: %s", view)
	}
}
