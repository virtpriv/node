package tui

import (
	"errors"

	tea "charm.land/bubbletea/v2"
	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/lndrpc"
)

type statusReader interface {
	Collect(config.AppConfig, bool, app.StatusLND) app.StatusSnapshot
	Close()
}

type statusScope struct {
	config           config.AppConfig
	walletExists     bool
	walletGeneration uint64
	client           *lndrpc.Client
}

type statusRequest struct {
	scope    statusScope
	revision uint64
}
type statusResultMsg struct {
	request  *statusRequest
	snapshot app.StatusSnapshot
}

func requestStatusCmd() tea.Msg { return refreshStatusMsg{} }

func (m Model) currentStatusScope() statusScope {
	return statusScope{config: *m.cfg,
		walletExists: m.state.WalletExists, walletGeneration: m.screenCtx.walletGeneration,
		client: m.lndClient}
}

// Every trigger reaches this gate on the event loop. Overlap requests one
// follow-up read, never a concurrent collector or a queue of redundant reads.
func (m *Model) admitStatus() tea.Cmd {
	current := m.currentStatusScope()
	if m.statusScope != current {
		m.screenCtx.Status = nil
	}
	if m.statusActive != nil {
		m.statusPending = true
		return nil
	}
	request := &statusRequest{scope: current, revision: m.statusRevision}
	m.statusActive = request
	collector := m.statusCollector
	return func() tea.Msg {
		var client app.StatusLND
		if request.scope.client != nil {
			client = request.scope.client
		}
		return statusResultMsg{request: request, snapshot: collector.Collect(
			request.scope.config, request.scope.walletExists, client)}
	}
}

func (m *Model) completeStatus(msg statusResultMsg) tea.Cmd {
	if msg.request == nil || msg.request != m.statusActive {
		return nil
	}
	m.statusActive = nil
	current := m.currentStatusScope()
	if msg.request.scope == current && msg.request.revision == m.statusRevision {
		if m.screenCtx.Status != nil && m.statusScope == current {
			msg.snapshot = msg.snapshot.Retain(*m.screenCtx.Status)
		}
		if !m.state.WalletKnown {
			msg.snapshot = msg.snapshot.WalletUnavailable(errors.New("wallet state unavailable"))
		}
		m.screenCtx.Status = &msg.snapshot
		m.statusScope = current
	} else {
		m.statusPending = true
	}
	if m.statusPending {
		m.statusPending = false
		return m.admitStatus()
	}
	return nil
}

func (m *Model) invalidateHostStatus(err error) {
	m.statusRevision++
	if status := m.screenCtx.Status; status != nil {
		// Preserve last-good values and wallet identity while new reads recover.
		for name, observation := range status.Services {
			observation.Err = err
			status.Services[name] = observation
		}
		status.Reboot.Err, status.Disk.Err, status.Memory.Err = err, err, err
		status.Bitcoin.Err = err
		*status = status.WalletUnavailable(err)
	}
}
