package tui

import (
	"context"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/p2p"
	"github.com/virtpriv/node/internal/theme"
)

// P2PUpgradeScreen observes an address, collects explicit consent, then owns
// the progress display. The submitted request retains the reviewed address.
type p2pStep int

const (
	p2pLoading p2pStep = iota
	p2pNoIP
	p2pConfirm
	p2pConfirm2
	p2pProgress
)

const (
	p2pZoneInput   = 0
	p2pZoneButtons = 1
)

type P2PUpgradeScreen struct {
	ctx  *ScreenContext
	step p2pStep

	// Confirm step
	input     textinput.Model
	focusZone int
	btnIdx    int // 0=Cancel, 1=Proceed
	error     string

	// Confirm2 step (final warning)
	confirm2Idx int // 0=Go Back, 1=Confirm

	// Embedded progress screen.
	progress *InstallProgressScreen

	request     p2p.UpgradeRequest
	addressRead *p2pAddressRead
}

func NewP2PUpgradeScreen(
	ctx *ScreenContext,
) *P2PUpgradeScreen {
	return &P2PUpgradeScreen{
		ctx: ctx, step: p2pLoading, btnIdx: 1, input: newP2PConfirmInput(),
	}
}

func newP2PConfirmInput() textinput.Model {
	ti := textinput.New()
	ti.Placeholder = "PUBLISH MY IP"
	ti.CharLimit = 13
	ti.SetWidth(20)
	ti.Validate = validatePrintableASCII
	ti.Prompt = "  "
	applyInputStyles(&ti)
	ti.Focus()
	return ti
}

// ── Screen interface ────────────────────────────────────

func (s *P2PUpgradeScreen) Init() tea.Cmd {
	if s.step != p2pLoading {
		return nil
	}
	return s.readAddress()
}

func (s *P2PUpgradeScreen) HandleKey(
	keyStr string, msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
	// Observation states cannot submit a mutation.
	if s.step == p2pNoIP || s.step == p2pLoading {
		switch keyStr {
		case "ctrl+c":
			return s, tea.Quit
		case "enter":
			if s.step == p2pNoIP {
				return s, s.readAddress()
			}
			return s, nil
		case "left":
			return s, emitFocusSidebar
		case "up", "shift+tab":
			if s.ctx.HasTabs {
				return s, emitFocusTabBar
			}
		case "backspace":
			return s, emitFocusParent
		}
		return s, nil
	}

	// Progress state
	if s.step == p2pProgress && s.progress != nil {
		if !s.progress.done {
			// Block all keys during active install
			return s, nil
		}
		switch keyStr {
		case "left":
			return s, emitFocusSidebar
		case "up":
			if s.ctx.HasTabs {
				return s, emitFocusTabBar
			}
		}
		newScreen, cmd := s.progress.HandleKey(
			keyStr, msg)
		s.progress =
			newScreen.(*InstallProgressScreen)
		return s, cmd
	}

	// Confirm2 state: final warning with Go Back / Confirm
	if s.step == p2pConfirm2 {
		switch keyStr {
		case "ctrl+c":
			return s, tea.Quit
		case "left":
			if s.confirm2Idx > 0 {
				s.confirm2Idx--
				return s, nil
			}
			return s, emitFocusSidebar
		case "right":
			if s.confirm2Idx < 1 {
				s.confirm2Idx++
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
			s.step = p2pConfirm
			s.focusZone = p2pZoneButtons
			s.input.Blur()
			return s, nil
		case "enter":
			if s.confirm2Idx == 0 {
				s.step = p2pConfirm
				s.focusZone = p2pZoneButtons
				s.input.Blur()
				return s, nil
			}
			return s.startInstall()
		}
		return s, nil
	}

	// Confirm state
	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit
	case "backspace":
		if s.focusZone == p2pZoneInput {
			var cmd tea.Cmd
			s.input, cmd = s.input.Update(msg)
			return s, cmd
		}
		return s, emitFocusParent

	case "left":
		if s.focusZone == p2pZoneButtons {
			if s.btnIdx > 0 {
				s.btnIdx--
				return s, nil
			}
			return s, emitFocusSidebar
		}
		var cmd tea.Cmd
		s.input, cmd = s.input.Update(msg)
		return s, cmd

	case "right":
		if s.focusZone == p2pZoneButtons {
			if s.btnIdx < 1 {
				s.btnIdx++
			}
			return s, nil
		}
		var cmd tea.Cmd
		s.input, cmd = s.input.Update(msg)
		return s, cmd

	case "up":
		if s.focusZone == p2pZoneButtons {
			s.focusZone = p2pZoneInput
			s.input.Focus()
			return s, nil
		}
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
		return s, nil

	case "down", "tab":
		if s.focusZone == p2pZoneInput {
			s.focusZone = p2pZoneButtons
			s.input.Blur()
			return s, nil
		}
		return s, nil

	case "shift+tab":
		if s.focusZone == p2pZoneButtons {
			s.focusZone = p2pZoneInput
			s.input.Focus()
			return s, nil
		}
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
		return s, nil

	case "enter":
		if s.focusZone == p2pZoneInput {
			// Enter on text field → move to buttons
			s.focusZone = p2pZoneButtons
			s.input.Blur()
			return s, nil
		}
		// Button zone
		if s.btnIdx == 0 {
			return s, emitCloseTab
		}
		// Proceed only if input matches exactly.
		if s.input.Value() != "PUBLISH MY IP" {
			s.error = "Type PUBLISH MY IP to confirm"
			return s, nil
		}
		s.error = ""
		s.step = p2pConfirm2
		s.confirm2Idx = 0
		return s, nil

	default:
		if s.focusZone == p2pZoneInput {
			s.error = ""
			var cmd tea.Cmd
			s.input, cmd = s.input.Update(msg)
			return s, cmd
		}
	}
	return s, nil
}

func (s *P2PUpgradeScreen) startInstall() (
	Screen, tea.Cmd,
) {
	var client app.P2PConnection
	if s.ctx.LndClient != nil {
		client = s.ctx.LndClient
	}
	if s.step != p2pConfirm2 || s.request.Address() == "" {
		return s, nil
	}
	operation := s.ctx.HelperWorkflows.UpgradeP2P(s.request, client)

	s.progress = NewInstallProgressScreen(
		s.ctx, operation,
		s.onInstallDone, s.onInstallFail)
	s.step = p2pProgress
	return s, s.progress.Init()
}

func (s *P2PUpgradeScreen) onInstallDone() tea.Cmd { return emitRefreshStatus }

func (s *P2PUpgradeScreen) onInstallFail() tea.Cmd {
	return func() tea.Msg {
		return refreshStatusMsg{}
	}
}

func (s *P2PUpgradeScreen) HandleMsg(
	msg tea.Msg,
) (Screen, tea.Cmd) {
	if s.step == p2pProgress && s.progress != nil {
		newScreen, cmd := s.progress.HandleMsg(msg)
		s.progress =
			newScreen.(*InstallProgressScreen)
		return s, cmd
	}
	switch msg := msg.(type) {
	case p2pAddressResultMsg:
		if msg.read == nil || msg.read != s.addressRead || s.step != p2pLoading {
			return s, nil
		}
		s.cancelAddressRead()
		if msg.err != nil || msg.request.Address() == "" {
			s.step = p2pNoIP
			return s, nil
		}
		s.request = msg.request
		s.step = p2pConfirm
		return s, s.input.Focus()
	case tea.PasteMsg:
		if s.step == p2pConfirm &&
			s.focusZone == p2pZoneInput {
			val := strings.TrimSuffix(
				string(msg.Content), "\n")
			s.input.SetValue(val)
		}
		return s, nil
	}
	return s, nil
}

// ── View ────────────────────────────────────────────────

func (s *P2PUpgradeScreen) View(
	w, h int,
) string {
	if s.step == p2pLoading {
		p := newPane(w)
		p.title(theme.Header, "Reading Public IPv4")
		p.line(" " + theme.Value.Render("Checking the address for your review..."))
		return p.render()
	}
	if s.step == p2pNoIP {
		return s.viewNoIP(w, h)
	}
	if s.step == p2pProgress && s.progress != nil {
		return s.progress.View(w, h)
	}
	if s.step == p2pConfirm2 {
		return s.viewConfirm2(w, h)
	}
	return s.viewConfirm(w, h)
}

func (s *P2PUpgradeScreen) viewNoIP(
	w, h int,
) string {
	isFocused := s.ctx.ContentFocused
	p := newPane(w)

	p.title(theme.Header,
		"Cannot Detect Public IP")
	p.line(" " + theme.Value.Render(
		"Could not determine your server's"))
	p.line(" " + theme.Value.Render(
		"public IP address."))
	p.blank()
	p.line(" " + theme.Value.Render(
		"Hybrid mode requires a public IPv4"))
	p.line(" " + theme.Value.Render(
		"address."))

	return p.renderWithBottomButtons(
		[]string{"Retry"}, 0, isFocused, h)
}

func (s *P2PUpgradeScreen) viewConfirm(
	w, h int,
) string {
	isFocused := s.ctx.ContentFocused
	p := newPane(w)

	p.title(theme.Header,
		"Upgrade to Clearnet + Tor (Hybrid P2P)")

	p.field("Server IP: ", s.request.Address())
	p.blank()
	p.line(" " + theme.Warn.Render(
		"This will permanently change your"+
			" node's privacy:"))
	p.blank()
	p.line(" " + theme.Value.Render(
		"  • Your server IP will be published to the"))
	p.line(" " + theme.Value.Render(
		"    Lightning Network gossip protocol"))
	p.line(" " + theme.Value.Render(
		"  • Every node on the network will"+
			" learn your IP"))
	p.line(" " + theme.Value.Render(
		"  • This links your IP to your Lightning"+
			" node identity"))
	p.line(" " + theme.Value.Render(
		"  • This CANNOT be undone. Once published,"))
	p.line(" " + theme.Value.Render(
		"    your IP cannot be retracted from"+
			" network gossip"))
	p.blank()
	p.line(" " + theme.Value.Render(
		"It will also:"))
	p.blank()
	p.line(" " + theme.Value.Render(
		"  • Open ports 9735 and 8080"+
			" in the firewall"))
	p.line(" " + theme.Value.Render(
		"  • Expose LND's full REST API (port 8080)"))
	p.line(" " + theme.Value.Render(
		"    to the internet, protected by TLS and"))
	p.line(" " + theme.Value.Render(
		"    macaroon authentication"))
	p.line(" " + theme.Value.Render(
		"  • Allow Zeus to connect over clearnet"))
	p.line(" " + theme.Value.Render(
		"  • Restart LND with the new config"))

	p.blank()
	p.input("Type PUBLISH MY IP to proceed:",
		s.input.View(),
		isFocused &&
			s.focusZone == p2pZoneInput)

	p.appendError(s.error)

	return p.renderWithBottomButtons(
		[]string{"Cancel", "Proceed"},
		s.btnIdx,
		isFocused &&
			s.focusZone == p2pZoneButtons, h)
}

func (s *P2PUpgradeScreen) viewConfirm2(
	w, h int,
) string {
	isFocused := s.ctx.ContentFocused
	p := newPane(w)

	p.title(theme.Warning, "Final Confirmation")
	p.blank()
	p.line(" " + theme.Value.Render(
		"Your IP address:"))
	p.blank()
	p.mono(s.request.Address())
	p.blank()
	p.line(" " + theme.Value.Render(
		"will be permanently published to the"))
	p.line(" " + theme.Value.Render(
		"Lightning Network."))
	p.blank()
	p.line(" " + theme.Warn.Render(
		"This cannot be undone."))

	return p.renderWithBottomButtons(
		[]string{"Go Back", "Confirm"},
		s.confirm2Idx,
		isFocused, h)
}

// ── HelpBindings ────────────────────────────────────────

func (s *P2PUpgradeScreen) HelpBindings() []key.Binding {
	if s.step == p2pNoIP {
		return []key.Binding{bind("enter", "retry", "enter"), kSidebar, kBack, kQuit}
	}
	if s.step == p2pLoading {
		return []key.Binding{kSidebar, kBack, kQuit}
	}

	if s.step == p2pProgress && s.progress != nil {
		return s.progress.HelpBindings()
	}

	if s.step == p2pConfirm2 {
		return actionButtonBindings(
			s.confirm2Idx, s.ctx.HasTabs)
	}

	// Confirm step
	if s.focusZone == p2pZoneInput {
		return []key.Binding{
			bind("enter", "buttons", "enter"),
			kTabButtons,
			kBack,
			kQuit,
		}
	}

	// Button zone
	var binds []key.Binding
	if s.btnIdx == 0 {
		binds = append(binds,
			kSidebar,
			kRightButton)
	} else {
		binds = append(binds,
			kLeftRightButtons)
	}
	binds = append(binds,
		kEnter,
		kShiftTabInput,
		kBack)
	if s.ctx.HasTabs {
		binds = append(binds,
			kUpTabBar)
	}
	binds = append(binds, kQuit)
	return binds
}

// Each mounted screen owns at most one observation. Replaced or closed screens
// cannot publish a late address into a different confirmation.
type p2pAddressRead struct {
	owner  *P2PUpgradeScreen
	cancel context.CancelFunc
}

type p2pAddressResultMsg struct {
	read    *p2pAddressRead
	request p2p.UpgradeRequest
	err     error
}

func (s *P2PUpgradeScreen) readAddress() tea.Cmd {
	if s.addressRead != nil {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	read := &p2pAddressRead{owner: s, cancel: cancel}
	s.addressRead = read
	s.request = p2p.UpgradeRequest{}
	s.error = ""
	s.step = p2pLoading
	workflows := s.ctx.HelperWorkflows
	return func() tea.Msg {
		request, err := workflows.ReadP2PAddress(ctx)
		return p2pAddressResultMsg{read: read, request: request, err: err}
	}
}

func (s *P2PUpgradeScreen) cancelAddressRead() {
	if s.addressRead != nil {
		s.addressRead.cancel()
		s.addressRead = nil
	}
}

func cancelP2PAddressRead(screen Screen) {
	if s, ok := screen.(*P2PUpgradeScreen); ok {
		s.cancelAddressRead()
	}
}
