package tui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/virtpriv/node/internal/app"
)

type connectionInfoReader interface {
	Read(app.ConnectionTarget) app.ConnectionInfo
	Close()
}

func (c *ScreenContext) connectionInfo() connectionInfoReader {
	if c.ConnectionInfo == nil {
		c.ConnectionInfo = app.NewConnectionInfoReader()
	}
	return c.ConnectionInfo
}

type connectionInfoScope struct {
	wallet  walletObservationScope
	mode    string
	enabled bool
}

func (c *ScreenContext) connectionScope(target app.ConnectionTarget) connectionInfoScope {
	if target == app.SyncthingWebConnection {
		return connectionInfoScope{enabled: c.Cfg.SyncthingEnabled}
	}
	return connectionInfoScope{wallet: c.walletObservationScope(), mode: c.Cfg.P2PMode}
}

type connectionInfoRequest struct {
	owner  Screen
	target app.ConnectionTarget
	scope  connectionInfoScope
}

type connectionInfoResultMsg struct {
	request *connectionInfoRequest
	info    app.ConnectionInfo
}

// These two displays refresh at entry/reactivation. One pending follow-up
// replaces overlapping triggers; a retired screen cannot publish to its successor.
type connectionInfoState struct {
	request       *connectionInfoRequest
	info          app.ConnectionInfo
	loaded        bool
	again         bool
	displayFailed bool
}

func (s *connectionInfoState) refresh(owner Screen, ctx *ScreenContext, target app.ConnectionTarget) tea.Cmd {
	if s.request != nil && !s.loaded {
		s.again = true
		return nil
	}
	s.info = app.ConnectionInfo{}
	s.loaded = false
	s.displayFailed = false
	request := &connectionInfoRequest{owner: owner, target: target, scope: ctx.connectionScope(target)}
	s.request = request
	reader := ctx.connectionInfo()
	return func() tea.Msg { return connectionInfoResultMsg{request: request, info: reader.Read(target)} }
}

func (s *connectionInfoState) current(ctx *ScreenContext) bool {
	return s.request != nil && s.loaded && s.request.scope == ctx.connectionScope(s.request.target)
}

func (s *connectionInfoState) retry(ctx *ScreenContext) bool {
	return s.request != nil && s.loaded && (!s.current(ctx) || s.info.AddressErr != nil || s.info.CredentialErr != nil)
}

func connectionState(screen Screen) *connectionInfoState {
	switch s := screen.(type) {
	case *PairingScreen:
		return &s.connection
	case *SyncthingWebUIScreen:
		return &s.connection
	}
	return nil
}

func (m *Model) completeConnectionInfo(msg connectionInfoResultMsg) tea.Cmd {
	if msg.request == nil {
		return nil
	}
	for _, tab := range m.tabs {
		if tab.Screen != msg.request.owner {
			continue
		}
		state := connectionState(tab.Screen)
		if state == nil || state.request != msg.request || state.loaded {
			return nil
		}
		state.loaded = true
		if state.again || msg.request.scope != m.screenCtx.connectionScope(msg.request.target) {
			state.again = false
			return state.refresh(tab.Screen, m.screenCtx, msg.request.target)
		}
		state.info = msg.info
		return nil
	}
	return nil
}

type connectionAction int

const (
	connectionTorQR connectionAction = iota
	connectionClearnetQR
	connectionMacaroon
	connectionWebURL
)

type connectionActionMsg struct {
	request *connectionInfoRequest
	action  connectionAction
	host    string
}

type connectionDisplayDoneMsg struct {
	request *connectionInfoRequest
	err     error
}

func connectionActionCmd(request *connectionInfoRequest, action connectionAction, host string) tea.Cmd {
	return func() tea.Msg { return connectionActionMsg{request: request, action: action, host: host} }
}

// Display admission and open overlays use the same owner, scope and endpoint
// checks. An accepted terminal handoff finishes independently.
func (m Model) connectionActionCurrent(msg connectionActionMsg) bool {
	tabs := m.effectiveTabs()
	if msg.request == nil || m.activeTab <= 0 || m.activeTab >= len(tabs) || tabs[m.activeTab].Screen != msg.request.owner {
		return false
	}
	state := connectionState(msg.request.owner)
	if state == nil || state.request != msg.request || !state.current(m.screenCtx) {
		return false
	}
	info := state.info
	switch msg.action {
	case connectionTorQR, connectionClearnetQR, connectionMacaroon:
		owner, ok := msg.request.owner.(*PairingScreen)
		if !ok || msg.request.target != app.LNDRESTConnection || !owner.available() || !info.HasCredential() {
			return false
		}
		switch msg.action {
		case connectionTorQR:
			return info.AddressErr == nil && msg.host != "" && msg.host == info.Address
		case connectionClearnetQR:
			status := m.screenCtx.Status
			return m.cfg.P2PMode == "hybrid" && status.PublicIP.Fresh() && msg.host != "" && msg.host == status.PublicIP.Value
		default:
			return true
		}
	case connectionWebURL:
		return msg.request.target == app.SyncthingWebConnection && m.cfg.SyncthingEnabled && info.AddressErr == nil && msg.host != "" && msg.host == info.Address
	}
	return false
}

func (m *Model) showConnectionInfo(msg connectionActionMsg) tea.Cmd {
	if !m.connectionActionCurrent(msg) {
		return nil
	}
	state := connectionState(msg.request.owner)
	if msg.action == connectionMacaroon {
		state.displayFailed = false
		return showCopyTextCmd(state.info.CredentialText(), func(err error) tea.Msg {
			return connectionDisplayDoneMsg{request: msg.request, err: err}
		})
	}
	m.qrCopyOverlay = nil
	m.connectionDisplay = &msg
	m.urlTarget = state.info.URL(msg.host)
	if msg.action == connectionWebURL {
		m.subview = svFullURL
	} else {
		m.qrLabel = "LND Connect - Tor"
		if msg.action == connectionClearnetQR {
			m.qrLabel = "LND Connect - Clearnet"
		}
		m.subview = svQR
	}
	return nil
}

func (m Model) connectionDisplayUnavailable() bool {
	return m.connectionDisplay != nil && !m.connectionActionCurrent(*m.connectionDisplay)
}
