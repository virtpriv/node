package tui

import (
	"errors"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/virtpriv/node/internal/accountaccess"
	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/theme"
)

type accountAccess interface {
	List() (accountaccess.Inventory, error)
	Inspect(accountaccess.Ref) (app.AccountDetails, error)
	Import(app.AccountKeyImport) error
	Close()
}

func (c *ScreenContext) accountAccess() accountAccess {
	if c.AccountAccess == nil {
		c.AccountAccess = app.NewAccountAccess(c.sshAccess())
	}
	return c.AccountAccess
}

type accountsMsg struct {
	owner     *AccountsScreen
	request   uint64
	inventory accountaccess.Inventory
	err       error
}
type accountDetailMsg struct {
	owner   Screen
	request uint64
	detail  app.AccountDetails
	err     error
}
type accountImportMsg struct {
	owner   *AccountsScreen
	attempt uint64
	err     error
}

// AccountsScreen keeps a review fixed until canceled or submitted. Reads and
// mutations return to this exact screen even when another section is visible.
type AccountsScreen struct {
	ctx                                   *ScreenContext
	accounts                              []accountaccess.Account
	access                                map[string]accountaccess.SystemAccess
	cursor                                int
	account                               *accountaccess.Account
	detail                                *app.AccountDetails
	keyCursor                             int
	request, attempt                      uint64
	loading, loaded, working, resultReady bool
	err                                   error
	review                                *app.AccountKeyImport
	resultErr                             error
	scroll                                int
	focusZone, btnIdx, confirmIdx         int
	information                           bool
	configured                            bool
}

func NewAccountsScreen(ctx *ScreenContext) *AccountsScreen { return &AccountsScreen{ctx: ctx} }
func (s *AccountsScreen) Init() tea.Cmd                    { return s.refresh() }

func openAccountsCmd(ctx *ScreenContext) tea.Cmd {
	return func() tea.Msg {
		return openTabMsg{Kind: tabAccounts, Label: "Accounts", Screen: NewAccountsScreen(ctx)}
	}
}

func (s *AccountsScreen) refresh() tea.Cmd {
	if s.loading || s.working || s.review != nil || s.resultReady {
		return nil
	}
	s.request++
	s.loading, s.err = true, nil
	request, access := s.request, s.ctx.accountAccess()
	if s.account != nil {
		ref := s.account.Ref()
		return func() tea.Msg {
			detail, err := access.Inspect(ref)
			return accountDetailMsg{owner: s, request: request, detail: detail, err: err}
		}
	}
	return func() tea.Msg {
		inventory, err := access.List()
		return accountsMsg{owner: s, request: request, inventory: inventory, err: err}
	}
}

func (s *AccountsScreen) HandleMsg(msg tea.Msg) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tabActivatedMsg:
		return s, s.refresh()
	case accountsMsg:
		if msg.owner != s || msg.request != s.request || s.account != nil || !s.loading {
			return s, nil
		}
		s.loading, s.loaded, s.err = false, true, msg.err
		if msg.err == nil {
			var selected accountaccess.Ref
			visible := s.visibleAccounts()
			if s.cursor < len(visible) {
				selected = visible[s.cursor].Ref()
			}
			s.accounts, s.access, s.cursor = msg.inventory.Accounts, msg.inventory.Access, 0
			for i, a := range s.visibleAccounts() {
				if a.Ref() == selected {
					s.cursor = i
					break
				}
			}
		}
	case accountDetailMsg:
		if msg.owner != s || msg.request != s.request || !s.loading || s.review != nil || s.working || s.account == nil {
			return s, nil
		}
		s.loading, s.err = false, msg.err
		if msg.err == nil {
			if msg.detail.Account.Ref() != s.account.Ref() {
				s.err = errors.New("account observation changed; refresh before continuing")
				return s, nil
			}
			fingerprint := ""
			if s.detail != nil && s.keyCursor < len(s.detail.Source.Keys) {
				fingerprint = s.detail.Source.Keys[s.keyCursor].Fingerprint
			}
			s.detail, s.keyCursor = &msg.detail, 0
			for i, k := range s.detail.Source.Keys {
				if k.Fingerprint == fingerprint {
					s.keyCursor = i
					break
				}
			}
		}
	case accountImportMsg:
		if msg.owner != s || msg.attempt != s.attempt || !s.working {
			return s, nil
		}
		s.working, s.resultReady, s.resultErr, s.scroll = false, true, msg.err, 0
		return s, refreshSSHKeysCmd
	}
	return s, nil
}

func (s *AccountsScreen) visibleAccounts() []accountaccess.Account {
	var visible []accountaccess.Account
	for _, a := range s.accounts {
		if a.KeyDiscoverySupported() {
			visible = append(visible, a)
		}
	}
	return visible
}

func (s *AccountsScreen) buttons() []string {
	labels := []string{"Back"}
	if s.account != nil {
		labels = append(labels, "Account information")
	}
	retry := s.err != nil || (s.detail != nil && (s.detail.Source.Problem != "" || s.detail.OwnerKeysProblem != ""))
	if s.account == nil && s.loaded && !s.loading {
		for _, a := range s.visibleAccounts() {
			retry = retry || s.access[a.Name] == accountaccess.AccessUnavailable
		}
	}
	if retry {
		labels = append(labels, "Retry")
	}
	return labels
}

func (s *AccountsScreen) listLen() int {
	if s.account == nil {
		return len(s.visibleAccounts())
	}
	if s.detail != nil {
		return len(s.detail.Source.Keys)
	}
	return 0
}

func (s *AccountsScreen) back() tea.Cmd {
	s.scroll, s.btnIdx, s.focusZone = 0, 0, sshZoneButtons
	s.information = false
	if s.account == nil {
		return emitFocusParent
	}
	s.request++ // Retire any outstanding detail read.
	s.account, s.detail, s.err, s.loading = nil, nil, nil, false
	return s.refresh()
}

func (s *AccountsScreen) HandleKey(k string, _ tea.KeyPressMsg) (Screen, tea.Cmd) {
	s.btnIdx = min(s.btnIdx, len(s.buttons())-1)
	if k == "ctrl+c" {
		return s, tea.Quit
	}
	if s.working || s.resultReady {
		switch k {
		case "left", "esc", "backspace":
			return s, emitFocusSidebar
		case "shift+tab":
			return s, emitFocusTabBar
		case "up":
			s.scroll = max(0, s.scroll-1)
		case "down":
			s.scroll++
		}
		if s.working {
			return s, nil
		}
	}
	if s.resultReady {
		switch k {
		case "enter":
			s.resultReady, s.review, s.detail = false, nil, nil
			s.focusZone, s.btnIdx, s.scroll = sshZoneButtons, 0, 0
			return s, s.refresh()
		}
		return s, nil
	}
	if s.review != nil {
		switch k {
		case "down":
			s.scroll++
		case "up":
			s.scroll = max(0, s.scroll-1)
		case "left":
			if s.confirmIdx == 0 {
				return s, emitFocusSidebar
			}
			s.confirmIdx = 0
		case "right":
			if !s.configured {
				s.confirmIdx = 1
			}
		case "shift+tab":
			return s, emitFocusTabBar
		case "esc", "backspace":
			s.review, s.scroll = nil, 0
		case "enter":
			if s.confirmIdx == 0 {
				s.review, s.scroll = nil, 0
				return s, nil
			}
			s.working = true
			s.attempt++
			attempt, review, access := s.attempt, *s.review, s.ctx.accountAccess()
			return s, func() tea.Msg { return accountImportMsg{owner: s, attempt: attempt, err: access.Import(review)} }
		}
		return s, nil
	}
	switch k {
	case "left":
		if s.focusZone == sshZoneButtons && s.btnIdx > 0 {
			s.btnIdx--
			return s, nil
		}
		return s, emitFocusSidebar
	case "right":
		if s.focusZone == sshZoneButtons {
			s.btnIdx = min(len(s.buttons())-1, s.btnIdx+1)
		}
	case "up", "shift+tab":
		if k == "up" && s.scroll > 0 {
			s.scroll--
			return s, nil
		}
		s.scroll = 0
		if s.focusZone == sshZoneButtons {
			return s, emitFocusTabBar
		}
		cursor := &s.cursor
		if s.account != nil {
			cursor = &s.keyCursor
		}
		if k == "shift+tab" || *cursor == 0 {
			s.focusZone, s.btnIdx = sshZoneButtons, 0
		} else {
			*cursor--
		}
	case "down", "tab":
		if k == "down" && s.information && (s.listLen() == 0 || (s.focusZone == sshZoneKeys && s.keyCursor == s.listLen()-1)) {
			s.scroll++
			return s, nil
		}
		s.scroll = 0
		if s.listLen() == 0 {
			return s, nil
		}
		if s.focusZone == sshZoneButtons {
			s.focusZone = sshZoneKeys
			return s, nil
		}
		if s.account == nil {
			s.cursor = min(s.listLen()-1, s.cursor+1)
		} else {
			s.keyCursor = min(s.listLen()-1, s.keyCursor+1)
		}
	case "backspace", "esc":
		return s, s.back()
	case "r":
		return s, s.refresh()
	case "enter":
		if s.focusZone == sshZoneButtons {
			if s.btnIdx == 0 {
				return s, s.back()
			}
			if s.account != nil && s.btnIdx == 1 {
				s.information, s.scroll = !s.information, 0
				return s, nil
			}
			return s, s.refresh()
		}
		if s.loading || s.err != nil {
			return s, nil
		}
		if s.account == nil {
			visible := s.visibleAccounts()
			if s.cursor < len(visible) {
				a := visible[s.cursor]
				if a.Name == "vpn" {
					return s, func() tea.Msg {
						screen := NewSSHKeysScreen(s.ctx)
						screen.accountRef = a.Ref()
						return openTabMsg{Kind: tabSSHKeys, Label: "vpn", Screen: screen, Parent: tabAccounts}
					}
				}
				s.account, s.scroll, s.focusZone, s.btnIdx = &a, 0, sshZoneButtons, 0
				return s, s.refresh()
			}
		} else if s.detail != nil && s.detail.Source.Problem == "" && s.detail.OwnerKeysProblem == "" &&
			s.detail.Account.Name != "vpn" && s.keyCursor < len(s.detail.Source.Keys) {
			key := s.detail.Source.Keys[s.keyCursor]
			s.configured = s.detail.Authorized[key.Fingerprint]
			s.review = &app.AccountKeyImport{Account: s.detail.Account, Key: key}
			s.scroll, s.confirmIdx = 0, 0
		}
	}
	return s, nil
}

// Keep navigation visible while account and key rows follow the cursor.
func (s *AccountsScreen) renderBody(header, body *paneBuilder, w, h, cursor int) string {
	head := header.render()
	vpH := max(1, h-strings.Count(head, "\n")-1)
	return head + "\n" + renderAccountViewport(body.render(), w, vpH, cursor, &s.scroll)

}

// Row navigation follows the selected row; extra arrow presses scroll through
// expanded information below it. Both account views use the shared viewport.
func renderAccountViewport(text string, w, h, cursor int, scroll *int) string {
	lines := strings.Count(text, "\n") + 1
	base := max(0, cursor-h+1)
	*scroll = min(*scroll, max(0, lines-h-base))
	if *scroll > 0 {
		cursor = base + *scroll + h - 1
	}
	return renderViewport(text, w, h, cursor, lines, true)
}

func accountText(p *paneBuilder, style lipgloss.Style, text string) {
	wrapped := style.Width(max(1, p.w-2)).Render(theme.PlainText(text))
	for _, line := range strings.Split(wrapped, "\n") {
		p.line(" " + line)
	}
}

func (s *AccountsScreen) View(w, h int) string {
	w, h = max(1, w), max(1, h)
	if s.review != nil || s.resultReady {
		return s.viewImport(w, h)
	}
	header, body := newPane(w), newPane(w)
	s.btnIdx = min(s.btnIdx, len(s.buttons())-1)
	title := "Accounts"
	if s.account != nil {
		title = theme.PlainText(s.account.Name)
	}
	header.title(theme.Header, title).buttons(s.buttons(), s.btnIdx, s.ctx.ContentFocused && s.focusZone == sshZoneButtons)
	cursor := 0
	switch {
	case s.err != nil:
		body.warn("Observation unavailable")
		accountText(body, theme.Warning, s.err.Error())
		body.blank().dim("Choose Retry to try again.")
	case s.account == nil:
		body.valueWrap("Use an SSH key from another account to connect as vpn.").blank()
		if s.loading {
			body.dim("Refreshing accounts...").blank()
		}
		visible := s.visibleAccounts()
		if s.loaded && len(visible) == 0 {
			body.dim("No local accounts to show.")
		}
		widths := []int{min(18, max(8, (w-3)/3)), 0}
		widths[1] = max(1, w-3-widths[0])
		accountTableRow(body, []string{"Account", "System access"}, widths, theme.TableHeader, " ")
		for i, a := range visible {
			selected := s.focusZone == sshZoneKeys && s.cursor == i
			style, marker := theme.Value, " "
			if selected && s.ctx.ContentFocused {
				style, marker = theme.NavActive, "▸"
			}
			accountTableRow(body, []string{a.Name, accountAccessLabel(s.access[a.Name])}, widths, style, marker)
			if selected {
				cursor = len(body.lines) - 1
			}
		}

	case s.detail == nil:
		body.dim("Reading account access...")
	default:
		d := s.detail
		if s.loading {
			body.dim("Refreshing key status...").blank()
		}
		switch {
		case !d.Account.KeyDiscoverySupported():
			body.valueWrap("Key import is not offered for this service or non-login account.")
		case d.Source.Problem != "":
			body.warn("Key discovery unavailable")
			accountText(body, theme.Warning, d.Source.Problem)
		case d.OwnerKeysProblem != "":
			body.warn("vpn key status unavailable").valueWrap("We could not check which keys vpn already has. Choose Retry before importing.")
		case len(d.Source.Keys) == 0:
			body.valueWrap("No supported keys found in this account's standard SSH key file.")
		default:
			available := 0
			for _, k := range d.Source.Keys {
				if !d.Authorized[k.Fingerprint] {
					available++
				}
			}
			if available == 0 {
				body.success("No import needed").valueWrap("All keys shown below are already configured for vpn.")
			} else {
				body.valueWrap("Select a key to review before adding it to vpn. Use a key only if you recognize its owner.")
			}
		}
		if d.Source.Excluded > 0 {
			body.blank().valueWrap(fmt.Sprintf("%d restricted or unsupported key entries cannot be imported.", d.Source.Excluded))
		}
		body.blank()
		widths := accountKeyWidths(w)
		accountTableRow(body, []string{"Name", "Type", "Status"}, widths, theme.TableHeader, " ")
		for i, k := range d.Source.Keys {
			selected := s.focusZone == sshZoneKeys && s.keyCursor == i
			style, marker := theme.Value, " "
			if selected && s.ctx.ContentFocused {
				style, marker = theme.NavActive, "▸"
			}
			label := k.Comment
			if label == "" {
				label = "SSH key"
			}
			status := "Available to import"
			if d.Authorized[k.Fingerprint] {
				status = "Configured for vpn"
			}
			if d.Source.Problem != "" || d.OwnerKeysProblem != "" {
				status = "Unavailable"
			}
			accountTableRow(body, []string{ansi.Truncate(theme.PlainText(label), widths[0], "…"), k.Type, status}, widths, style, marker)
			if selected {
				cursor = len(body.lines) - 1
			}
		}
		if s.information {
			renderAccountInformation(body, d)
		}

	}
	return s.renderBody(header, body, w, h, cursor)
}

func renderAccountInformation(p *paneBuilder, d *app.AccountDetails) {
	p.blank().labelLine("Account information")
	p.field("UID: ", fmt.Sprint(d.Account.UID)).field("GID: ", fmt.Sprint(d.Account.GID))
	accountText(p, theme.Value, "Home: "+d.Account.Home)
	accountText(p, theme.Value, "Shell: "+d.Account.Shell)
	if d.GroupsProblem != "" {
		accountText(p, theme.Warning, d.GroupsProblem)
	} else {
		accountText(p, theme.Value, "Unix groups: "+strings.Join(d.Groups, ", "))
	}
	accountText(p, theme.Value, "Key file: "+d.Source.Path)
}

func accountAccessLabel(access accountaccess.SystemAccess) string {
	switch access {
	case accountaccess.AccessRoot:
		return "Root account"
	case accountaccess.AccessPassword:
		return "Full sudo access • password required"
	case accountaccess.AccessPasswordless:
		return "Full sudo access • no password required"
	case accountaccess.AccessReview:
		return "Needs review"
	default:
		return "Unavailable"
	}
}

func accountKeyWidths(w int) []int {
	usable := max(3, w-4)
	return []int{max(1, usable*2/5), max(1, usable/4), max(1, usable-usable*2/5-usable/4)}
}

// Wrap cells so access status remains readable. The same table
// renderer serves imported sources and the owner's existing key management.
func accountTableRow(p *paneBuilder, cells []string, widths []int, style lipgloss.Style, marker string) {
	blocks := make([]string, len(cells))
	for i, cell := range cells {
		blocks[i] = style.Width(widths[i]).Render(theme.PlainText(cell))
		if i < len(cells)-1 {
			blocks[i] = lipgloss.NewStyle().PaddingRight(1).Render(blocks[i])
		}
	}
	row := lipgloss.JoinHorizontal(lipgloss.Top, blocks...)
	for i, line := range strings.Split(row, "\n") {
		prefix := "  "
		if i == 0 {
			prefix = marker + " "
		}
		p.line(prefix + line)
	}
}

func (s *AccountsScreen) viewImport(w, h int) string {
	p := newPane(w)
	labels, active, focused := []string{"Go Back", "Import Key"}, s.confirmIdx, s.ctx.ContentFocused
	if s.configured {
		labels, active = []string{"Back"}, 0
	}
	if s.resultReady {
		labels, active = []string{"Return"}, 0
		if s.resultErr != nil {
			p.title(theme.Warning, "Import not confirmed")
			accountText(p, theme.Warning, s.resultErr.Error())
			p.blank().valueWrap("Return to read the current key status before retrying.")
		} else {
			p.title(theme.Success, "Key imported")
			p.valueWrap("Keep this session open and test a new SSH connection as vpn using this key.")
		}
	} else {
		if s.configured {
			p.title(theme.Header, "SSH key")
		} else {
			p.title(theme.Header, "Import key into vpn")
		}
		p.field("From: ", theme.PlainText(s.review.Account.Name))
		accountText(p, theme.Value, theme.PlainText(s.review.Key.Comment))
		p.labelLine("Fingerprint:").monoWrap(s.review.Key.Fingerprint).blank()
		if s.configured {
			p.success("Already configured for vpn")
		} else {
			p.warnWrapWords("The holder of this key will gain vpn SSH and node access, including wallet access.")
			p.blank().valueWrap("The source account and key are kept. Sudo policy stays the same.")
		}
		if s.working {
			labels, active, focused = []string{"Importing..."}, 0, false
		}
	}
	// Keep the action reachable on short terminals while the explanation scrolls.
	buttons := renderButtons(labels, active, focused, w)
	vpH := max(1, h-strings.Count(buttons, "\n")-2)
	text := p.render()
	lines := strings.Count(text, "\n") + 1
	s.scroll = min(s.scroll, max(0, lines-vpH))
	content := renderViewport(text, w, vpH, min(lines-1, s.scroll+vpH-1), lines, true)
	return content + "\n\n" + buttons
}

func (s *AccountsScreen) HelpBindings() []key.Binding {
	if s.working {
		return []key.Binding{bind("↑↓", "scroll", "up", "down"), bind("⇧tab", "tabs", "shift+tab"), kSidebar, kQuit}
	}
	if s.resultReady || s.review != nil {
		return []key.Binding{bind("←→", "buttons", "left", "right"), bind("↑↓", "scroll", "up", "down"), bind("enter", "select", "enter"), bind("⇧tab", "tabs", "shift+tab"), kQuit}
	}
	list := "accounts"
	if s.account != nil {
		list = "keys"
	}
	if s.focusZone == sshZoneButtons {
		return manageButtonBindings(list, s.btnIdx, s.ctx.HasTabs)
	}
	return []key.Binding{bind("↑↓", list, "up", "down"), bind("enter", "open", "enter"), bind("⇧tab", "buttons", "shift+tab"), kBack, kSidebar, kQuit}
}
