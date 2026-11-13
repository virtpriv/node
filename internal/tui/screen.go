// Package tui implements the unprivileged operator terminal interface.
package tui

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"time"

	"github.com/virtpriv/node/internal/api"
	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/lndrpc"
	"github.com/virtpriv/node/internal/syncthing"
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
	// invoiceStatusMsg), paste messages, and any
	// future non-key message types.
	HandleMsg(msg tea.Msg) (Screen, tea.Cmd)

	// View renders the screen content.
	// Width and height are passed as parameters since
	// they're only needed at render time.
	View(w, h int) string

	// HelpBindings returns the current key bindings
	// for the helpbar.
	HelpBindings() []key.Binding

	// Init returns any initial command (e.g.
	// createInvoiceCmd for receive flow after input).
	// Called once when the screen is mounted to a tab.
	Init() tea.Cmd
}

// ── ScreenContext ────────────────────────────────────────
// Model publishes one application snapshot through this shared context. Screens
// read the current observation when rendering. No screen owns a second copy.

type ScreenContext struct {
	NodeUpdates    nodeUpdates
	ChannelHistory *channelHistoryContext
	PaymentHistory *paymentHistoryContext
	OnChain        *OnChainContext
	// Jobs starts and watches the changes vpnd runs as jobs.
	Jobs jobRunner
	// shownJobs are the jobs whose outcome this TUI has shown, so a screen
	// opened again does not show it a second time.
	shownJobs           map[string]bool
	ConnectionInfo      connectionInfoReader
	Fees                feeReader
	Syncthing           *app.Syncthing
	syncthingRevision   uint64
	syncthingActive     uint64
	syncthingPending    bool
	syncthingPolling    bool
	WalletCreation      *app.WalletCreation
	walletCreationOwner *WalletCreateScreen
	WalletRuntime       walletRuntime
	walletRead          *walletStateRequest
	walletReadPending   bool
	walletClient        *walletClientRequest
	walletGeneration    uint64 // Scopes retained observations. Routine reads do not change it.
	Workflows           *app.Workflows
	SSHAccess           *app.SSHAccess
	AccountAccess       accountAccess
	sshAuthRevision     uint64
	Cfg                 *config.AppConfig
	State               *RuntimeState
	LndClient           *lndrpc.Client
	Status              *statusSnapshot
	HasTabs             bool   // varies by section, Model sets it before calling View/HelpBindings
	ContentFocused      bool   // true when content pane has focus (not tab bar, not sidebar)
	Version             string // set once at construction
	LatestVersion       string // updated by latestVersionMsg handler
	LatestChecked       bool   // the release check has answered, found or not
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

func (c *ScreenContext) syncthing() *app.Syncthing {
	if c.Syncthing == nil {
		c.Syncthing = app.NewSyncthing()
	}
	return c.Syncthing
}

func (c *ScreenContext) walletCreation() *app.WalletCreation {
	if c.WalletCreation == nil {
		c.WalletCreation = app.NewWalletCreation()
	}
	return c.WalletCreation
}

// RuntimeState contains facts whose authority is the live system rather than
// config.json. Known flags make read failures explicit so risky actions can
// fail closed instead of guessing.
type RuntimeState struct {
	WalletExists            bool
	WalletKnown             bool
	SSHPasswordAuthDisabled bool
	SSHPasswordAuthKnown    bool
	SyncthingDevices        []syncthing.Device
	SyncthingDevicesErr     error
	SyncthingDevicesKnown   bool
	SyncthingDevicesChecked time.Time
	SyncthingDelivery       map[string]syncthing.Delivery
	SyncthingDeliveryErr    error
	SyncthingDeliveryKnown  bool
	SyncthingCopy           app.BackupCopy
	SyncthingCopyErr        error
	SyncthingCopyKnown      bool
}

func (c *ScreenContext) invalidateWalletObservations() {
	c.cancelWalletRead()
	c.cancelWalletClient()
	c.walletGeneration++
	c.Status = nil
	if c.ChannelHistory != nil {
		c.ChannelHistory.ChannelHistorySnapshot = app.ChannelHistorySnapshot{}
	}
	if c.PaymentHistory != nil {
		c.PaymentHistory.PaymentHistorySnapshot = app.PaymentHistorySnapshot{}
	}
	if c.OnChain != nil {
		c.OnChain.OnChainSnapshot = app.OnChainSnapshot{}
		c.OnChain.Selection.Clear()
	}
}

func (c *ScreenContext) walletExists() bool {
	return c.State != nil && c.State.WalletKnown && c.State.WalletExists
}

func (c *ScreenContext) walletKnown() bool {
	return c.State != nil && c.State.WalletKnown
}

func (c *ScreenContext) walletDisplayAvailable() bool {
	if c.State == nil || !c.State.WalletExists {
		return false
	}
	return c.walletKnown() || (c.Status != nil &&
		(c.Status.Node.Known() || c.Status.Balance.Known() || c.Status.Channels.Known()))
}

func (c *ScreenContext) disabledWalletButtons(indices ...int) []int {
	if c.walletExists() {
		return nil
	}
	return indices
}

func walletUnavailableHelpBindings(c *ScreenContext) []key.Binding {
	enter := kEnterCreateWallet
	if !c.walletKnown() || c.walletClientUnavailable() {
		enter = kEnterRetryWalletRead
	}
	return []key.Binding{enter, kSidebar, kBack, kQuit}
}

// OnChainContext holds wallet data and the selection shared by home and send.
// Prepared sends own copies of their reviewed inputs.
type OnChainContext struct {
	app.OnChainSnapshot
	revision  uint64
	Selection app.CoinSelection
	reader    onChainReader
	scope     walletObservationScope
	active    *onChainRequest
	pending   bool
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
	Kind        tabKind
	Label       string
	Index       int    // for other detail tabs (dedup key)
	Key         string // funding outpoint for channel detail tabs
	Screen      Screen
	FocusTabBar bool    // true = tab bar focused on open
	Replace     bool    // true = replace existing screen on dedup
	Parent      tabKind // parent tab kind (0 = section home)
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

func emitRefreshStatus() tea.Msg {
	return refreshStatusMsg{}
}

func (c *ScreenContext) sshAccess() *app.SSHAccess {
	if c.SSHAccess != nil {
		return c.SSHAccess
	}
	return app.NewSSHAccess()
}
