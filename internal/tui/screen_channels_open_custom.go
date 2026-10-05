package tui

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/theme"
)

// ── Custom peer step ───────────────────────────────────

func (s *ChannelOpenScreen) handleCustomPeerKey(
	keyStr string, msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
	switch s.customZone {
	case coCustomZonePubkey:
		return s.handleCustomPubkeyKey(keyStr, msg)
	case coCustomZoneHost:
		return s.handleCustomHostKey(keyStr, msg)
	case coCustomZoneButtons:
		return s.handleCustomButtonKey(keyStr)
	}
	return s, nil
}

func (s *ChannelOpenScreen) handleCustomPubkeyKey(
	keyStr string, msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit
	case "left":
		if s.pubkeyInput.Value() != "" {
			var cmd tea.Cmd
			s.pubkeyInput, cmd =
				s.pubkeyInput.Update(tea.Msg(msg))
			return s, cmd
		}
		return s, emitFocusSidebar
	case "right":
		if s.pubkeyInput.Value() != "" {
			var cmd tea.Cmd
			s.pubkeyInput, cmd =
				s.pubkeyInput.Update(tea.Msg(msg))
			return s, cmd
		}
		return s, nil
	case "up":
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
		return s, nil
	case "down":
		s.pubkeyInput.Blur()
		s.hostInput.Focus()
		s.customZone = coCustomZoneHost
		return s, nil
	case "tab":
		s.pubkeyInput.Blur()
		s.hostInput.Focus()
		s.customZone = coCustomZoneHost
		return s, nil
	case "shift+tab":
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
		return s, nil
	case "backspace":
		var cmd tea.Cmd
		s.pubkeyInput, cmd =
			s.pubkeyInput.Update(tea.Msg(msg))
		return s, cmd
	case "enter":
		s.pubkeyInput.Blur()
		s.hostInput.Focus()
		s.customZone = coCustomZoneHost
		return s, nil
	default:
		var cmd tea.Cmd
		s.pubkeyInput, cmd =
			s.pubkeyInput.Update(tea.Msg(msg))
		return s, cmd
	}
}

func (s *ChannelOpenScreen) handleCustomHostKey(
	keyStr string, msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit
	case "left":
		if s.hostInput.Value() != "" {
			var cmd tea.Cmd
			s.hostInput, cmd =
				s.hostInput.Update(tea.Msg(msg))
			return s, cmd
		}
		return s, emitFocusSidebar
	case "right":
		if s.hostInput.Value() != "" {
			var cmd tea.Cmd
			s.hostInput, cmd =
				s.hostInput.Update(tea.Msg(msg))
			return s, cmd
		}
		return s, nil
	case "up":
		s.hostInput.Blur()
		s.pubkeyInput.Focus()
		s.customZone = coCustomZonePubkey
		return s, nil
	case "down":
		s.hostInput.Blur()
		s.customZone = coCustomZoneButtons
		return s, nil
	case "tab":
		s.hostInput.Blur()
		s.customZone = coCustomZoneButtons
		return s, nil
	case "shift+tab":
		s.hostInput.Blur()
		s.pubkeyInput.Focus()
		s.customZone = coCustomZonePubkey
		return s, nil
	case "backspace":
		var cmd tea.Cmd
		s.hostInput, cmd =
			s.hostInput.Update(tea.Msg(msg))
		return s, cmd
	case "enter":
		s.hostInput.Blur()
		s.customZone = coCustomZoneButtons
		return s, nil
	default:
		var cmd tea.Cmd
		s.hostInput, cmd =
			s.hostInput.Update(tea.Msg(msg))
		return s, cmd
	}
}

func (s *ChannelOpenScreen) handleCustomButtonKey(
	keyStr string,
) (Screen, tea.Cmd) {
	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit
	case "left":
		if s.customBtnIdx > 0 {
			s.customBtnIdx--
		} else {
			return s, emitFocusSidebar
		}
		return s, nil
	case "right":
		if s.customBtnIdx < 1 {
			s.customBtnIdx++
		}
		return s, nil
	case "up":
		s.customZone = coCustomZoneHost
		s.hostInput.Focus()
		return s, nil
	case "tab":
		return s, nil
	case "shift+tab":
		s.customZone = coCustomZoneHost
		s.hostInput.Focus()
		return s, nil
	case "enter":
		switch s.customBtnIdx {
		case 0: // Go Back
			s.error = ""
			s.step = coStepInput
			return s, nil
		case 1: // Continue
			return s.submitCustomPeer()
		}
		return s, nil
	case "backspace":
		s.error = ""
		s.step = coStepInput
		return s, nil
	}
	return s, nil
}

// ── Custom peer form submission ────────────────────────

func (s *ChannelOpenScreen) submitCustomPeer() (
	Screen, tea.Cmd,
) {
	pubkey, host, err := app.ValidateChannelPeer(s.pubkeyInput.Value(), s.hostInput.Value())
	if err != nil {
		s.error = err.Error()
		return s, nil
	}
	s.customPubkey = pubkey
	s.customHost = host
	s.customAlias = pubkey[:16] + "..."
	s.error = ""
	// Return to input with custom peer confirmed
	s.peerIdx = len(s.peerList)
	s.peerConfirmed = true
	s.step = coStepInput
	s.focusZone = coZoneAmounts
	return s, nil
}

// ── Custom peer view ───────────────────────────────────

func (s *ChannelOpenScreen) viewCustomPeer(
	w, h int,
) string {
	p := newPane(w)
	p.title(theme.Header, "Custom Peer")

	isFocused := s.ctx.ContentFocused

	p.input("Node Pubkey:",
		s.pubkeyInput.View(),
		isFocused &&
			s.customZone == coCustomZonePubkey)
	p.blank()
	p.input("Host (host:port):",
		s.hostInput.View(),
		isFocused &&
			s.customZone == coCustomZoneHost)

	p.appendError(s.error)

	btnFocused := isFocused &&
		s.customZone == coCustomZoneButtons
	return p.renderWithBottomButtons(
		[]string{"Go Back", "Continue"},
		s.customBtnIdx, btnFocused, h)
}

// ── Custom peer helpbar ────────────────────────────────

func (s *ChannelOpenScreen) customPeerBindings() []key.Binding {
	switch s.customZone {
	case coCustomZonePubkey:
		binds := []key.Binding{
			kLeftRightCursor, kTabNext,
			kSidebar,
		}
		if s.ctx.HasTabs {
			binds = append(binds, kShiftTabBar)
		}
		binds = append(binds, kQuit)
		return binds
	case coCustomZoneHost:
		return []key.Binding{
			kLeftRightCursor, kTabButtons,
			bind("⇧tab", "pubkey", "shift+tab"),
			kSidebar, kQuit,
		}
	case coCustomZoneButtons:
		binds := buttonNav(s.customBtnIdx)
		binds = append(binds, kEnter,
			bind("⇧tab", "host", "shift+tab"),
			kBack, kQuit)
		return binds
	}
	return nil
}
