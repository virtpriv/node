package tui

import (
	"slices"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/virtpriv/node/internal/theme"
)

// UpdatesScreen opens from the System tab's Updates button. It offers Node
// Updates for vpn and the node software, and Update Packages for Debian's
// packages. The System
// screen keeps owning a package update, so closing this tab loses neither a
// running update nor its result, and a second one cannot start meanwhile.
type UpdatesScreen struct {
	ctx     *ScreenContext
	home    *SystemHomeScreen
	button  int
	confirm bool
}

const (
	updatesNodeButton     = "Node Updates"
	updatesPackagesButton = "Update Packages"
)

func openUpdatesCmd(home *SystemHomeScreen) tea.Cmd {
	return func() tea.Msg {
		return openTabMsg{Kind: tabUpdates, Label: "Updates", Screen: &UpdatesScreen{ctx: home.ctx, home: home}}
	}
}

func (s *UpdatesScreen) Init() tea.Cmd                       { return nil }
func (s *UpdatesScreen) HandleMsg(tea.Msg) (Screen, tea.Cmd) { return s, nil }

// buttons names the actions. labels gives what each button shows.
func (s *UpdatesScreen) buttons() []string {
	return []string{updatesNodeButton, updatesPackagesButton}
}

func (s *UpdatesScreen) labels() []string {
	labels := s.buttons()
	if s.home.pkgPending != nil {
		labels[slices.Index(labels, updatesPackagesButton)] = "Updating..."
	}
	return labels
}

func (s *UpdatesScreen) HandleKey(k string, _ tea.KeyPressMsg) (Screen, tea.Cmd) {
	if s.confirm {
		s.confirm = false
		if k != "y" || s.home.pkgPending != nil {
			return s, nil
		}
		attempt := &packageAttempt{}
		s.home.pkgPending, s.home.pkgResult = attempt, ""
		return s, packageUpdateCmd(s.home, attempt)
	}
	buttons := s.buttons()
	s.button = min(max(s.button, 0), len(buttons)-1)
	switch k {
	case "ctrl+c":
		return s, tea.Quit
	case "backspace":
		return s, emitFocusParent
	case "left":
		if s.button > 0 {
			s.button--
			return s, nil
		}
		return s, emitFocusSidebar
	case "right", "tab":
		if s.button+1 < len(buttons) {
			s.button++
		}
	case "up":
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
	case "enter":
		switch buttons[s.button] {
		case updatesNodeButton:
			screen := NewNodeUpdateScreen(s.ctx)
			return s, func() tea.Msg {
				return openTabMsg{Kind: tabNodeUpdates, Label: "Node Updates", Screen: screen, Parent: tabUpdates}
			}
		case updatesPackagesButton:
			s.confirm = s.home.pkgPending == nil
		}
	}
	return s, nil
}

func (s *UpdatesScreen) View(w, _ int) string {
	p := newPane(w)
	p.title(theme.Header, "Updates")
	p.buttons(s.labels(), s.button, s.ctx.ContentFocused && !s.confirm)
	p.blank()
	p.line(" Node Updates: new releases of vpn and the node software.")
	p.line(" Update Packages: Debian's updates, installed now.")
	if s.confirm {
		p.blank()
		p.line(" " + theme.Warning.Render("Update packages? [y/n]"))
	}
	if s.home.pkgResult != "" {
		p.blank()
		p.valueWrap(s.home.pkgResult)
	}
	if status := s.ctx.Status; status != nil && status.Reboot.Fresh() && status.Reboot.Value {
		p.blank()
		p.line(" A reboot is needed. Select Reboot on the System tab.")
	}
	return p.render()
}

func (s *UpdatesScreen) HelpBindings() []key.Binding {
	if s.confirm {
		return confirmDialogBindings()
	}
	bindings := actionButtonBindings(s.button, s.ctx.HasTabs)
	if s.home.pkgPending != nil && s.buttons()[min(s.button, len(s.buttons())-1)] == updatesPackagesButton {
		bindings = slices.DeleteFunc(bindings, func(b key.Binding) bool { return slices.Contains(b.Keys(), "enter") })
	}
	return bindings
}
