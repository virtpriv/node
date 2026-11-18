package tui

import (
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/virtpriv/node/internal/theme"
	"github.com/virtpriv/node/internal/tui/steplist"
)

// ── Focus helpers ────────────────────────────────────────

func (m *Model) focusSidebar() {
	m.nav.Focus()
	m.tabFocused = false
	m.contentFocused = false
}

func (m *Model) focusTabBar() {
	m.nav.Blur()
	m.tabFocused = true
	m.contentFocused = false
}

func (m *Model) focusContent() {
	m.nav.Blur()
	m.tabFocused = false
	m.contentFocused = true
}

// rememberTabPosition saves the current activeTab into
// sectionFocus for the current section, so the next
// "up from sidebar" or focusTabBarMsg-from-home-screen
// restores it. Guards against saving 0, which would
// clobber existing memory with a value the restore
// logic treats as "no memory."
//
// Call this from any code path that *intentionally*
// moves the cursor onto a detail tab and wants that
// position remembered: section-exit, tab-bar→sidebar
// boundary, openTabMsg, closeTab.
func (m *Model) rememberTabPosition() {
	if m.activeTab <= 0 {
		return
	}
	sec := m.nav.ActiveSection()
	if sec < 0 || sec >= numSections {
		return
	}
	m.sectionFocus[sec] = m.activeTab
}

// activateTab delivers a tabActivatedMsg to the active
// tab's screen, giving it a chance to refresh stale data.
// Returns the screen's cmd (typically a fetch) or nil if
// no screen is mounted or the active tab is the section
// home. Only called when the user "commits" to viewing a
// detail tab, not during tab bar browsing or sidebar
// navigation.
func (m *Model) activateTab() tea.Cmd {
	tabs := m.effectiveTabs()
	if m.activeTab <= 0 ||
		m.activeTab >= len(tabs) {
		return nil
	}
	tab := tabs[m.activeTab]
	if tab.Screen == nil {
		return nil
	}
	m.screenCtx.HasTabs = m.hasDetailTabs()
	m.screenCtx.ContentFocused = m.contentFocused
	newScreen, cmd :=
		tab.Screen.HandleMsg(tabActivatedMsg{})
	m.setTabScreen(m.activeTab, newScreen)
	return cmd
}

// stepSpinner is a screen with a step list. Its spinner runs only while a
// step runs and the screen shows, so a hidden screen causes no redraws.
type stepSpinner interface {
	spinner() *steplist.Spinner
	stepRuns() bool
}

// shownStepSpinner is the step list screen on screen, if any.
func (m Model) shownStepSpinner() stepSpinner {
	if len(m.tabs) == 0 {
		return nil
	}
	tabs := m.effectiveTabs()
	if m.activeTab <= 0 || m.activeTab >= len(tabs) {
		return nil
	}
	owner, _ := tabs[m.activeTab].Screen.(stepSpinner)
	return owner
}

// Update hands a step list's spinner tick to its screen while the screen
// shows. A tick for a hidden screen stops its spinner. After every other
// message the screen on screen gets its spinner started or stopped, which
// covers every way a tab comes back into view.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if tick, ok := msg.(spinner.TickMsg); ok {
		for _, tab := range m.tabs {
			owner, ok := tab.Screen.(stepSpinner)
			if !ok || !owner.spinner().Owns(tick) {
				continue
			}
			if owner == m.shownStepSpinner() && owner.stepRuns() {
				return m, owner.spinner().Update(tick)
			}
			owner.spinner().Set(false)
			return m, nil
		}
		return m, nil
	}
	next, cmd := m.update(msg)
	updated := next.(Model)
	if owner := updated.shownStepSpinner(); owner != nil {
		cmd = tea.Batch(cmd, owner.spinner().Set(owner.stepRuns()))
	}
	return updated, cmd
}

func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case relaunchMsg:
		m.relaunch = true
		return m, tea.Quit
	case nodeUpdateMsg:
		return m.routeNodeUpdate(msg.owner, msg)
	case nodeUpdateTick:
		return m.routeNodeUpdate(msg.owner, msg)
	case settingsReviewWaitingMsg:
		for _, tab := range m.tabs {
			if tab.Screen == msg.owner {
				_, cmd := msg.owner.HandleMsg(msg)
				return m, cmd
			}
		}
		return m, nil
	case settingsPrepareMsg:
		for _, tab := range m.tabs {
			if tab.Screen == msg.owner {
				_, cmd := msg.owner.HandleMsg(msg)
				return m, cmd
			}
		}
		return m, nil
	case settingsReviewMsg:
		// A closed form drops the node's answer. The Prepare job keeps its
		// review, which Node Updates shows when it opens.
		for _, tab := range m.tabs {
			if tab.Screen == msg.owner {
				_, cmd := msg.owner.HandleMsg(msg)
				return m, cmd
			}
		}
		return m, nil
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tea.ResumeMsg:
		return m, requestStatusCmd
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	case tea.PasteMsg:
		// Route paste to active tab's screen.
		tabs := m.effectiveTabs()
		if m.activeTab > 0 &&
			m.activeTab < len(tabs) &&
			tabs[m.activeTab].Screen != nil {
			s := tabs[m.activeTab].Screen
			m.screenCtx.HasTabs = m.hasDetailTabs()
			m.screenCtx.ContentFocused = true
			newScreen, cmd := s.HandleMsg(msg)
			m.setTabScreen(m.activeTab, newScreen)
			return m, cmd
		}
		// Route paste to section home screen.
		sec := m.nav.ActiveSection()
		if sec >= 0 && sec < numSections &&
			m.sectionScreens[sec] != nil {
			m.screenCtx.HasTabs = m.hasDetailTabs()
			m.screenCtx.ContentFocused = true
			newScreen, cmd :=
				m.sectionScreens[sec].HandleMsg(msg)
			m.sectionScreens[sec] = newScreen
			return m, cmd
		}
		return m, nil

	// ── Screen-to-Model messages ────────────────────
	case closeSSHScreenMsg:
		if msg.screen == nil || sshAccessBusy(msg.screen) {
			return m, nil
		}
		return m.closeScreenTab(msg.screen)
	case closeTabMsg:
		return m.closeTab(m.activeTab)
	case focusSidebarMsg:
		m.focusSidebar()
		return m, nil
	case focusTabBarMsg:
		m.focusTabBar()
		m.tabCursorX = 0
		// Two cases:
		//  1. Detail screen emitted this. m.activeTab
		//     is already > 0, preserve it (the user
		//     should land on the tab they came from).
		//  2. Home screen emitted this. m.activeTab
		//     is 0, restore from sectionFocus or fall
		//     back to tab 1. Without this, the tab
		//     bar focuses on the home tab and shows
		//     no visible detail-tab cursor.
		if m.activeTab == 0 {
			sec := m.nav.ActiveSection()
			tabs := m.effectiveTabs()
			remembered := m.sectionFocus[sec]
			if remembered >= 1 &&
				remembered < len(tabs) {
				m.activeTab = remembered
			} else if len(tabs) > 1 {
				m.activeTab = 1
			}
		}
		return m, nil
	case focusParentMsg:
		// Navigate to the active tab's parent. If
		// the parent is 0 (section home), focus the
		// section home. Otherwise find the open tab
		// whose kind matches the parent and focus it.
		tabs := m.effectiveTabs()
		if m.activeTab > 0 &&
			m.activeTab < len(tabs) {
			parent := tabs[m.activeTab].Parent
			if parent != 0 {
				for i, t := range tabs {
					if t.Kind == parent {
						m.activeTab = i
						m.focusContent()
						return m, m.activateTab()
					}
				}
			}
		}
		// If the parent is section home or not found,
		// fall back to section home.
		m.activeTab = 0
		m.focusContent()
		return m, nil
	case refreshStatusMsg:
		cmd := m.admitStatus()
		return m, cmd
	case openTabMsg:
		if msg.Kind == tabSSHKeyDetail {
			detail, ok := msg.Screen.(*SSHKeyDetailScreen)
			if !ok || msg.Key == "" || detail.keyInfo.Fingerprint != msg.Key || m.nav.ActiveSection() != secSystem {
				return m, nil
			}
			for i, tab := range m.effectiveTabs() {
				if tab.Kind == tabSSHKeyDetail && tab.Key == msg.Key {
					m.activeTab = i
					m.rememberTabPosition()
					m.focusContent()
					return m, m.activateTab()
				}
			}
		}

		// A flow tab is open once in a section.
		if msg.Kind != tabSSHKeyDetail {
			sec := m.nav.ActiveSection()
			tabs := m.effectiveTabs()
			for i, t := range tabs {
				if t.Kind == msg.Kind &&
					t.Section == sec {
					m.activeTab = i
					m.rememberTabPosition()
					if msg.Replace && (nodeUpdatesBusy(t.Screen) || m.sshTabBusy(t)) {
						return m, nil
					}
					if msg.Replace &&
						msg.Screen != nil {
						m.setTabScreen(i, msg.Screen)
					}
					m.focusContent()
					if msg.Replace &&
						msg.Screen != nil {
						return m, msg.Screen.Init()
					}
					return m, m.activateTab()
				}
			}
		}
		m.tabs = append(m.tabs, openTab{
			Kind:    msg.Kind,
			Label:   msg.Label,
			Key:     msg.Key,
			Section: m.nav.ActiveSection(),
			Parent:  msg.Parent,
			Screen:  msg.Screen,
		})
		m.activeTab = len(m.effectiveTabs()) - 1
		m.rememberTabPosition()
		m.focusContent()
		if msg.Screen != nil {
			return m, msg.Screen.Init()
		}
		return m, nil

	case serviceActionRequestMsg:
		return m, m.startServiceAction(msg)
	case serviceActionResultMsg:
		return m, m.completeServiceAction(msg)
	case systemRefreshMsg:
		return m, requestStatusCmd
	case rebootRequestMsg:
		return m, m.startReboot(msg)
	case rebootResultMsg:
		return m, m.completeReboot(msg)
	case packageUpdateRequestMsg:
		return m, m.startPackageUpdate(msg)
	case packageUpdateResultMsg:
		return m, m.completePackageUpdate(msg)
	case statusResultMsg:
		cmd := m.completeStatus(msg)
		return m, cmd
	case releasesFolderMsg:
		m.screenCtx.applyReleasesFolder(msg)
		return m, nil
	case latestVersionMsg:
		m.screenCtx.LatestVersion = string(msg)
		m.screenCtx.LatestChecked = true
		if msg == "" {
			return m, retryLatestVersionCmd()
		}
		return m, nil
	case restoredJobsMsg:
		return m, m.restoreSystemJobs(msg)
	case refreshSSHKeysMsg:
		var cmds []tea.Cmd
		for _, tab := range m.tabs {
			switch screen := tab.Screen.(type) {
			case *SSHKeysScreen:
				cmds = append(cmds, screen.refresh())
			case *AccountsScreen:
				cmds = append(cmds, screen.refresh())
			}
		}
		return m, tea.Batch(cmds...)
	case accountsMsg:
		return m.routeSSHResult(msg.owner, msg)
	case accountDetailMsg:
		return m.routeSSHResult(msg.owner, msg)
	case accountImportMsg:
		return m.routeSSHResult(msg.owner, msg)
	case sshKeysListMsg:
		return m.routeSSHResult(msg.owner, msg)
	case sshKeyAddMsg:
		return m.routeSSHResult(msg.owner, msg)
	case sshKeyRemoveMsg:
		return m.routeSSHResult(msg.owner, msg)
	case sshPwAuthDoneMsg:
		return m.routeSSHResult(msg.owner, msg)
	case refreshSSHAuthMsg:
		for _, tab := range m.tabs {
			if screen, ok := tab.Screen.(*SSHPasswordAuthScreen); ok && sshAccessBusy(screen) {
				return m, nil
			}
		}
		m.screenCtx.sshAuthRevision++
		revision, ctx, access := m.screenCtx.sshAuthRevision, m.screenCtx, m.screenCtx.sshAccess()
		return m, func() tea.Msg {
			enabled, err := access.PasswordAuth()
			return sshPwAuthStateMsg{owner: ctx, revision: revision, disabled: !enabled, err: err}
		}
	case sshPwAuthStateMsg:
		if msg.owner != m.screenCtx || msg.revision != m.screenCtx.sshAuthRevision {
			return m, nil
		}
		m.state.SSHPasswordAuthKnown = msg.err == nil
		if msg.err == nil {
			m.state.SSHPasswordAuthDisabled = msg.disabled
		}
		return m, nil
	case changePwDoneMsg:
		return m.routeSSHResult(msg.owner, msg)
	case closeLoginPasswordMsg:
		if msg.owner == nil || msg.owner.attempt != msg.attempt || sshAccessBusy(msg.owner) {
			return m, nil
		}
		return m.closeScreenTab(msg.owner)
	case tickMsg:
		return m, tea.Batch(requestStatusCmd, readReleasesFolderCmd, tickEveryCmd(m.pollInterval()))
	}
	return m, nil
}

func (m Model) routeNodeUpdate(owner *NodeUpdateScreen, msg tea.Msg) (tea.Model, tea.Cmd) {
	for _, tab := range m.tabs {
		if tab.Screen == owner {
			_, cmd := owner.HandleMsg(msg)
			return m, cmd
		}
	}
	// A closed or replaced screen cannot restart polling or alter a new review.
	return m, nil
}

// ── Key dispatch (tab-first) ─────────────────────────────

func (m Model) handleKey(
	msg tea.KeyPressMsg,
) (tea.Model, tea.Cmd) {
	key := msg.String()

	if key == "ctrl+z" {
		if !canSuspend() {
			return m, nil
		}
		return m, tea.Suspend
	}

	// 1. Tab bar focused
	if m.tabFocused {
		return m.handleTabBarKey(key)
	}

	// 2. Sidebar focused
	if m.nav.Focused {
		return m.handleSidebarKey(key)
	}

	// 3. Content focused, dispatch by active tab
	tabs := m.effectiveTabs()
	if m.activeTab > 0 && m.activeTab < len(tabs) {
		tab := tabs[m.activeTab]
		if tab.Screen != nil {
			m.screenCtx.HasTabs = m.hasDetailTabs()
			m.screenCtx.ContentFocused = true
			newScreen, cmd :=
				tab.Screen.HandleKey(key, msg)
			m.setTabScreen(m.activeTab, newScreen)
			return m, cmd
		}
		return m, nil
	}

	// 4. Section home, all sections are screen-backed
	sec := m.nav.ActiveSection()
	if sec >= 0 && sec < numSections &&
		m.sectionScreens[sec] != nil {
		m.screenCtx.HasTabs = m.hasDetailTabs()
		m.screenCtx.ContentFocused = true
		newScreen, cmd :=
			m.sectionScreens[sec].HandleKey(key, msg)
		m.sectionScreens[sec] = newScreen
		return m, cmd
	}

	return m, nil
}

// ── Sidebar keys ─────────────────────────────────────────

func (m Model) handleSidebarKey(
	key string,
) (tea.Model, tea.Cmd) {
	switch key {
	case "ctrl+c":
		return m, tea.Quit
	case "up":
		if m.nav.Cursor == 0 && m.hasDetailTabs() {
			m.focusTabBar()
			m.tabCursorX = 0
			// Restore last tab position for this
			// section, falling back to tab 1 if no
			// memory yet or if the saved index is
			// out of range.
			sec := m.nav.ActiveSection()
			tabs := m.effectiveTabs()
			remembered := m.sectionFocus[sec]
			if remembered >= 1 &&
				remembered < len(tabs) {
				m.activeTab = remembered
			} else if m.activeTab < 1 {
				m.activeTab = 1
			}
			return m, nil
		}
		m.nav.MoveUp()
		return m, nil
	case "down", "tab":
		m.nav.MoveDown()
		return m, nil
	case "enter", "right":
		// The theme toggle only responds to Enter.
		// Right arrow is ignored on the toggle.
		if m.nav.IsOnThemeToggle() {
			if key == "enter" {
				mode := theme.Toggle()
				m.prefs.Theme = mode
				m.savePreferences()
				m.nav.UpdateThemeLabel()
				for _, screen := range m.sectionScreens {
					restyleScreenFields(screen)
				}
				for _, tab := range m.tabs {
					restyleScreenFields(tab.Screen)
				}
			}
			return m, nil
		}
		// Save the current section's tab position
		// before Activate switches us away. After
		// Activate, m.nav.ActiveSection() will return
		// the new section, so we have to capture the
		// old section's index here.
		m.rememberTabPosition()
		m.nav.Activate()
		m.focusContent()
		m.activeTab = 0
		m.tabFocused = false
		m.tabCursorX = 0
		return m, nil
	}
	return m, nil
}

// ── Tab bar keys ─────────────────────────────────────────

func (m Model) handleTabBarKey(
	key string,
) (tea.Model, tea.Cmd) {
	tabs := m.effectiveTabs()

	switch key {
	case "ctrl+c":
		return m, tea.Quit

	case "up":
		// Up reaches the ✕ above a tab that can be closed.
		if m.activeTab > 0 {
			m.tabCursorX = 1
		}
		return m, nil

	case "down", "tab":
		if key == "down" && m.tabCursorX == 1 {
			m.tabCursorX = 0
			return m, nil
		}
		m.tabCursorX = 0
		m.focusContent()
		return m, m.activateTab()

	case "left":
		if m.tabCursorX == 1 {
			m.tabCursorX = 0
			return m, nil
		}
		if m.activeTab > 1 {
			m.activeTab--
			m.tabCursorX = 0
			if m.activeTab-1 < m.tabScrollOffset {
				m.tabScrollOffset = m.activeTab - 1
				if m.tabScrollOffset < 0 {
					m.tabScrollOffset = 0
				}
			}
			return m, nil
		}
		m.focusSidebar()
		// Save current position before resetting so
		// the next "up from sidebar" restores it.
		m.rememberTabPosition()
		m.activeTab = 0
		m.tabScrollOffset = 0
		return m, nil

	case "right":
		if m.tabCursorX == 1 {
			m.tabCursorX = 0
			return m, nil
		}
		if m.activeTab < len(tabs)-1 {
			m.activeTab++
			m.tabCursorX = 0
			return m, nil
		}
		return m, nil

	case "enter":
		if m.tabCursorX == 1 && m.activeTab > 0 {
			return m.closeTab(m.activeTab)
		}
		// Enter on a tab label focuses its content.
		m.focusContent()
		return m, m.activateTab()

	case "backspace":
		if m.activeTab > 0 {
			return m.closeTab(m.activeTab)
		}
		m.focusSidebar()
		m.activeTab = 0
		return m, nil
	}
	return m, nil
}

// Done belongs to its originating screen even if navigation changes before delivery.
func (m Model) closeScreenTab(screen Screen) (tea.Model, tea.Cmd) {
	for i, tab := range m.tabs {
		if tab.Screen != screen {
			continue
		}
		if tab.Section == m.nav.ActiveSection() {
			for index, visible := range m.effectiveTabs() {
				if visible.Screen == screen {
					return m.closeTab(index)
				}
			}
		} else {
			m.tabs = append(m.tabs[:i], m.tabs[i+1:]...)
			m.sectionFocus[tab.Section] = 0
			return m, nil
		}
	}
	return m, nil
}

func (m Model) closeTab(
	tabIdx int,
) (tea.Model, tea.Cmd) {
	tabs := m.effectiveTabs()
	if tabIdx <= 0 || tabIdx >= len(tabs) {
		return m, nil
	}

	closingTab := tabs[tabIdx]
	// Keep submitted operations reachable until their bounded calls return.
	if m.sshTabBusy(closingTab) {
		return m, nil
	}

	// Closing a parent also removes its child tabs in the same section.
	// Future asynchronous results will no longer reach the removed screens.
	descendants := m.tabDescendantKinds(closingTab)
	shouldRemove := func(t openTab) bool {
		if t.Section != closingTab.Section {
			return false
		}
		if t.Kind == closingTab.Kind && t.Key == closingTab.Key {
			return true
		}
		if descendants[t.Parent] {
			return true
		}
		return false
	}

	var newTabs []openTab
	for _, t := range m.tabs {
		if shouldRemove(t) {
			continue
		}
		newTabs = append(newTabs, t)
	}
	m.tabs = newTabs

	m.tabCursorX = 0

	// Close-to-neighbor: land on whatever tab now
	// occupies the closed parent's index. If that
	// index is past the new end, clamp to the last
	// tab. If no detail tabs remain, fall back to
	// the section home (index 0).
	//
	// Closing an SSH key's tab with no siblings lands
	// on the key list, which is the neighbor at that
	// index. Closing it with siblings lands on the next
	// sibling, which is what a Sparrow or browser user
	// expects.
	newTabCount := len(m.effectiveTabs())
	if newTabCount > 1 {
		landing := tabIdx
		if landing >= newTabCount {
			landing = newTabCount - 1
		}
		m.activeTab = landing
		m.focusTabBar()
	} else {
		// No detail tabs left, fall back to section
		// home. Focus content since there's no tab bar.
		m.activeTab = 0
		m.focusContent()
	}

	// Save the resolved landing index into
	// sectionFocus so the next "up from sidebar"
	// lands here too. This intentionally bypasses
	// rememberTabPosition because we want to *clear*
	// memory when no detail tabs remain (write 0),
	// not preserve stale memory the way the helper's
	// guard does.
	sec := closingTab.Section
	if sec >= 0 && sec < numSections {
		m.sectionFocus[sec] = m.activeTab
	}

	// Scroll correctness: keep tabScrollOffset in
	// agreement with where m.activeTab now points.
	// The renderTabBar pass compensates for stale
	// offsets on the fly, so skipping this would not
	// be user-visible today, but the model field
	// would drift from rendered state, which will
	// burn any future tab-system test that asserts
	// on tabScrollOffset directly. Mirror the two
	// invariants the "left" key handler maintains:
	//
	//   1. If no detail tabs remain, reset to 0.
	//   2. Otherwise, if the active tab is now to
	//      the left of the current offset, pull the
	//      offset backward to make it visible.
	//
	// The original upper-bound clamp (don't let the
	// offset point past the new end) is also kept.
	if m.activeTab == 0 {
		m.tabScrollOffset = 0
	} else if m.activeTab-1 < m.tabScrollOffset {
		m.tabScrollOffset = m.activeTab - 1
		if m.tabScrollOffset < 0 {
			m.tabScrollOffset = 0
		}
	}
	if m.tabScrollOffset >
		len(m.effectiveTabs())-2 {
		m.tabScrollOffset =
			len(m.effectiveTabs()) - 2
		if m.tabScrollOffset < 0 {
			m.tabScrollOffset = 0
		}
	}

	return m, nil
}

// ── Screen dispatch helpers ──────────────────────────────

// setTabScreen updates the Screen on the active tab.
// Works on the underlying m.tabs slice (effectiveTabs is
// a derived view).
func (m *Model) setTabScreen(
	effectiveIdx int, s Screen,
) {
	tabs := m.effectiveTabs()
	if effectiveIdx <= 0 || effectiveIdx >= len(tabs) {
		return
	}
	target := tabs[effectiveIdx]
	for i := range m.tabs {
		if m.tabs[i].Kind == target.Kind &&
			m.tabs[i].Key == target.Key &&
			m.tabs[i].Section == target.Section {
			m.tabs[i].Screen = s
			return
		}
	}
}

// Parent close or replacement must not discard a submitted SSH operation.
func (m Model) sshTabBusy(tab openTab) bool {
	if sshAccessBusy(tab.Screen) {
		return true
	}
	descendants := m.tabDescendantKinds(tab)
	for _, child := range m.tabs {
		if child.Section == tab.Section && descendants[child.Parent] && sshAccessBusy(child.Screen) {
			return true
		}
	}
	return false
}

// Parent links are by tab kind. Follow all levels so closing Accounts cannot
// orphan a key operation underneath vpn.
func (m Model) tabDescendantKinds(parent openTab) map[tabKind]bool {
	kinds := map[tabKind]bool{parent.Kind: true}
	for range len(m.tabs) {
		changed := false
		for _, tab := range m.tabs {
			if tab.Section == parent.Section && kinds[tab.Parent] && !kinds[tab.Kind] {
				kinds[tab.Kind], changed = true, true
			}
		}
		if !changed {
			break
		}
	}
	return kinds
}
