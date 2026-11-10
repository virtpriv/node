package tui

import (
	"errors"

	tea "charm.land/bubbletea/v2"
	"github.com/virtpriv/node/internal/api"
	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/logger"
)

// rebootAttempt is one Reboot job this TUI watches.
type rebootAttempt struct {
	updates <-chan app.JobUpdate
}

type rebootRequestMsg struct {
	owner   *SystemHomeScreen
	attempt *rebootAttempt
}

type rebootResultMsg struct {
	owner   *SystemHomeScreen
	attempt *rebootAttempt
	update  app.JobUpdate
}

func watchRebootCmd(owner *SystemHomeScreen, attempt *rebootAttempt) tea.Cmd {
	updates := attempt.updates
	return func() tea.Msg {
		u, ok := <-updates
		if !ok {
			return nil
		}
		return rebootResultMsg{owner: owner, attempt: attempt, update: u}
	}
}

func (m *Model) startReboot(msg rebootRequestMsg) tea.Cmd {
	if msg.owner == nil || m.sectionScreens[secSystem] != msg.owner ||
		msg.attempt == nil || msg.owner.rebootPending != msg.attempt || msg.attempt.updates != nil {
		return nil
	}
	m.invalidateHostStatus(errors.New("reboot request may have changed host state"))
	msg.attempt.updates = m.screenCtx.jobs().Start(api.KindReboot, nil)
	return watchRebootCmd(msg.owner, msg.attempt)
}

func (m *Model) completeReboot(msg rebootResultMsg) tea.Cmd {
	if msg.owner == nil || m.sectionScreens[secSystem] != msg.owner ||
		msg.attempt == nil || msg.owner.rebootPending != msg.attempt || msg.attempt.updates == nil {
		return nil
	}
	if !msg.update.Final {
		return watchRebootCmd(msg.owner, msg.attempt)
	}
	msg.owner.rebootPending = nil
	msg.owner.applyRebootResult(msg.update)
	if err := msg.update.Error(); err != nil {
		logger.TUI("Reboot: %v", err)
	}
	m.invalidateHostStatus(errors.New("reboot request may have changed host state"))
	return m.admitStatus()
}

// applyRebootResult words a finished Reboot job under UX-012's freeze.
func (s *SystemHomeScreen) applyRebootResult(u app.JobUpdate) {
	switch u.Outcome {
	case app.JobCompleted:
		s.rebootAccepted = true
		s.rebootResult = "Reboot requested. The SSH connection may close. Reconnect after the host restarts."
	case app.JobRestarted:
		s.rebootResult = "The node restarted."
	case app.JobNotStarted:
		s.rebootResult = "Reboot was not requested. " + jobReason(u)
	default:
		s.rebootResult = "Reboot outcome unconfirmed. The host may be restarting. " +
			"Ask your administrator to check host state before retrying."
	}
}
