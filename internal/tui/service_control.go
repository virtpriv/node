package tui

import (
	"errors"

	tea "charm.land/bubbletea/v2"
	"github.com/virtpriv/node/internal/api"
	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/servicecontrol"
)

// serviceAttempt is one service job this TUI watches.
type serviceAttempt struct {
	request servicecontrol.Request
	updates <-chan app.JobUpdate
}

type serviceActionRequestMsg struct {
	owner   *SystemHomeScreen
	attempt *serviceAttempt
}

type serviceActionResultMsg struct {
	owner   *SystemHomeScreen
	attempt *serviceAttempt
	update  app.JobUpdate
}

func watchServiceCmd(owner *SystemHomeScreen, attempt *serviceAttempt) tea.Cmd {
	updates := attempt.updates
	return func() tea.Msg {
		u, ok := <-updates
		if !ok {
			return nil
		}
		return serviceActionResultMsg{owner: owner, attempt: attempt, update: u}
	}
}

func (m *Model) startServiceAction(msg serviceActionRequestMsg) tea.Cmd {
	if msg.owner == nil || m.sectionScreens[secSystem] != msg.owner ||
		msg.attempt == nil || msg.owner.svcPending != msg.attempt || msg.attempt.updates != nil {
		return nil
	}
	// Invalidate on the event loop before vpnd can accept the request.
	m.invalidateServiceStatus(msg.attempt.request.Service())
	msg.attempt.updates = m.screenCtx.jobs().Start(api.KindServiceAction, app.ServiceActionParams(msg.attempt.request))
	return watchServiceCmd(msg.owner, msg.attempt)
}

func (m *Model) completeServiceAction(msg serviceActionResultMsg) tea.Cmd {
	if msg.owner == nil || m.sectionScreens[secSystem] != msg.owner ||
		msg.attempt == nil || msg.owner.svcPending != msg.attempt || msg.attempt.updates == nil {
		return nil
	}
	if !msg.update.Final {
		return watchServiceCmd(msg.owner, msg.attempt)
	}
	msg.owner.svcPending = nil
	msg.owner.svcResult = serviceResultText(msg.attempt.request, app.VerifyServiceAction(msg.attempt.request, msg.update))
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

func serviceResultText(request servicecontrol.Request, u app.JobUpdate) string {
	label := request.Service() + " " + request.Action()
	switch u.Outcome {
	case app.JobCompleted:
		return label + " completed."
	case app.JobNotStarted:
		return withReason(label+" was not started", jobReason(u))
	}
	return withReason(label+" outcome unconfirmed", jobReason(u)) + " Check status and logs before retrying."
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
