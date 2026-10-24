package tui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/virtpriv/node/internal/theme"
)

func (m Model) View() tea.View {
	var content string

	if m.width == 0 {
		content = "Loading..."
	} else {
		switch m.subview {
		case svQR:
			content = m.viewQR()
		case svFullURL:
			content = m.viewFullURL()
		default:
			content = m.viewMain()
		}
	}

	if m.width > 0 {
		content = lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content)
	}
	v := tea.NewView(content)
	v.BackgroundColor, v.ForegroundColor = theme.ColorBackground, theme.ColorText
	v.AltScreen = true
	v.WindowTitle = "Virtual Private Node"
	v.ReportFocus = true
	return v
}

// viewMain draws the frame: the top bar with the open tabs, the sidebar
// beside the active screen, the theme toggle under the sidebar, and the help
// line below. View centers it in the window.
func (m Model) viewMain() string {
	insideW := tuiWidth - 2
	sidebarW := theme.SidebarWidth
	contentW := insideW - sidebarW - 1
	border := theme.FrameBorder

	marks, names := "", ""
	if m.hasDetailTabs() {
		marks, names = m.renderTabBar(insideW)
	}

	contentLines := strings.Split(m.renderActiveTabContent(contentW, theme.ContentRows), "\n")

	// The sidebar: each section's box, closed by a line.
	var sideRows []string
	var sideLines []bool
	for _, block := range m.nav.BlockRows(sidebarW) {
		for _, row := range block {
			sideRows = append(sideRows, row)
			sideLines = append(sideLines, false)
		}
		sideRows = append(sideRows, strings.Repeat("─", sidebarW))
		sideLines = append(sideLines, true)
	}

	output := []string{
		border.Render("╭" + strings.Repeat("─", insideW) + "╮"),
		border.Render("│") + clampLine(marks, insideW) + border.Render("│"),
		border.Render("│") + clampLine(names, insideW) + border.Render("│"),
		border.Render("├" + strings.Repeat("─", sidebarW) + "┬" + strings.Repeat("─", contentW) + "┤"),
	}
	for r := range theme.ContentRows {
		content := ""
		if r < len(contentLines) {
			content = contentLines[r]
		}
		left, side, middle := border.Render("│"), sideRows[r], border.Render("│")
		if sideLines[r] {
			left, side, middle = border.Render("├"), border.Render(side), border.Render("┤")
		}
		output = append(output, left+side+middle+clampLine(content, contentW)+border.Render("│"))
	}
	output = append(output,
		border.Render("│")+m.nav.ThemeIconCell(sidebarW)+border.Render("│")+strings.Repeat(" ", contentW)+border.Render("│"),
		border.Render("╰"+strings.Repeat("─", sidebarW)+"┴"+strings.Repeat("─", contentW)+"╯"),
		centerInWidth(m.renderHelpBar(tuiWidth), tuiWidth))
	if banner := m.renderNetworkBanner(); banner != "" {
		output = append([]string{centerInWidth(banner, tuiWidth)}, output...)
	}
	return strings.Join(output, "\n")
}

func (m Model) renderNetworkBanner() string {
	profile, err := m.cfg.NetworkConfig()
	if err != nil {
		return theme.Warning.Render("⚠ Unsupported network profile")
	}
	if !profile.TestingOnly {
		return ""
	}
	return theme.Warning.Render("⚠ TESTING NETWORK, "+profile.DisplayName+".") +
		theme.Dim.Render(" Coins have no mainnet value.")
}

func (m Model) hasDetailTabs() bool {
	sec := m.nav.ActiveSection()
	for _, t := range m.tabs {
		if t.Section == sec {
			return true
		}
	}
	return false
}

// renderTabBar draws the open tabs' names on the lower row of the top bar
// and, on the upper row, a ✕ above the current tab. Left and right move
// between names, up reaches the ✕. Arrows show when tabs are off screen.
func (m Model) renderTabBar(maxW int) (marks, names string) {
	tabs := m.effectiveTabs()
	if len(tabs) <= 1 {
		return "", ""
	}
	dim := lipgloss.NewStyle().Foreground(theme.ColorBorder)

	type tabRender struct {
		str   string
		width int
	}
	var allTabs []tabRender
	for i := 1; i < len(tabs); i++ {
		label := tabs[i].Label
		if len(label) > 14 {
			label = label[:12] + ".."
		}
		style := dim
		if m.tabFocused && m.activeTab == i && m.tabCursorX == 0 {
			style = theme.NavActive
		}
		s := style.Render(" "+label+" ") + " "
		allTabs = append(allTabs, tabRender{str: s, width: lipgloss.Width(s)})
	}

	arrowW := 2
	availW := maxW

	offset := min(max(m.tabScrollOffset, 0), len(allTabs)-1)
	activeIdx := max(m.activeTab-1, 0)
	if activeIdx < offset {
		offset = activeIdx
	}

	// fit returns the end of the tabs that fit from offset.
	fit := func(offset int) int {
		used := 0
		if offset > 0 {
			used = arrowW
		}
		end := offset
		for end < len(allTabs) {
			right := 0
			if end+1 < len(allTabs) {
				right = arrowW
			}
			if used+allTabs[end].width+right > availW {
				break
			}
			used += allTabs[end].width
			end++
		}
		return end
	}
	endIdx := fit(offset)
	if activeIdx >= endIdx {
		endIdx = activeIdx + 1
		used := 0
		offset = endIdx - 1
		for offset > 0 {
			if used+allTabs[offset-1].width+allTabs[offset].width > availW-arrowW*2 {
				break
			}
			used += allTabs[offset].width
			offset--
		}
		endIdx = fit(offset)
	}

	var top, bottom strings.Builder
	col := 0
	if offset > 0 {
		bottom.WriteString(dim.Render("◀ "))
		top.WriteString(strings.Repeat(" ", arrowW))
		col = arrowW
	}
	for i := offset; i < endIdx; i++ {
		bottom.WriteString(allTabs[i].str)
		if i == activeIdx && m.activeTab > 0 {
			// The ✕ sits above the middle of the name, which is the tab's
			// width less the space after it.
			labelW := allTabs[i].width - 1
			at := col + (labelW-1)/2
			xStyle := dim
			if m.tabFocused && m.tabCursorX == 1 {
				xStyle = theme.NavActive
			}
			top.WriteString(strings.Repeat(" ", at-lipgloss.Width(top.String())) + xStyle.Render("✕"))
		}
		col += allTabs[i].width
	}
	if endIdx < len(allTabs) {
		bottom.WriteString(dim.Render(" ▶"))
	}
	return top.String(), bottom.String()
}

func (m Model) effectiveTabs() []openTab {
	sec := m.nav.ActiveSection()
	mainLabel := "Channels"
	switch sec {
	case secWallet:
		mainLabel = "Wallet"
	case secOnChain:
		mainLabel = "On-Chain"
	case secAddons:
		mainLabel = "Add-ons"
	case secSystem:
		mainLabel = "System"
	}

	tabs := []openTab{
		{Kind: tabMain, Label: mainLabel, Section: sec},
	}
	for _, t := range m.tabs {
		if t.Section == sec {
			if t.Kind == tabUtxoDetail || t.Kind == tabOnChainTx {
				t.Label = onChainDetailLabel(t.Screen)
			}
			if detail, ok := t.Screen.(*ChannelDetailScreen); ok {
				t.Label = detail.label()
			}
			if detail, ok := t.Screen.(*PaymentDetailScreen); ok {
				t.Label = detail.label()
			}
			tabs = append(tabs, t)
		}
	}
	return tabs
}

func (m Model) renderActiveTabContent(w, h int) string {
	tabs := m.effectiveTabs()
	idx := m.activeTab
	if idx < 0 || idx >= len(tabs) {
		idx = 0
	}
	screen := tabs[idx].Screen
	sec := m.nav.ActiveSection()
	if screen == nil && sec >= 0 && sec < numSections {
		screen = m.sectionScreens[sec]
	}
	if screen == nil {
		return ""
	}
	m.screenCtx.HasTabs = m.hasDetailTabs()
	m.screenCtx.ContentFocused = m.contentFocused
	if (sec == secChannels || sec == secWallet || sec == secOnChain) &&
		!m.screenCtx.walletKnown() && (idx > 0 || m.screenCtx.walletDisplayAvailable()) {
		notice := ansi.Wrap("Wallet state unavailable. Wallet actions are temporarily disabled.", w, "")
		return theme.Warn.Render(notice) + "\n" + screen.View(w, max(1, h-lipgloss.Height(notice)))
	}
	return screen.View(w, h)
}

// ── Helpers ──────────────────────────────────────────────

func clampLine(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s + strings.Repeat(" ",
			w-lipgloss.Width(s))
	}
	// Truncation: trim runes from end until visual
	// width fits. This is ANSI-safe, lipgloss.Width
	// ignores escape sequences.
	r := []rune(s)
	for lipgloss.Width(string(r)) > w-1 && len(r) > 0 {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}

func centerInWidth(s string, w int) string {
	vis := lipgloss.Width(s)
	if vis >= w {
		return s
	}
	left := (w - vis) / 2
	right := w - vis - left
	return strings.Repeat(" ", left) + s +
		strings.Repeat(" ", right)
}

func parseBalance(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}
