package tui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/virtpriv/node/internal/paths"
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

	v := tea.NewView(content)
	v.AltScreen = true
	v.WindowTitle = "Virtual Private Node"
	v.ReportFocus = true
	return v
}

func (m Model) viewMain() string {
	totalW := tuiWidth
	totalH := tuiHeight

	insideW := totalW - 2
	insideH := totalH - 2

	sidebarW := m.nav.Width
	if sidebarW < 10 {
		sidebarW = 10
	}

	contentW := insideW - sidebarW - 1
	if contentW < 30 {
		contentW = 30
		sidebarW = insideW - 1 - contentW
	}

	tabBarRows := 2
	hasTabContent := m.hasDetailTabs()
	bodyH := insideH - tabBarRows

	// ── Sidebar block heights ─────────────────────
	sepRows := numSections - 1
	sideUsable := bodyH - sepRows

	if sideUsable < numSections {
		sideUsable = numSections
	}

	blockBase := sideUsable / numSections
	blockRem := sideUsable % numSections

	var blockHeights [numSections]int
	for i := 0; i < numSections; i++ {
		blockHeights[i] = blockBase
		if i < blockRem {
			blockHeights[i]++
		}
	}

	blocks := m.nav.BlockRows(sidebarW, blockHeights)

	// ── Tab bar (full width) ──────────────────────
	var tabBar string
	if hasTabContent {
		tabBar = m.renderTabBar(insideW)
		tabBarVW := lipgloss.Width(tabBar)
		if tabBarVW < insideW {
			tabBar += strings.Repeat(" ",
				insideW-tabBarVW)
		} else if tabBarVW > insideW {
			tabBar = tabBar[:insideW]
		}
	} else {
		tabBar = strings.Repeat(" ", insideW)
	}

	// ── Content body ──────────────────────────────
	contentBodyH := bodyH

	rawContent := m.renderActiveTabContent(
		contentW, contentBodyH)

	contentLines := strings.Split(rawContent, "\n")
	for len(contentLines) < contentBodyH {
		contentLines = append(contentLines,
			strings.Repeat(" ", contentW))
	}
	if len(contentLines) > contentBodyH {
		contentLines = contentLines[:contentBodyH]
	}

	border := theme.FrameBorder

	// ── Build frame ───────────────────────────────
	var output []string

	output = append(output, border.Render(
		"╭"+strings.Repeat("─", insideW)+"╮"))

	output = append(output,
		border.Render("│")+
			tabBar+
			border.Render("│"))

	output = append(output, border.Render(
		"├"+strings.Repeat("─", sidebarW)+
			"┬"+strings.Repeat("─", contentW)+
			"┤"))

	// Build sidebar rows (blocks + separators)
	var sideRows []string
	var sideSeps []bool
	for si := 0; si < numSections; si++ {
		for _, row := range blocks[si] {
			sideRows = append(sideRows, row)
			sideSeps = append(sideSeps, false)
		}
		if si < numSections-1 {
			sep := strings.Repeat("─", sidebarW)
			sideRows = append(sideRows, sep)
			sideSeps = append(sideSeps, true)
		}
	}

	// Build content rows
	var contentRows []string
	for _, line := range contentLines {
		contentRows = append(contentRows,
			clampLine(line, contentW))
	}

	for len(sideRows) < bodyH {
		sideRows = append(sideRows,
			strings.Repeat(" ", sidebarW))
		sideSeps = append(sideSeps, false)
	}
	for len(contentRows) < bodyH {
		contentRows = append(contentRows,
			strings.Repeat(" ", contentW))
	}

	for r := 0; r < bodyH; r++ {
		isSideSep := r < len(sideSeps) && sideSeps[r]

		leftEdge := "│"
		middle := "│"
		rightEdge := "│"

		if isSideSep {
			leftEdge = "├"
			middle = "┤"
		}

		sideCell := sideRows[r]
		contentCell := contentRows[r]

		if isSideSep {
			output = append(output,
				border.Render(leftEdge)+
					border.Render(sideCell)+
					border.Render(middle)+
					contentCell+
					border.Render(rightEdge))
		} else {
			output = append(output,
				border.Render(leftEdge)+
					sideCell+
					border.Render(middle)+
					contentCell+
					border.Render(rightEdge))
		}
	}

	output = append(output, border.Render(
		"╰"+strings.Repeat("─", sidebarW)+
			"┴"+strings.Repeat("─", contentW)+"╯"))

	frame := strings.Join(output, "\n")

	helpStr := m.renderHelpBar(totalW)
	helpLine := centerInWidth(helpStr, totalW)

	fullContent := frame + "\n" + helpLine
	if banner := m.renderNetworkBanner(); banner != "" {
		fullContent = centerInWidth(banner, totalW) + "\n" + fullContent
	}
	// First-run verification banner (ruling xvi): shown until
	// the journal proves a real sshd login for the admin user.
	if !m.state.KeyVerificationKnown ||
		m.state.KeyVerificationPending {
		fullContent = centerInWidth(
			m.renderVerifyBanner(), totalW) +
			"\n" + fullContent
	}

	return lipgloss.Place(
		m.width, m.height,
		lipgloss.Center, lipgloss.Center,
		fullContent,
	)
}

func (m Model) renderNetworkBanner() string {
	profile, err := m.cfg.NetworkConfig()
	if err != nil {
		return theme.Warning.Render("⚠ Unsupported network profile")
	}
	if !profile.TestingOnly {
		return ""
	}
	return theme.Warning.Render("⚠ TESTING NETWORK — "+profile.DisplayName) +
		theme.Dim.Render(" — coins have no mainnet value")
}

// renderVerifyBanner is the first-run access-verification
// banner line. Copy is deliberately imperative and small: it
// nags, it never blocks.
func (m Model) renderVerifyBanner() string {
	if !m.state.KeyVerificationKnown {
		return theme.Warning.Render("⚠ Key verification status unavailable") +
			theme.Dim.Render(" — check: journalctl -u vpn-helperd")
	}
	target := "ssh " + paths.AdminUser + "@<server-ip>"
	if ip := m.state.KeyVerificationAddress; ip != "" {
		target = "ssh " + paths.AdminUser + "@" + ip
	}
	return theme.Warning.Render("⚠ Key verification pending") +
		theme.Dim.Render(
			" — connect from a second terminal: "+target)
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

func (m Model) renderTabBar(maxW int) string {
	tabs := m.effectiveTabs()

	if len(tabs) <= 1 {
		return ""
	}

	type tabRender struct {
		str   string
		width int
	}
	var allTabs []tabRender

	for i := 1; i < len(tabs); i++ {
		tab := tabs[i]
		isCursor := m.tabFocused && m.activeTab == i

		label := tab.Label
		if len(label) > 14 {
			label = label[:12] + ".."
		}

		var s string
		if isCursor && m.tabCursorX == 1 {
			s = theme.NavItem.Render(" "+label+" ") +
				theme.NavActive.Render("✕") + " "
		} else if isCursor && m.tabCursorX == 0 {
			s = theme.NavActive.Render(" "+label+" ") +
				"✕ "
		} else {
			s = lipgloss.NewStyle().
				Foreground(theme.ColorBorder).
				Render(" " + label + " ✕ ")
		}

		allTabs = append(allTabs, tabRender{
			str: s, width: lipgloss.Width(s),
		})
	}

	arrowW := 2
	availW := maxW

	offset := m.tabScrollOffset
	if offset < 0 {
		offset = 0
	}
	if offset >= len(allTabs) {
		offset = len(allTabs) - 1
	}
	if offset < 0 {
		offset = 0
	}

	activeIdx := m.activeTab - 1
	if activeIdx < 0 {
		activeIdx = 0
	}
	if activeIdx < offset {
		offset = activeIdx
	}

	needLeftArrow := offset > 0
	usedW := 0
	if needLeftArrow {
		usedW = arrowW
	}

	endIdx := offset
	for endIdx < len(allTabs) {
		w := allTabs[endIdx].width
		rightArrowW := 0
		if endIdx+1 < len(allTabs) {
			rightArrowW = arrowW
		}
		if usedW+w+rightArrowW > availW {
			break
		}
		usedW += w
		endIdx++
	}

	if activeIdx >= endIdx {
		endIdx = activeIdx + 1
		usedW = 0
		offset = endIdx - 1
		for offset > 0 {
			w := allTabs[offset-1].width
			if usedW+w+allTabs[offset].width >
				availW-arrowW*2 {
				break
			}
			usedW += allTabs[offset].width
			offset--
		}
		needLeftArrow = offset > 0
		usedW = 0
		if needLeftArrow {
			usedW = arrowW
		}
		endIdx = offset
		for endIdx < len(allTabs) {
			w := allTabs[endIdx].width
			rightArrowW := 0
			if endIdx+1 < len(allTabs) {
				rightArrowW = arrowW
			}
			if usedW+w+rightArrowW > availW {
				break
			}
			usedW += w
			endIdx++
		}
	}

	needRightArrow := endIdx < len(allTabs)

	var parts []string
	if needLeftArrow {
		parts = append(parts,
			lipgloss.NewStyle().
				Foreground(theme.ColorBorder).
				Render("◀ "))
	}

	for i := offset; i < endIdx; i++ {
		parts = append(parts, allTabs[i].str)
	}

	if needRightArrow {
		parts = append(parts,
			lipgloss.NewStyle().
				Foreground(theme.ColorBorder).
				Render(" ▶"))
	}

	return strings.Join(parts, "")
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
	// width fits. This is ANSI-safe — lipgloss.Width
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
