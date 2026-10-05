package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/servicecontrol"
)

type serviceControlStub struct {
	requests []servicecontrol.Request
	results  chan app.ServiceActionResult
}

func (s *serviceControlStub) Control(r servicecontrol.Request) <-chan app.ServiceActionResult {
	s.requests = append(s.requests, r)
	s.results = make(chan app.ServiceActionResult, 1)
	return s.results
}
func (*serviceControlStub) Close() {}

func TestServiceActionIdentityAndCompletionWhileHidden(t *testing.T) {
	m, _ := statusModelFixture(t)
	fake := &serviceControlStub{}
	m.screenCtx.ServiceControls = fake
	m.nav.ActiveItem = secSystem
	m.focusContent()
	s := NewSystemHomeScreen(m.screenCtx)
	m.sectionScreens[secSystem] = s
	press := func(code rune) tea.Cmd { return statusUpdate(&m, tea.KeyPressMsg{Code: code}) }
	press(tea.KeyDown)
	press(tea.KeyDown)
	press('r')
	requestCmd := press('y')
	if requestCmd == nil {
		t.Fatal("confirmation did not construct request")
	}
	requestMsg := requestCmd()
	resultCmd := statusUpdate(&m, requestMsg)
	if len(fake.requests) != 1 || fake.requests[0].Service() != "bitcoind" || fake.requests[0].Action() != "restart" {
		t.Fatalf("wrong command: %v", fake.requests)
	}
	if statusUpdate(&m, requestMsg) != nil || len(fake.requests) != 1 {
		t.Fatal("replayed admission started twice")
	}
	press(tea.KeyDown)
	// Assert content through the actual pane allocation. Live screenshots still
	// establish visual quality, and operator reports establish interaction timing.
	view := ansi.Strip(m.viewMain())
	found := false
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "restarting...") {
			found = true
			if !strings.Contains(line, "bitcoind") {
				t.Fatalf("pending moved with cursor: %s", line)
			}
		}
	}
	if !found {
		t.Fatal("pending action hidden")
	}
	for _, binding := range s.HelpBindings() {
		for _, key := range binding.Keys() {
			if key == "r" || key == "s" || key == "a" || key == "p" || key == "u" {
				t.Fatal("busy help advertises blocked action")
			}
		}
	}
	press('r')
	if press('y') != nil || len(fake.requests) != 1 {
		t.Fatal("accepted a second action while busy")
	}
	m.nav.ActiveItem = secOnChain
	fake.results <- app.ServiceActionResult{Outcome: app.ServiceActionUnconfirmed, Err: errors.New("injected read failure")}
	oldResult := resultCmd()
	statusUpdate(&m, oldResult)
	if s.svcPending != nil {
		t.Fatal("hidden owner did not complete")
	}
	m.nav.ActiveItem = secSystem
	view = ansi.Strip(m.viewMain())
	if !strings.Contains(view, "bitcoind restart outcome unconfirmed") || !strings.Contains(view, "injected read failure") {
		t.Fatalf("outcome lost: %s", view)
	}
	press('r')
	newRequest := press('y')()
	newResult := statusUpdate(&m, newRequest)
	statusUpdate(&m, oldResult)
	if s.svcPending == nil || len(fake.requests) != 2 || fake.requests[1].Service() != "lnd" {
		t.Fatal("old completion cleared newer action")
	}
	fake.results <- app.ServiceActionResult{Outcome: app.ServiceActionCompleted}
	statusUpdate(&m, newResult())
	if s.svcPending != nil || !strings.Contains(s.svcResult, "lnd restart completed") {
		t.Fatal("new action did not complete")
	}
}

func TestServiceMutationRejectsPredatingStatusAtAdmissionAndCompletion(t *testing.T) {
	m, reader := statusModelFixture(t)
	fake := &serviceControlStub{}
	m.screenCtx.ServiceControls = fake
	m.nav.ActiveItem = secSystem
	m.focusContent()
	s := NewSystemHomeScreen(m.screenCtx)
	m.sectionScreens[secSystem] = s
	m.statusScope = m.currentStatusScope()
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
	fake.results <- app.ServiceActionResult{Outcome: app.ServiceActionCompleted}
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
