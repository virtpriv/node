package tui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/logger"
)

type sshLoginVerifier interface {
	Verify() app.SSHVerification
	Close()
}

type refreshSSHVerificationMsg struct{}
type sshVerificationRequest struct{ owner *ScreenContext }
type sshVerificationResultMsg struct {
	request *sshVerificationRequest
	result  app.SSHVerification
}

func requestSSHVerificationCmd() tea.Msg { return refreshSSHVerificationMsg{} }

// Startup, resume and ticks share one event-loop admission point. Overlap
// requests a single follow-up instead of racing marker reads against clears.
func (m *Model) admitSSHVerification() tea.Cmd {
	if m.verificationActive != nil {
		m.verificationPending = true
		return nil
	}
	if m.screenCtx.SSHVerification == nil {
		m.screenCtx.SSHVerification = app.NewSSHLoginVerifier()
	}
	reader := m.screenCtx.SSHVerification
	request := &sshVerificationRequest{owner: m.screenCtx}
	m.verificationActive = request
	return func() tea.Msg {
		return sshVerificationResultMsg{request: request, result: reader.Verify()}
	}
}

func (m *Model) completeSSHVerification(msg sshVerificationResultMsg) tea.Cmd {
	if msg.request == nil || msg.request != m.verificationActive || msg.request.owner != m.screenCtx {
		return nil
	}
	m.verificationActive = nil
	m.state.KeyVerificationKnown = msg.result.Err == nil
	m.state.KeyVerificationAddress = ""
	if msg.result.Err != nil {
		logger.TUI("verify SSH login: %v", msg.result.Err)
	} else {
		m.state.KeyVerificationPending = msg.result.Pending
		m.state.KeyVerificationAddress = msg.result.Address
	}
	if m.verificationPending {
		m.verificationPending = false
		return m.admitSSHVerification()
	}
	return nil
}
