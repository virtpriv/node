package tui

import (
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/logger"
	"github.com/virtpriv/node/internal/theme"
)

// ── TUI layout constants ────────────────────────────────
// The frame's size lives in theme, shared with the installer, which draws
// the same shape. All widths and heights derive from these values.
const tuiWidth = theme.FrameWidth

// Tab types for the top tab bar
type tabKind int

const (
	tabMain              tabKind = iota // Main view for current section
	tabUpdates                          // Node Updates and Debian packages
	tabNodeUpdates                      // Node update flow
	tabAccounts                         // Local accounts and key import
	tabSSHKeys                          // SSH key management
	tabSSHKeyDetail                     // SSH key detail (per-key)
	tabSSHKeyAdd                        // SSH key add flow
	tabSSHPasswordAuth                  // SSH password auth toggle
	tabSSHChangePassword                // change login password
	tabSettings                         // the TUI's settings and Test release
)

type openTab struct {
	Kind  tabKind
	Label string
	Key   string // key fingerprint for SSH key detail tabs
	// Section is the section the tab was opened in.
	Section int
	// Parent declares which tab kind owns this tab.
	// Zero means "section home is the parent" (top
	// level detail tabs opened from home screens).
	// Non-zero means this tab is a child of another
	// detail tab (e.g. tabSSHKeyDetail's Parent is
	// tabSSHKeys). Used by closeTab, which also
	// closes children and their children, and by
	// focusParentMsg for backspace navigation.
	Parent tabKind
	Screen Screen // owns all state for this tab's content
}

type systemRefreshMsg struct{}
type tickMsg time.Time
type latestVersionMsg string

// tabActivatedMsg is delivered to a screen's HandleMsg
// when the user navigates to (or lands on) the screen's
// tab. Screens opt in by handling it. Those that don't
// care silently ignore it via the default fall-through.
// Used to refresh stale data without replacing the screen
// or its in-progress state.
type tabActivatedMsg struct{}

type statusSnapshot = app.StatusSnapshot

type Model struct {
	prefs *config.Preferences
	state *RuntimeState

	width  int
	height int

	// shared context for screen components
	screenCtx *ScreenContext

	// relaunch asks Show to start the installed vpn once this one has ended.
	relaunch bool

	// section home screens
	sectionScreens [numSections]Screen

	statusCollector statusReader
	statusActive    *statusRequest
	statusPending   bool
	statusRevision  uint64

	// Navigation
	nav            NavSidebar
	contentFocused bool

	// Tab bar
	tabs            []openTab
	activeTab       int
	tabFocused      bool
	tabCursorX      int
	tabScrollOffset int

	// Per-section tab memory. sectionFocus[s] holds
	// the user's last activeTab index within section
	// s, so that returning to s and pressing up from
	// the sidebar restores their previous position
	// instead of jumping to the leftmost detail tab.
	// Zero means "no memory yet, fall back to tab 1".
	//
	sectionFocus [numSections]int
}

func NewModel(
	prefs *config.Preferences,
	state *RuntimeState, version string,
) Model {
	theme.Init(prefs.Theme != "light")
	m := Model{
		prefs: prefs, state: state,
		statusCollector: app.NewStatusCollector(),
		nav:             NewNavSidebar(),
	}
	m.screenCtx = &ScreenContext{
		NodeUpdates: app.NewNodeUpdates(),
		State:       state,
		Version:     version,
	}
	m.sectionScreens[secSystem] =
		NewSystemHomeScreen(m.screenCtx)
	return m
}

func (m Model) savePreferences() {
	if err := config.SavePreferences(m.prefs); err != nil {
		logger.TUI(
			"ERROR: failed to save TUI preferences: %v", err)
	}
}

func (m Model) pollInterval() time.Duration {
	if m.screenCtx.Status == nil {
		return 3 * time.Second
	}
	return 60 * time.Second
}

func Show(prefs *config.Preferences, version string) {
	// The TUI's readers are closed before a relaunch replaces this program.
	if err := finishRun(run(prefs, version), syscall.Exec); err != nil {
		relaunchFailed(err)
	}
}

// run shows the TUI until it ends and returns its final model.
func run(prefs *config.Preferences, version string) tea.Model {
	state := observeRuntimeState()
	m := NewModel(prefs, state, version)
	// Bubble Tea does not cancel or join commands on exit. The workflow owner
	// releases helper readers even when Run fails or provides no final model.
	defer func() {
		if m.screenCtx.AccountAccess != nil {
			m.screenCtx.AccountAccess.Close()
		}
		if m.screenCtx.Jobs != nil {
			m.screenCtx.Jobs.Close()
		}
		m.statusCollector.Close()
		if updater, ok := m.screenCtx.NodeUpdates.(*app.NodeUpdates); ok {
			updater.Close()
		}
	}()
	p := tea.NewProgram(m)
	final, err := p.Run()
	if err != nil {
		logger.TUI("terminal session: %v", err)
	}
	return final
}

func observeRuntimeState() *RuntimeState {
	state := &RuntimeState{}
	if enabled, err := app.NewSSHAccess().PasswordAuth(); err != nil {
		logger.TUI("read live SSH password authentication: %v", err)
	} else {
		state.SSHPasswordAuthDisabled = !enabled
		state.SSHPasswordAuthKnown = true
	}
	return state
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		requestStatusCmd,
		fetchLatestVersionCmd(),
		clearReleasesFolderCmd(m.screenCtx.NodeUpdates, m.screenCtx.Version),
		restoreSystemJobsCmd(m.screenCtx),
		tickEveryCmd(m.pollInterval()))
}
