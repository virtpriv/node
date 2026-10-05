package tui

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/theme"
)

// OCReceiveScreen retains its last address until an explicit request succeeds.
// Closing the tab discards observation, not addresses already derived by LND.
type OCReceiveScreen struct {
	ctx        *ScreenContext
	addresses  app.OnChainReceiveClient
	attempt    uint64
	requesting bool
	address    string
	errMsg     string
	btnIdx     int // 0=Show QR, 1=New Address or Retry; only Retry without an address
}

func NewOCReceiveScreen(
	ctx *ScreenContext,
) *OCReceiveScreen {
	return &OCReceiveScreen{
		ctx: ctx,
	}
}

// ── Screen interface ────────────────────────────────────

func (s *OCReceiveScreen) Init() tea.Cmd {
	if s.attempt != 0 {
		return nil
	}
	return s.requestAddress()
}

func (s *OCReceiveScreen) requestAddress() tea.Cmd {
	if s.requesting || !s.ctx.walletExists() {
		return nil
	}
	s.attempt++
	s.requesting = true
	s.errMsg = ""
	return getNewAddressCmd(s)
}

func (s *OCReceiveScreen) HandleKey(
	keyStr string, msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
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
		if !s.requesting && s.address != "" && s.btnIdx < 1 {
			s.btnIdx++
		}
		return s, nil
	case "up", "shift+tab":
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
		return s, nil
	case "down", "tab":
		return s, nil
	case "backspace":
		return s, emitFocusParent
	case "enter":
		if s.requesting {
			return s, nil
		}
		if s.address == "" {
			return s, s.requestAddress()
		}
		switch s.btnIdx {
		case 0: // Show QR
			attempt := s.attempt
			return s, func() tea.Msg {
				return receiveAddressQRMsg{owner: s, attempt: attempt}
			}
		case 1: // New Address
			return s, s.requestAddress()
		}
		return s, nil
	}
	return s, nil
}

func (s *OCReceiveScreen) HandleMsg(
	msg tea.Msg,
) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case newAddressMsg:
		if msg.owner != s || msg.attempt != s.attempt || !s.requesting {
			return s, nil
		}
		s.requesting = false
		if msg.err != nil {
			s.errMsg = msg.err.Error()
			return s, nil
		}
		s.address = msg.address.Text()
		s.btnIdx = 0
	}
	return s, nil
}

func (s *OCReceiveScreen) View(
	w, h int,
) string {
	p := newPane(w)
	p.title(theme.Header, "⛓ Receive On-Chain")
	if s.address != "" {
		p.labelLine("Address:")
		p.monoWrap(s.address)
		p.blank()
		p.dim("Send Bitcoin to this address.")
		p.dim("Earlier addresses remain valid.")
	}

	if s.requesting {
		p.blank()
		p.dim("Generating address...")
		return p.renderWithBottomButtons([]string{"Generating..."}, 0, false, h)
	}

	buttons := []string{"Show QR", "New Address"}
	if s.errMsg != "" {
		p.blank()
		p.appendError(s.errMsg)
		p.dim("Retry requests another address.")
		buttons[1] = "Retry"
	}
	if s.address == "" {
		buttons = []string{"New Address"}
		if s.errMsg != "" {
			buttons[0] = "Retry"
		}
	}

	return p.renderWithBottomButtons(
		buttons, s.btnIdx, s.ctx.ContentFocused, h, s.ctx.disabledWalletButtons(len(buttons)-1)...)
}

func (s *OCReceiveScreen) HelpBindings() []key.Binding {
	if !s.requesting {
		return tabButtonBindings(s.ctx.HasTabs)
	}
	binds := []key.Binding{kSidebar}
	if s.ctx.HasTabs {
		binds = append(binds, kShiftTabBar)
	}
	binds = append(binds, kQuit)
	return binds
}
