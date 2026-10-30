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

	// Fullscreen views
	if m.subview == svQR || m.subview == svFullURL {
		return []key.Binding{
			bind(theme.KeyEnter, "back", "enter"),
			kQuit,
		}
	}

	// Sidebar focused
	if m.nav.Focused {
		return sidebarBindings()
	}

	// Tab bar focused
	if m.tabFocused {
		viewOnly := false
		onTab := m.activeTab > 0
		tabs := m.effectiveTabs()
		if m.activeTab > 0 && m.activeTab < len(tabs) {
			switch tabs[m.activeTab].Kind {
			case tabPayment, tabOnChainTx,
				tabUtxoDetail:
				viewOnly = true
			}
		}
		return tabBarBindings(viewOnly, onTab)
	}

	// Detail tab content (view-only)
	tabs := m.effectiveTabs()
	if m.activeTab > 0 && m.activeTab < len(tabs) {
		tab := tabs[m.activeTab]

		// L16 new path: delegate to screen component
		if tab.Screen != nil {
			m.screenCtx.HasTabs = hasTabs
			m.screenCtx.ContentFocused = m.contentFocused
			return tab.Screen.HelpBindings()
		}
	}

	// Section home views
	sec := m.nav.ActiveSection()

	// L16: delegate to section home screen if present
	if sec >= 0 && sec < numSections &&
		m.sectionScreens[sec] != nil {
		m.screenCtx.HasTabs = hasTabs
		m.screenCtx.ContentFocused = m.contentFocused
		return m.sectionScreens[sec].HelpBindings()
	}

	return []key.Binding{kQuit}
}
