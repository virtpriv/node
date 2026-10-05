package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/lipgloss/v2"

	"github.com/virtpriv/node/internal/theme"
)

func (m Model) renderHelpBar(maxW int) string {
	bindings := m.currentBindings()
	return renderBindings(bindings, maxW)
}

func renderBindings(
	bindings []key.Binding, maxW int,
) string {
	if maxW <= 0 {
		return ""
	}

	sep := theme.HelpSep.Render(" │ ")
	sepW := lipgloss.Width(sep)

	type helpPart struct {
		rendered string
		width    int
	}
	var parts []helpPart
	for _, b := range bindings {
		if !b.Enabled() {
			continue
		}
		h := b.Help()
		if h.Key == "" {
			continue
		}
		part := theme.HelpKey.Render(h.Key) +
			" " +
			theme.HelpDesc.Render(h.Desc)
		parts = append(parts, helpPart{
			rendered: part,
			width:    lipgloss.Width(part),
		})
	}

	if len(parts) == 0 {
		return ""
	}

	fitCount := len(parts)
	for fitCount > 0 {
		totalW := 0
		for i := 0; i < fitCount; i++ {
			totalW += parts[i].width
			if i < fitCount-1 {
				totalW += sepW
			}
		}
		if totalW <= maxW {
			break
		}
		fitCount--
	}

	if fitCount == 0 {
		if len(parts) > 0 {
			return parts[0].rendered
		}
		return ""
	}

	var strs []string
	for i := 0; i < fitCount; i++ {
		strs = append(strs, parts[i].rendered)
	}

	return strings.Join(strs, sep)
}

func (m Model) currentBindings() []key.Binding {
	hasTabs := m.hasDetailTabs()

	// Fullscreen views
	if m.subview == svQR || m.subview == svFullURL {
		return []key.Binding{
			bind("enter", "back", "enter"),
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
