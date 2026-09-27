package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/x/ansi"
	"github.com/virtualprivatenode/vpn/internal/accountaccess"
	"github.com/virtualprivatenode/vpn/internal/app"
	"github.com/virtualprivatenode/vpn/internal/theme"
)

// ── SSHKeysScreen ──────────────────────────────────────
// Pure list/management screen. Add and per-key detail
// are opened as their own tabs (tabSSHKeyAdd,
// tabSSHKeyDetail). Mirrors the Syncthing manage pattern.
// ── Focus zones ────────────────────────────────────────

const (
	sshZoneButtons = 0
	sshZoneKeys    = 1
)

// ── Messages ───────────────────────────────────────────

// Read and mutation results belong to a screen instance and request number.
type sshKeysListMsg struct {
	owner   *SSHKeysScreen
	request uint64
	keys    []app.SSHKey
	err     error
}
type sshKeyAddMsg struct {
	owner   *SSHKeyAddScreen
	attempt uint64
	err     error
}
type sshKeyRemoveMsg struct {
	owner   *SSHKeyDetailScreen
	attempt uint64
	err     error
}
type refreshSSHKeysMsg struct{}
type closeSSHScreenMsg struct{ screen Screen }

func refreshSSHKeysCmd() tea.Msg { return refreshSSHKeysMsg{} }
func closeSSHScreenCmd(screen Screen) tea.Cmd {
	return func() tea.Msg { return closeSSHScreenMsg{screen: screen} }
}

func (s *SSHKeysScreen) refresh() tea.Cmd {
	s.request++
	request, access := s.request, s.ctx.sshAccess()
	return func() tea.Msg {
		keys, err := access.ListKeys()
		return sshKeysListMsg{owner: s, request: request, keys: keys, err: err}
	}
}

func (s *SSHKeyAddScreen) addCommand(line string) tea.Cmd {
	s.attempt++
	attempt, access := s.attempt, s.ctx.sshAccess()
	return func() tea.Msg { return sshKeyAddMsg{owner: s, attempt: attempt, err: access.AddKey(line)} }
}
func (s *SSHKeyDetailScreen) removeCommand() tea.Cmd {
	s.attempt++
	attempt, fingerprint, access := s.attempt, s.keyInfo.Fingerprint, s.ctx.sshAccess()
	return func() tea.Msg { return sshKeyRemoveMsg{owner: s, attempt: attempt, err: access.RemoveKey(fingerprint)} }
}

// ── Screen ─────────────────────────────────────────────

type SSHKeysScreen struct {
	ctx     *ScreenContext
	request uint64

	keys          []app.SSHKey
	keyCursor     int
	loadErr       error
	loaded        bool
	focusZone     int
	btnIdx        int
	information   bool
	detail        *app.AccountDetails
	detailErr     error
	detailRequest uint64
	accountRef    accountaccess.Ref
	scroll        int
}

func NewSSHKeysScreen(
	ctx *ScreenContext,
) *SSHKeysScreen {
	return &SSHKeysScreen{ctx: ctx}
}

func (s *SSHKeysScreen) Init() tea.Cmd {
	return tea.Batch(s.refresh(), fetchSSHPasswordAuthCmd())
}

func (s *SSHKeysScreen) HandleKey(
	keyStr string, msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
	s.clampCursor()

	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit

	case "left":
		if s.focusZone == sshZoneButtons &&
			s.btnIdx > 0 {
			s.btnIdx--
			return s, nil
		}
		return s, emitFocusSidebar

	case "right":
		if s.focusZone == sshZoneButtons &&
			s.btnIdx < 3 {
			s.btnIdx++
		}
		return s, nil

	case "up":
		if s.scroll > 0 {
			s.scroll--
			return s, nil
		}
		if s.focusZone == sshZoneKeys {
			if s.keyCursor > 0 {
				s.keyCursor--
			} else {
				s.focusZone = sshZoneButtons
				s.btnIdx = 0
			}
			return s, nil
		}
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
		return s, nil

	case "down", "tab":
		if keyStr == "down" && s.information && (len(s.keys) == 0 || (s.focusZone == sshZoneKeys && s.keyCursor == len(s.keys)-1)) {
			s.scroll++
			return s, nil
		}
		if s.focusZone == sshZoneButtons {
			if len(s.keys) > 0 {
				s.focusZone = sshZoneKeys
				s.keyCursor = 0
				s.scroll = 0
			}
			return s, nil
		}
		if s.focusZone == sshZoneKeys {
			if s.keyCursor < len(s.keys)-1 {
				s.keyCursor++
			}
		}
		return s, nil

	case "shift+tab":
		s.scroll = 0
		if s.focusZone == sshZoneKeys {
			s.focusZone = sshZoneButtons
			s.btnIdx = 0
			return s, nil
		}
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
		return s, nil

	case "backspace":
		return s, emitFocusParent

	case "enter":
		if s.focusZone == sshZoneButtons {
			switch s.btnIdx {
			case 0:
				return s.openAddTab()
			case 1:
				return s.openPasswordAuthTab()
			case 2:
				return s.openChangePasswordTab()
			case 3:
				s.information, s.scroll = !s.information, 0
				if s.information {
					return s, s.readInformation()
				}
			}
			return s, nil
		}
		return s.openDetailTab()
	}
	return s, nil
}

func (s *SSHKeysScreen) HandleMsg(
	msg tea.Msg,
) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tabActivatedMsg:
		var info tea.Cmd
		if s.information {
			info = s.readInformation()
		}
		return s, tea.Batch(s.refresh(), fetchSSHPasswordAuthCmd(), info)
	case accountDetailMsg:
		if msg.owner != s || msg.request != s.detailRequest {
			return s, nil
		}
		s.detailErr = msg.err
		if msg.err == nil {
			s.detail = &msg.detail
		}
		return s, nil
	case sshKeysListMsg:
		if msg.owner != s || msg.request != s.request {
			return s, nil
		}
		selected := ""
		if s.keyCursor >= 0 && s.keyCursor < len(s.keys) {
			selected = s.keys[s.keyCursor].Fingerprint
		}
		s.loaded = true
		s.loadErr = msg.err
		if msg.err != nil {
			return s, nil
		}
		s.keys = msg.keys
		s.keyCursor = 0
		found := false
		for i, key := range s.keys {
			if key.Fingerprint == selected {
				s.keyCursor = i
				found = true
				break
			}
		}
		if !found {
			s.focusZone = sshZoneButtons
		}
		s.clampCursor()
		return s, nil
	}
	return s, nil
}

func (s *SSHKeysScreen) readInformation() tea.Cmd {
	s.detailRequest++
	request, access, ref := s.detailRequest, s.ctx.accountAccess(), s.accountRef
	s.detail, s.detailErr = nil, nil
	return func() tea.Msg {
		detail, err := access.Inspect(ref)
		return accountDetailMsg{owner: s, request: request, detail: detail, err: err}
	}
}

func (s *SSHKeysScreen) View(w, h int) string {
	return s.viewList(w, h)
}

func (s *SSHKeysScreen) HelpBindings() []key.Binding {
	if s.focusZone == sshZoneButtons {
		return manageButtonBindings(
			"keys", s.btnIdx, s.ctx.HasTabs)
	}
	return homeListBindings(
		"navigate", "open", "buttons")
}

// ── Tab openers ────────────────────────────────────────

func (s *SSHKeysScreen) openAddTab() (Screen, tea.Cmd) {
	screen := NewSSHKeyAddScreen(s.ctx)
	return s, func() tea.Msg {
		return openTabMsg{
			Kind:   tabSSHKeyAdd,
			Label:  "Add Key",
			Screen: screen,
			Parent: tabSSHKeys,
		}
	}
}

func (s *SSHKeysScreen) openPasswordAuthTab() (
	Screen, tea.Cmd,
) {
	screen := NewSSHPasswordAuthScreen(s.ctx)
	return s, func() tea.Msg {
		return openTabMsg{
			Kind:   tabSSHPasswordAuth,
			Label:  "Password Auth",
			Screen: screen,
			Parent: tabSSHKeys,
		}
	}
}

func (s *SSHKeysScreen) openChangePasswordTab() (
	Screen, tea.Cmd,
) {
	screen := NewChangePasswordScreen(s.ctx)
	return s, func() tea.Msg {
		return openTabMsg{
			Kind:   tabSSHChangePassword,
			Label:  "Change Password",
			Screen: screen,
			Parent: tabSSHKeys,
		}
	}
}

func (s *SSHKeysScreen) openDetailTab() (Screen, tea.Cmd) {
	if s.loadErr != nil || s.keyCursor < 0 || s.keyCursor >= len(s.keys) {
		return s, nil
	}
	k := s.keys[s.keyCursor]
	label := theme.PlainText(k.Comment)
	if label == "" {
		label = k.Type
	}
	if len(label) > 17 {
		label = label[:17] + "..."
	}
	screen := NewSSHKeyDetailScreen(
		s.ctx, k)
	return s, func() tea.Msg {
		return openTabMsg{
			Kind:   tabSSHKeyDetail,
			Label:  label,
			Key:    k.Fingerprint,
			Screen: screen,
			Parent: tabSSHKeys,
		}
	}
}

// ── View ──────────────────────────────────────────────

func (s *SSHKeysScreen) viewList(w, h int) string {
	s.clampCursor()
	isFocused := s.ctx.ContentFocused
	onButtons := isFocused &&
		s.focusZone == sshZoneButtons

	// ── Fixed header: title + button ────────────
	var headerLines []string
	headerLines = append(headerLines, "")
	headerLines = append(headerLines,
		centerPad(
			theme.Header.Render("vpn"), w))
	headerLines = append(headerLines, "")
	headerLines = append(headerLines,
		renderButtons(
			[]string{
				"Add Key",
				"Password Auth",
				"Change Password",
				"Account information",
			},
			s.btnIdx, onButtons, w))
	headerLines = append(headerLines, "")

	// The status comes from sshd's effective configuration.
	pwAuthLabel := theme.Success.Render("enabled")
	if !s.ctx.State.SSHPasswordAuthKnown {
		pwAuthLabel = theme.Warning.Render("unavailable")
	} else if s.ctx.State.SSHPasswordAuthDisabled {
		pwAuthLabel = theme.Warning.Render("disabled")
	}
	headerLines = append(headerLines,
		" "+theme.Label.Render(
			"Effective Password Auth: ")+
			pwAuthLabel)
	headerLines = append(headerLines, "")

	headerH := len(strings.Split(strings.Join(headerLines, "\n"), "\n"))
	header := strings.Join(headerLines, "\n")

	// ── Scrollable body ────────────────────────
	cursorLine := 0
	var midLines []string

	if !s.loaded {
		midLines = append(midLines, " "+theme.Dim.Render("Loading authorized keys..."))
	} else if s.loadErr != nil {
		midLines = append(midLines,
			" "+theme.Warning.Render(
				s.loadErr.Error()))
	} else if len(s.keys) == 0 {
		midLines = append(midLines,
			" "+theme.Dim.Render(
				"No authorized keys found"))
		midLines = append(midLines, "")
		midLines = append(midLines,
			" "+theme.Dim.Render(
				"Press enter to add a key"))
	} else {
		body := newPane(w)
		body.lines = nil
		widths := accountKeyWidths(w)
		accountTableRow(body, []string{"Name", "Type", "Status"}, widths, theme.TableHeader, " ")
		for i, k := range s.keys {
			style, marker := theme.Value, " "
			selected := s.focusZone == sshZoneKeys && s.keyCursor == i
			if selected && isFocused {
				style, marker = theme.NavActive, "▸"
			}
			name := k.Comment
			if name == "" {
				name = "SSH key"
			}
			accountTableRow(body, []string{ansi.Truncate(theme.PlainText(name), widths[0], "…"), k.Type, "Configured"}, widths, style, marker)
			if selected {
				cursorLine = len(body.lines) - 1
			}
		}
		midLines = append(midLines, body.lines...)
	}
	if s.information {
		body := newPane(w)
		if s.detailErr != nil {
			accountText(body, theme.Warning, s.detailErr.Error())
		} else if s.detail == nil {
			body.dim("Reading account information...")
		} else {
			renderAccountInformation(body, s.detail)
		}
		midLines = append(midLines, body.lines...)
	}

	midContent := strings.Join(midLines, "\n")

	// ── Viewport ───────────────────────────────
	vpH := h - headerH
	if vpH < 1 {
		vpH = 1
	}

	vpRendered := renderAccountViewport(midContent, w, vpH, cursorLine, &s.scroll)

	return header + "\n" + vpRendered
}

// ── Helpers ───────────────────────────────────────────

func (s *SSHKeysScreen) clampCursor() {
	if s.keyCursor >= len(s.keys) {
		s.keyCursor = len(s.keys) - 1
	}
	if s.keyCursor < 0 {
		s.keyCursor = 0
	}
	if len(s.keys) == 0 &&
		s.focusZone == sshZoneKeys {
		s.focusZone = sshZoneButtons
		s.btnIdx = 0
	}
}

func sshAccessBusy(screen Screen) bool {
	switch s := screen.(type) {
	case *AccountsScreen:
		return s.working
	case *ChangePasswordScreen:
		return s.step == changePwStepWorking
	case *SSHKeyAddScreen:
		return s.step == sshAddStepWorking
	case *SSHKeyDetailScreen:
		return s.step == sshKeyDetailStepWorking
	case *SSHPasswordAuthScreen:
		return s.step == sshPwAuthStepWorking
	}
	return false
}

// SSH results reach only their originating screen, including across sections.
func (m Model) routeSSHResult(owner Screen, msg tea.Msg) (tea.Model, tea.Cmd) {
	for i, tab := range m.tabs {
		if tab.Screen == owner {
			screen, cmd := tab.Screen.HandleMsg(msg)
			m.tabs[i].Screen = screen
			return m, cmd
		}
	}
	return m, nil
}
