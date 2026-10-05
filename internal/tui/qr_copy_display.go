package tui

import (
	"slices"

	tea "charm.land/bubbletea/v2"
	"github.com/virtpriv/node/internal/app"
)

// Requests capture the exact QR or Copy payload on the event loop. Commands never
// read mutable screens, and only the mounted owner can admit a display.
type qrCopyDisplayRequest struct {
	owner             Screen
	text, label       string
	copy              bool
	wallet            walletObservationScope
	pubkey            string
	uris              []string
	invoice           *invoiceAttempt
	pair              uint64
	admitted, running bool
}

type qrCopyDisplayMsg struct{ request *qrCopyDisplayRequest }
type qrCopyDisplayDoneMsg struct {
	request *qrCopyDisplayRequest
	err     error
}

type qrCopyDisplayState struct {
	request *qrCopyDisplayRequest
	failed  bool
}

func (s *qrCopyDisplayState) command(request *qrCopyDisplayRequest) tea.Cmd {
	if request.text == "" || (s.request != nil && s.request.running) {
		return nil
	}
	s.request = request
	return func() tea.Msg { return qrCopyDisplayMsg{request: request} }
}

func qrCopyDisplay(screen Screen) *qrCopyDisplayState {
	switch s := screen.(type) {
	case *NodeInfoScreen:
		return &s.display
	case *ReceiveScreen:
		return &s.display
	case *SyncthingPairScreen:
		return &s.display
	}
	return nil
}

func (r *qrCopyDisplayRequest) current() bool {
	if r == nil || r.owner == nil {
		return false
	}
	state := qrCopyDisplay(r.owner)
	if state == nil || state.request != r {
		return false
	}
	switch s := r.owner.(type) {
	case *NodeInfoScreen:
		status := s.ctx.Status
		if status == nil || !status.Node.Fresh() || r.wallet != s.ctx.walletObservationScope() || r.pubkey != status.Node.Value.Pubkey {
			return false
		}
		if r.copy {
			return slices.Equal(r.uris, status.Node.Value.URIs)
		}
		return slices.Contains(status.Node.Value.URIs, r.text)
	case *ReceiveScreen:
		// An unavailable lookup does not invalidate the issued invoice. Only its
		// owning lifecycle or an observed terminal outcome retires this display.
		return r.wallet == s.ctx.walletObservationScope() && s.step == recvStepWaiting &&
			r.invoice != nil && r.invoice == s.attempt && r.text == s.invoice.PaymentRequest()
	case *SyncthingPairScreen:
		return s.ctx.Cfg.SyncthingEnabled && s.step == syncPairStepPostPair &&
			s.result.Outcome == app.SyncthingComplete && r.pair == s.attempt && r.text == s.result.LocalID
	}
	return false
}

func (m Model) qrCopyDisplayCurrent(request *qrCopyDisplayRequest) bool {
	tabs := m.effectiveTabs()
	return request.current() && m.activeTab > 0 && m.activeTab < len(tabs) && tabs[m.activeTab].Screen == request.owner
}

func (m *Model) showQRCopyDisplay(request *qrCopyDisplayRequest) tea.Cmd {
	if !m.qrCopyDisplayCurrent(request) || request.admitted || m.copyTerminal != nil || m.subview != svNone {
		return nil
	}
	request.admitted = true
	qrCopyDisplay(request.owner).failed = false
	if request.copy {
		request.running = true
		m.copyTerminal = request
		return showCopyTextCmd(request.text, func(err error) tea.Msg {
			return qrCopyDisplayDoneMsg{request: request, err: err}
		})
	}
	m.qrCopyOverlay = request
	m.connectionDisplay = nil
	m.urlTarget, m.qrLabel = request.text, request.label
	m.subview = svQR
	return nil
}

func (m *Model) completeQRCopyDisplay(msg qrCopyDisplayDoneMsg) tea.Cmd {
	if msg.request == nil || m.copyTerminal != msg.request {
		return nil
	}
	m.copyTerminal = nil
	msg.request.running = false
	for _, tab := range m.tabs {
		if tab.Screen == msg.request.owner {
			if state := qrCopyDisplay(tab.Screen); state != nil && state.request == msg.request {
				state.failed = msg.err != nil
			}
			break
		}
	}
	// Copy shows an admitted snapshot. Observations resume after terminal return;
	// clearing the display does not change the source invoice or node information.
	return requestStatusCmd
}
