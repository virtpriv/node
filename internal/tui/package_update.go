package tui

import (
	"errors"

	tea "charm.land/bubbletea/v2"
	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/logger"
)

type packageAttempt struct {
	results <-chan app.PackageUpdateResult
}

type packageUpdateRequestMsg struct {
	owner   *SystemHomeScreen
	attempt *packageAttempt
}

type packageUpdateResultMsg struct {
	owner   *SystemHomeScreen
	attempt *packageAttempt
	result  app.PackageUpdateResult
}

func packageUpdateCmd(owner *SystemHomeScreen, attempt *packageAttempt) tea.Cmd {
	return func() tea.Msg { return packageUpdateRequestMsg{owner: owner, attempt: attempt} }
}

func (m *Model) startPackageUpdate(msg packageUpdateRequestMsg) tea.Cmd {
	if msg.owner == nil || m.sectionScreens[secSystem] != msg.owner ||
		msg.attempt == nil || msg.owner.pkgPending != msg.attempt || msg.attempt.results != nil {
		return nil
	}
	// Invalidate on the event loop before the helper can accept the update.
	m.invalidatePackageStatus()
	results := m.screenCtx.packageUpdates().Update()
	msg.attempt.results = results
	return func() tea.Msg {
		return packageUpdateResultMsg{owner: msg.owner, attempt: msg.attempt, result: <-results}
	}
}

func (m *Model) completePackageUpdate(msg packageUpdateResultMsg) tea.Cmd {
	if msg.owner == nil || m.sectionScreens[secSystem] != msg.owner ||
		msg.attempt == nil || msg.owner.pkgPending != msg.attempt || msg.attempt.results == nil {
		return nil
	}
	msg.owner.pkgPending = nil
	switch msg.result.Outcome {
	case app.PackageUpdateCompleted:
		msg.owner.pkgResult = "Package update completed."
	case app.PackageUpdateNotStarted:
		msg.owner.pkgResult = "Package update was not started. Reopen the TUI to try again."
	default:
		msg.owner.pkgResult = "Package update outcome unconfirmed. The host may still be working. " +
			"Ask your administrator to check package state and logs before retrying."
	}
	if msg.result.Err != nil {
		logger.TUI("Package update: %v", msg.result.Err)
	}
	m.invalidatePackageStatus()
	return m.admitStatus()
}

func (m *Model) invalidatePackageStatus() {
	m.invalidateHostStatus(errors.New("package update may have changed host state"))
}
