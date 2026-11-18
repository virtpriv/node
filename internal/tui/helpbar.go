package tui

import (
	"charm.land/bubbles/v2/key"

	"github.com/virtpriv/node/internal/theme"
)

func (m Model) renderHelpBar(maxW int) string {
	bindings := m.currentBindings()
	return renderBindings(bindings, maxW)
}

func renderBindings(
	bindings []key.Binding, maxW int,
) string {
	var pairs []string
	for _, b := range bindings {
		if b.Enabled() {
			h := b.Help()
			pairs = append(pairs, h.Key, h.Desc)
		}
	}
	return theme.HelpLine(maxW, pairs...)
}

func (m Model) currentBindings() []key.Binding {
	hasTabs := m.hasDetailTabs()

	// Sidebar focused
	if m.nav.Focused {
		return sidebarBindings()
	}

	// Tab bar focused
	if m.tabFocused {
		return tabBarBindings(m.activeTab > 0)
	}

	// Detail tab content
	tabs := m.effectiveTabs()
	if m.activeTab > 0 && m.activeTab < len(tabs) {
		tab := tabs[m.activeTab]

		if tab.Screen != nil {
			m.screenCtx.HasTabs = hasTabs
			m.screenCtx.ContentFocused = m.contentFocused
			return tab.Screen.HelpBindings()
		}
	}

	// Section home views
	sec := m.nav.ActiveSection()

	if sec >= 0 && sec < numSections &&
		m.sectionScreens[sec] != nil {
		m.screenCtx.HasTabs = hasTabs
		m.screenCtx.ContentFocused = m.contentFocused
		return m.sectionScreens[sec].HelpBindings()
	}

	return []key.Binding{kQuit}
}
