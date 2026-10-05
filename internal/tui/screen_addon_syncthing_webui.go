package tui

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/theme"
)

// SyncthingWebUIScreen observes the current onion and the installation's staged
// password. It does not recover passwords changed in the external Web UI.
type SyncthingWebUIScreen struct {
	ctx         *ScreenContext
	btnIdx      int
	showSecrets bool
	connection  connectionInfoState
}

func NewSyncthingWebUIScreen(ctx *ScreenContext) *SyncthingWebUIScreen {
	return &SyncthingWebUIScreen{ctx: ctx}
}

func (s *SyncthingWebUIScreen) Init() tea.Cmd {
	if s.connection.request != nil {
		return nil
	}
	return s.connection.refresh(s, s.ctx, app.SyncthingWebConnection)
}

func (s *SyncthingWebUIScreen) HandleKey(keyStr string, msg tea.KeyPressMsg) (Screen, tea.Cmd) {
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

func (s *SyncthingWebUIScreen) buttons() []string {
	if !s.ctx.Cfg.SyncthingEnabled {
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
	if info.AddressErr == nil {
		buttons = append(buttons, "Full URL")
	}
	if info.HasCredential() {
		label := "Show Password"
		if s.showSecrets {
			label = "Hide Password"
		}
		buttons = append(buttons, label)
	}
	if s.connection.retry(s.ctx) {
		buttons = append(buttons, "Retry")
	}
	return buttons
}

func (s *SyncthingWebUIScreen) enter() tea.Cmd {
	buttons := s.buttons()
	if s.btnIdx < 0 || s.btnIdx >= len(buttons) {
		return nil
	}
	switch buttons[s.btnIdx] {
	case "Retry":
		s.btnIdx = 0
		s.showSecrets = false
		return s.connection.refresh(s, s.ctx, app.SyncthingWebConnection)
	case "Full URL":
		return connectionActionCmd(s.connection.request, connectionWebURL, s.connection.info.Address)
	case "Show Password", "Hide Password":
		s.showSecrets = !s.showSecrets
	}
	return nil
}

func (s *SyncthingWebUIScreen) HandleMsg(msg tea.Msg) (Screen, tea.Cmd) {
	if _, ok := msg.(tabActivatedMsg); ok {
		s.btnIdx = 0
		s.showSecrets = false
		return s, s.connection.refresh(s, s.ctx, app.SyncthingWebConnection)
	}
	return s, nil
}

func (s *SyncthingWebUIScreen) View(w, h int) string {
	p := newPane(w)
	p.title(theme.Header, "↻ Syncthing Web UI")
	switch {
	case !s.ctx.Cfg.SyncthingEnabled:
		p.dim("Syncthing is not enabled.")
	case !s.connection.loaded:
		p.dim("Reading connection information...")
	case !s.connection.current(s.ctx):
		p.warnWrap("Connection information changed. Retry to read it again.")
	default:
		info := s.connection.info
		p.labelLine("URL:")
		if info.AddressErr != nil {
			p.warnWrap("Tor address unavailable. Retry to read it again.")
		} else {
			p.monoWrap(info.URL(info.Address))
		}
		p.blank()
		p.monoField("User: ", "admin")
		if info.HasCredential() {
			if s.showSecrets {
				p.monoField("Pass: ", info.CredentialText())
			} else {
				p.monoField("Pass: ", "••••••••")
			}
		} else {
			p.warnWrap("Staged Web UI password unavailable. Check the helper service, then retry.")
		}
	}
	return p.renderWithBottomButtons(s.buttons(), s.btnIdx, s.ctx.ContentFocused && s.connection.loaded, h)
}

func (s *SyncthingWebUIScreen) HelpBindings() []key.Binding {
	if !s.ctx.Cfg.SyncthingEnabled || !s.connection.loaded {
		return viewDetailBindings(s.ctx.HasTabs)
	}
	return tabButtonBindings(s.ctx.HasTabs)
}
