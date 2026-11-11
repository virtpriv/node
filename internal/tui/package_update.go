package tui

import (
	"errors"

	tea "charm.land/bubbletea/v2"
	"github.com/virtpriv/node/internal/api"
	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/logger"
)

// packageAttempt is one Update Packages job this TUI watches, started here
// or found running at start.
type packageAttempt struct {
	updates <-chan app.JobUpdate
	// job is the latest look at the job, for its step.
	job api.Job
}

type packageUpdateRequestMsg struct {
	owner   *SystemHomeScreen
	attempt *packageAttempt
}

type packageUpdateResultMsg struct {
	owner   *SystemHomeScreen
	attempt *packageAttempt
	update  app.JobUpdate
}

func packageUpdateCmd(owner *SystemHomeScreen, attempt *packageAttempt) tea.Cmd {
	return func() tea.Msg { return packageUpdateRequestMsg{owner: owner, attempt: attempt} }
}

func watchPackageCmd(owner *SystemHomeScreen, attempt *packageAttempt) tea.Cmd {
	updates := attempt.updates
	return func() tea.Msg {
		u, ok := <-updates
		if !ok {
			return nil
		}
		return packageUpdateResultMsg{owner: owner, attempt: attempt, update: u}
	}
}

func (m *Model) startPackageUpdate(msg packageUpdateRequestMsg) tea.Cmd {
	if msg.owner == nil || m.sectionScreens[secSystem] != msg.owner ||
		msg.attempt == nil || msg.owner.pkgPending != msg.attempt || msg.attempt.updates != nil {
		return nil
	}
	// Invalidate on the event loop before vpnd can accept the update.
	m.invalidatePackageStatus()
	msg.attempt.updates = m.screenCtx.jobs().Start(api.KindPackageUpdate, nil)
	return watchPackageCmd(msg.owner, msg.attempt)
}

func (m *Model) completePackageUpdate(msg packageUpdateResultMsg) tea.Cmd {
	if msg.owner == nil || m.sectionScreens[secSystem] != msg.owner ||
		msg.attempt == nil || msg.owner.pkgPending != msg.attempt || msg.attempt.updates == nil {
		return nil
	}
	if !msg.update.Final {
		msg.attempt.job = msg.update.Job
		return watchPackageCmd(msg.owner, msg.attempt)
	}
	msg.owner.pkgPending = nil
	msg.owner.pkgResult = packageResultText(msg.update)
	if err := msg.update.Error(); err != nil {
		logger.TUI("Package update: %v", err)
	}
	m.invalidatePackageStatus()
	return m.admitStatus()
}

// packageResultText words a finished Update Packages job under UX-012's
// freeze: an unknown outcome stays unknown.
func packageResultText(u app.JobUpdate) string {
	switch u.Outcome {
	case app.JobCompleted:
		return "Package update completed."
	case app.JobNotStarted:
		return withReason("Package update was not started", jobReason(u))
	case app.JobUnconfirmed:
		if observationLost(u) {
			return "Package update outcome unconfirmed. The node may still be working. " +
				"Check the node before trying again."
		}
		return "Package update outcome unconfirmed. It ended without a result. " +
			"Check the node before trying again."
	}
	return withReason("Package update outcome unconfirmed", jobReason(u)) +
		" Check the node before trying again."
}

func (m *Model) invalidatePackageStatus() {
	m.invalidateHostStatus(errors.New("package update may have changed host state"))
}
