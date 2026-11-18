package tui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/virtpriv/node/internal/app"
)

type statusReader interface {
	Collect() app.StatusSnapshot
	Close()
}

type statusRequest struct {
	revision uint64
}
type statusResultMsg struct {
	request  *statusRequest
	snapshot app.StatusSnapshot
}

func requestStatusCmd() tea.Msg { return refreshStatusMsg{} }

// Every trigger reaches this gate on the event loop. Overlap requests one
// follow-up read, never a concurrent collector or a queue of redundant reads.
func (m *Model) admitStatus() tea.Cmd {
	if m.statusActive != nil {
		m.statusPending = true
		return nil
	}
	request := &statusRequest{revision: m.statusRevision}
	m.statusActive = request
	collector := m.statusCollector
	return func() tea.Msg {
		return statusResultMsg{request: request, snapshot: collector.Collect()}
	}
}

func (m *Model) completeStatus(msg statusResultMsg) tea.Cmd {
	if msg.request == nil || msg.request != m.statusActive {
		return nil
	}
	m.statusActive = nil
	if msg.request.revision == m.statusRevision {
		if m.screenCtx.Status != nil {
			msg.snapshot = msg.snapshot.Retain(*m.screenCtx.Status)
		}
		m.screenCtx.Status = &msg.snapshot
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
		// Keep the last good values while new reads recover.
		for name, observation := range status.Services {
			observation.Err = err
			status.Services[name] = observation
		}
		status.Reboot.Err, status.Disk.Err, status.Memory.Err = err, err, err
	}
}
