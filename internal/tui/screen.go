// Package tui implements the unprivileged operator terminal interface.
package tui

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/virtpriv/node/internal/api"
	"github.com/virtpriv/node/internal/app"
)

// ── Screen interface ────────────────────────────────────
// Each tab's content implements this interface.
// The three routing tables (key dispatch, view dispatch,
// helpbar dispatch) collapse into single interface calls.

type Screen interface {
	// HandleKey processes keyboard input.
	// Returns the (possibly new) screen and a command.
	HandleKey(key string, msg tea.KeyPressMsg) (Screen, tea.Cmd)

	// HandleMsg processes async results (e.g.
	// sshKeysListMsg), paste messages, and any
	// future non-key message types.
	HandleMsg(msg tea.Msg) (Screen, tea.Cmd)

	// View renders the screen content.
	// Width and height are passed as parameters since
	// they're only needed at render time.
	View(w, h int) string

	// HelpBindings returns the current key bindings
	// for the helpbar.
	HelpBindings() []key.Binding

	// Init returns any initial command (e.g. the first
	// read of a list). Called once when the screen is
	// mounted to a tab.
	Init() tea.Cmd
}

// ── ScreenContext ────────────────────────────────────────
// Model publishes one application snapshot through this shared context. Screens
// read the current observation when rendering. No screen owns a second copy.

type ScreenContext struct {
	NodeUpdates nodeUpdates
	// Jobs starts and watches the changes vpnd runs as jobs.
	Jobs jobRunner
	// shownJobs are the jobs whose outcome this TUI has shown, so a screen
	// opened again does not show it a second time.
	shownJobs       map[string]bool
	SSHAccess       *app.SSHAccess
	AccountAccess   accountAccess
	sshAuthRevision uint64
	State           *RuntimeState
	Status          *statusSnapshot
	HasTabs         bool   // Model sets it before calling View/HelpBindings
	ContentFocused  bool   // true when content pane has focus (not tab bar, not sidebar)
	Version         string // set once at construction
	LatestVersion   string // updated by latestVersionMsg handler
	LatestChecked   bool   // the release check has answered, found or not
	// FolderReleases lists the versions whose files are all in the releases
	// folder, newest first. FolderNotFolder reports a releases folder that is
	// a link or a file, so it is not read.
	FolderReleases  []string
	FolderNotFolder bool
}

// jobRunner starts and watches vpnd's jobs. Closing it stops watching, never
// a job.
type jobRunner interface {
	Start(kind string, params any) <-chan app.JobUpdate
	Watch(id string) <-chan app.JobUpdate
	Latest(kind string) (api.Job, bool, error)
	Close()
}

func (c *ScreenContext) markShown(id string) {
	if id == "" {
		return
	}
	if c.shownJobs == nil {
		c.shownJobs = map[string]bool{}
	}
	c.shownJobs[id] = true
}

func (c *ScreenContext) jobs() jobRunner {
	if c.Jobs == nil {
		c.Jobs = app.NewJobs()
	}
	return c.Jobs
}

// RuntimeState contains facts whose authority is the live system rather than
// config.json. Known flags make read failures explicit so risky actions can
// fail closed instead of guessing.
type RuntimeState struct {
	SSHPasswordAuthDisabled bool
	SSHPasswordAuthKnown    bool
}

// ── Screen-to-Model messages ────────────────────────────
// Screens emit these via tea.Cmd. They flow through the
// Bubble Tea event loop and arrive in Model's Update like
// any other message. Model handles them in its main
// switch, no synchronous cmd inspection needed.

// closeTabMsg tells Model to close the active tab.
type closeTabMsg struct{}

// openTabMsg tells Model to open a new tab with the
// given screen.
type openTabMsg struct {
	Kind    tabKind
	Label   string
	Key     string // key fingerprint for SSH key detail tabs
	Screen  Screen
	Replace bool    // true = replace existing screen on dedup
	Parent  tabKind // parent tab kind (0 = section home)
}

// focusSidebarMsg tells Model to move focus to the
// sidebar.
type focusSidebarMsg struct{}

// focusTabBarMsg tells Model to move focus to the tab
// bar.
type focusTabBarMsg struct{}

// focusParentMsg tells Model to focus the active tab's
// parent tab. Model reads the Parent field from the
// active tab, finds the matching open tab in the same
// section, and sets activeTab to it: no close, no
// cascade. If no parent tab is open (Parent == 0 or
// parent was closed), falls back to focusing the
// section home (activeTab = 0).
type focusParentMsg struct{}

// refreshStatusMsg tells Model to re-fetch node status.
// statusResultMsg carries a completed observation and its request identity.
type refreshStatusMsg struct{}

// ── Message emitters ────────────────────────────────────
// Screens use these as tea.Cmd values. Each is a
// func() tea.Msg that returns instantly. The message
// flows through the Bubble Tea runtime on the next tick.

func emitCloseTab() tea.Msg {
	return closeTabMsg{}
}

func emitFocusSidebar() tea.Msg {
	return focusSidebarMsg{}
}

func emitFocusTabBar() tea.Msg {
	return focusTabBarMsg{}
}

func emitFocusParent() tea.Msg {
	return focusParentMsg{}
}

func (c *ScreenContext) sshAccess() *app.SSHAccess {
	if c.SSHAccess != nil {
		return c.SSHAccess
	}
	return app.NewSSHAccess()
}
