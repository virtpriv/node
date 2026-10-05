package tui

import (
	"errors"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/servicecontrol"
)

type serviceAttempt struct {
	request servicecontrol.Request
	results <-chan app.ServiceActionResult
}

type serviceActionRequestMsg struct {
	owner   *SystemHomeScreen
	attempt *serviceAttempt
}

type serviceActionResultMsg struct {
	owner   *SystemHomeScreen
	attempt *serviceAttempt
	result  app.ServiceActionResult
}

func (m *Model) startServiceAction(msg serviceActionRequestMsg) tea.Cmd {
	if msg.owner == nil || m.sectionScreens[secSystem] != msg.owner ||
		msg.attempt == nil || msg.owner.svcPending != msg.attempt || msg.attempt.results != nil {
		return nil
	}
	// Invalidate on the event loop before the application can send the request.
	m.invalidateServiceStatus(msg.attempt.request.Service())
	results := m.screenCtx.serviceControls().Control(msg.attempt.request)
	msg.attempt.results = results
	return func() tea.Msg {
		return serviceActionResultMsg{owner: msg.owner, attempt: msg.attempt, result: <-results}
	}
}

func (m *Model) completeServiceAction(msg serviceActionResultMsg) tea.Cmd {
	if msg.owner == nil || m.sectionScreens[secSystem] != msg.owner ||
		msg.attempt == nil || msg.owner.svcPending != msg.attempt || msg.attempt.results == nil {
		return nil
	}
	msg.owner.svcPending = nil
	msg.owner.svcResult = serviceResultText(msg.attempt.request, msg.result)
	m.invalidateServiceStatus(msg.attempt.request.Service())
	return m.admitStatus()
}

func (m *Model) invalidateServiceStatus(service string) {
	m.statusRevision++
	if status := m.screenCtx.Status; status != nil {
		err := errors.New("service state may have changed")
		observation := status.Services[service]
		observation.Err = err
		if status.Services != nil {
			status.Services[service] = observation
		}
		if service == "bitcoind" || service == "tor" {
			status.Bitcoin.Err = err
		}
		if service != "syncthing" {
			*status = status.WalletUnavailable(err)
		}
	}
}

func serviceResultText(request servicecontrol.Request, result app.ServiceActionResult) string {
	label := request.Service() + " " + request.Action()
	switch result.Outcome {
	case app.ServiceActionCompleted:
		return label + " completed."
	case app.ServiceActionNotStarted:
		return label + " was not started. " + serviceErrorText(result.Err)
	default:
		return label + " outcome unconfirmed. " + serviceErrorText(result.Err) +
			" Check status and logs before retrying; the host may still be working."
	}
}

func serviceErrorText(err error) string {
	if err == nil {
		return "No verified completion received."
	}
	// Keep helper errors on one printable line; wrapping belongs to the view.
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		if r == '—' {
			return ':'
		}
		return r
	}, err.Error())
}

func servicePendingText(request servicecontrol.Request) string {
	switch request.Action() {
	case "start":
		return "starting..."
	case "stop":
		return "stopping..."
	default:
		return "restarting..."
	}
}
