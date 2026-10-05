package tui

import (
	"net"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/theme"
)

// PairingScreen displays one owned observation of the REST endpoint and staged
// macaroon. Re-entry refreshes it; rendering performs no credential reads.
type PairingScreen struct {
	ctx        *ScreenContext
	btnIdx     int
	connection connectionInfoState
}

func NewPairingScreen(ctx *ScreenContext) *PairingScreen { return &PairingScreen{ctx: ctx} }

func (s *PairingScreen) Init() tea.Cmd {
	if s.connection.request != nil {
		return nil
	}
	return s.connection.refresh(s, s.ctx, app.LNDRESTConnection)
}

func (s *PairingScreen) HandleKey(keyStr string, msg tea.KeyPressMsg) (Screen, tea.Cmd) {
	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit
	case "left":
		if s.btnIdx > 0 {
			s.btnIdx--
			return s, nil
		}
		return s, emitFocusSidebar
	case "right":
		if s.btnIdx < len(s.buttons())-1 {
			s.btnIdx++
		}
	case "up", "shift+tab":
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
	case "backspace":
		return s, emitFocusParent
	case "enter":
		return s, s.enter()
	}
	return s, nil
}

func (s *PairingScreen) available() bool {
	return s.ctx.walletExists() && s.ctx.Status != nil && s.ctx.Status.Node.Fresh()
}

func (s *PairingScreen) buttons() []string {
	if !s.available() {
		return nil
	}
	if !s.connection.loaded {
		return []string{"Reading..."}
	}
	if !s.connection.current(s.ctx) {
		return []string{"Retry"}
	}
	info := s.connection.info
	var buttons []string
	if info.HasCredential() {
		if info.AddressErr == nil {
			buttons = append(buttons, "Show QR (Tor)")
		}
		if s.ctx.Cfg.P2PMode == "hybrid" && s.ctx.Status.PublicIP.Fresh() && s.ctx.Status.PublicIP.Value != "" {
			buttons = append(buttons, "Show QR (Clearnet)")
		}
		buttons = append(buttons, "Copyable Macaroon")
	}
	if s.connection.retry(s.ctx) {
		buttons = append(buttons, "Retry")
	}
	return buttons
}

func (s *PairingScreen) enter() tea.Cmd {
	buttons := s.buttons()
	if s.btnIdx < 0 || s.btnIdx >= len(buttons) {
		return nil
	}
	switch buttons[s.btnIdx] {
	case "Retry":
		s.btnIdx = 0
		return s.connection.refresh(s, s.ctx, app.LNDRESTConnection)
	case "Show QR (Tor)":
		return connectionActionCmd(s.connection.request, connectionTorQR, s.connection.info.Address)
	case "Show QR (Clearnet)":
		return connectionActionCmd(s.connection.request, connectionClearnetQR, s.ctx.Status.PublicIP.Value)
	case "Copyable Macaroon":
		return connectionActionCmd(s.connection.request, connectionMacaroon, "")
	}
	return nil
}

func (s *PairingScreen) HandleMsg(msg tea.Msg) (Screen, tea.Cmd) {
	if _, ok := msg.(tabActivatedMsg); ok {
		s.btnIdx = 0
		return s, s.connection.refresh(s, s.ctx, app.LNDRESTConnection)
	}
	return s, nil
}

func (s *PairingScreen) View(w, h int) string {
	if !s.ctx.walletKnown() && !s.ctx.walletDisplayAvailable() {
		return renderWalletStateUnavailable(w, h)
	}
	p := newPane(w)
	p.title(theme.Lightning, "⚡ Zeus - LND REST")
	switch {
	case !s.ctx.Cfg.HasLND() || !s.ctx.walletDisplayAvailable():
		p.dim("Create LND wallet first.")
	case !s.available():
		p.dim("Waiting for LND...")
	case !s.connection.loaded:
		p.dim("Reading connection information...")
	case !s.connection.current(s.ctx):
		p.warnWrap("Connection information changed. Retry to read it again.")
	default:
		info := s.connection.info
		if s.ctx.Cfg.P2PMode == "hybrid" {
			p.labelLine("Clearnet:")
			if s.ctx.Status.PublicIP.Fresh() && s.ctx.Status.PublicIP.Value != "" {
				p.monoWrap(net.JoinHostPort(s.ctx.Status.PublicIP.Value, "8080"))
			} else {
				p.dim("Server address unavailable.")
			}
			p.blank()
		}
		p.labelLine("Tor:")
		if info.AddressErr != nil {
			p.warnWrap("Tor address unavailable. Retry to read it again.")
		} else {
			p.monoWrap(info.Address)
			p.monoField("Port: ", "8080")
		}
		p.blank()
		if info.HasCredential() {
			mac := info.CredentialText()
			p.labelLine("Macaroon:")
			p.monoWrap(mac[:min(24, len(mac))] + "...")
		} else {
			p.warnWrap("Staged macaroon unavailable. Check the helper service, then retry.")
		}
		if s.connection.displayFailed {
			p.warnWrap("Macaroon display did not complete. Try Copyable Macaroon again.")
		}
	}
	return p.renderWithBottomButtons(s.buttons(), s.btnIdx, s.ctx.ContentFocused && s.connection.loaded, h)
}

func (s *PairingScreen) HelpBindings() []key.Binding {
	if !s.available() || !s.connection.loaded {
		return viewDetailBindings(s.ctx.HasTabs)
	}
	return tabButtonBindings(s.ctx.HasTabs)
}
