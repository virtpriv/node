package tui

import (
	"errors"

	tea "charm.land/bubbletea/v2"
	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/logger"
)

type rebootAttempt struct {
	results <-chan app.RebootResult
}

type rebootRequestMsg struct {
	owner   *SystemHomeScreen
	attempt *rebootAttempt
}

type rebootResultMsg struct {
	owner   *SystemHomeScreen
	attempt *rebootAttempt
	result  app.RebootResult
}

func (m *Model) startReboot(msg rebootRequestMsg) tea.Cmd {
	if msg.owner == nil || m.sectionScreens[secSystem] != msg.owner ||
		msg.attempt == nil || msg.owner.rebootPending != msg.attempt || msg.attempt.results != nil {
		return nil
	}
	m.invalidateHostStatus(errors.New("reboot request may have changed host state"))
	results := m.screenCtx.reboots().Request()
	msg.attempt.results = results
	return func() tea.Msg {
		return rebootResultMsg{owner: msg.owner, attempt: msg.attempt, result: <-results}
	}
}

func (m *Model) completeReboot(msg rebootResultMsg) tea.Cmd {
	if msg.owner == nil || m.sectionScreens[secSystem] != msg.owner ||
		msg.attempt == nil || msg.owner.rebootPending != msg.attempt || msg.attempt.results == nil {
		return nil
	}
	msg.owner.rebootPending = nil
	switch msg.result.Outcome {
	case app.RebootAccepted:
		msg.owner.rebootAccepted = true
		msg.owner.rebootResult = "Reboot requested. The SSH connection may close. Reconnect after the host restarts."
	case app.RebootNotStarted:
		msg.owner.rebootResult = "Reboot was not requested. Reopen the TUI to try again."
	default:
		msg.owner.rebootResult = "Reboot outcome unconfirmed. The host may be restarting. " +
			"Ask your administrator to check host state before retrying."
	}
	if msg.result.Err != nil {
		logger.TUI("Reboot: %v", msg.result.Err)
	}
	m.invalidateHostStatus(errors.New("reboot request may have changed host state"))
	return m.admitStatus()
}
