package tui

import (
	"os"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/virtualprivatenode/vpn/internal/app"
	"github.com/virtualprivatenode/vpn/internal/component"
	"github.com/virtualprivatenode/vpn/internal/config"
	"github.com/virtualprivatenode/vpn/internal/lndrpc"
	"github.com/virtualprivatenode/vpn/internal/logger"
	"github.com/virtualprivatenode/vpn/internal/syncthing"
	"github.com/virtualprivatenode/vpn/internal/theme"
)

type wSubview int

// ── TUI layout constants ────────────────────────────────
// Change these to resize the entire TUI frame. All widths
// and heights derive from these values.
const (
	tuiWidth  = 82
	tuiHeight = 34
)

const (
	svNone wSubview = iota
	svQR
	svFullURL
)

// Tab types for the top tab bar
type tabKind int

const (
	tabMain              tabKind = iota // Main view for current section
	tabChannel                          // Channel detail
	tabPayment                          // Payment detail
	tabSend                             // ⚡ Send payment flow
	tabReceive                          // ⚡ Receive payment flow
	tabPairing                          // Pairing screen
	tabOnChain                          // ⛓ On-chain send flow
	tabOCReceive                        // ⛓ On-chain receive flow
	tabSyncthing                        //
	tabSyncthingDevice                  // Syncthing device detail
	tabSyncthingWebUI                   // Syncthing Web UI
	tabSyncthingPair                    // Syncthing pair device flow
	tabOpenChannel                      // Channel open flow
	tabOnChainTx                        // on-chain transaction detail
	tabUtxoDetail                       // UTXO detail with label edit
	tabChannelHistory                   // channel history view
	tabSyncthingInstall                 // Syncthing install flow
	tabP2PUpgrade                       // P2P mode upgrade flow
	tabSelfUpdate                       // Self-update flow
	tabAutoUnlock                       // Auto-unlock configuration flow
	tabWalletCreate                     // Wallet creation flow
	tabNodeInfo                         // Receive channel / node info screen
	tabAccounts                         // Local accounts and key import
	tabSSHKeys                          // SSH key management
	tabSSHKeyDetail                     // SSH key detail (per-key)
	tabSSHKeyAdd                        // SSH key add flow
	tabSSHPasswordAuth                  // SSH password auth toggle
	tabSSHChangePassword                // change login password
)

type openTab struct {
	Kind  tabKind
	Label string
	Index int    // row identity for other detail tabs
	Key   string // funding outpoint for channel detail tabs
	// Section remains the tab's owner across wallet-to-auto-unlock transitions.
	Section int
	// Parent declares which tab kind owns this tab.
	// Zero means "section home is the parent" (top-
	// level detail tabs opened from home screens).
	// Non-zero means this tab is a child of another
	// detail tab (e.g. tabSyncthingDevice's Parent is
	// tabSyncthing). Used by closeTab for cascade-
	// close and by focusParentMsg for backspace
	// navigation. No grandchild tabs exist — depth
	// is at most two levels.
	Parent tabKind
	Screen Screen // L16: owns all state for this tab's content (nil = legacy path)
}

type systemRefreshMsg struct{}
type tickMsg time.Time
type latestVersionMsg string

// tabActivatedMsg is delivered to a screen's HandleMsg
// when the user navigates to (or lands on) the screen's
// tab. Screens opt in by handling it — those that don't
// care silently ignore it via the default fall-through.
// Used to refresh stale data without replacing the screen
// or its in-progress state.
type tabActivatedMsg struct{}

type syncthingPairedMsg struct {
	owner   *SyncthingPairScreen
	attempt uint64
	result  app.SyncthingResult
}
type syncthingRemovedMsg struct {
	owner   *SyncthingDeviceScreen
	attempt uint64
	result  app.SyncthingResult
}
type syncthingDevicesMsg struct {
	owner    *ScreenContext
	revision uint64
	devices  []syncthing.Device
	err      error
}
type syncthingCloseMsg struct {
	owner   Screen
	attempt uint64
}
type channelOpenResultMsg struct {
	attempt *channelOpenAttempt
	result  app.ChannelOpenResult
}
type newAddressMsg struct {
	owner   *OCReceiveScreen
	attempt uint64
	address app.OnChainReceiveAddress
	err     error
}
type receiveAddressQRMsg struct {
	owner   *OCReceiveScreen
	attempt uint64
}
type invoiceCreatedMsg struct {
	attempt *invoiceAttempt
	invoice app.LightningInvoice
	err     error
}
type invoiceCheckMsg struct {
	attempt *invoiceAttempt
}
type invoiceStatusMsg struct {
	attempt *invoiceAttempt
	state   app.InvoiceState
	err     error
}
type payReqDecodedMsg struct {
	attempt *paymentAttempt
	payment app.PreparedPayment
	err     error
}
type sendPaymentResultMsg struct {
	attempt *paymentAttempt
	result  *lndrpc.SendPaymentResult
	err     error
}
type onChainSendAttempt struct{ prepared app.PreparedOnChainSend }

type sendCoinsResultMsg struct {
	attempt *onChainSendAttempt
	result  app.OnChainSendResult
}

type channelCloseResultMsg struct {
	attempt *channelCloseAttempt
	result  app.ChannelCloseResult
}

type labelTxMsg struct {
	owner   *OnChainHomeScreen
	attempt uint64
	result  app.TransactionLabelResult
}

type channelInfo = app.StatusChannel

type peerOption struct {
	Pubkey      string
	Host        string
	Alias       string
	TorOnly     bool
	Taproot     bool
	MinChanSize int64
}

type statusSnapshot = app.StatusSnapshot

type Model struct {
	cfg       *config.AppConfig
	prefs     *config.Preferences
	state     *RuntimeState
	lndClient *lndrpc.Client
	version   string

	disableSuspend bool
	subview        wSubview
	width          int
	height         int

	// L16: shared context for screen components
	screenCtx *ScreenContext

	// L16: section home screens (nil = legacy path)
	sectionScreens [numSections]Screen

	latestVersion       string
	statusCollector     statusReader
	statusActive        *statusRequest
	statusPending       bool
	statusScope         statusScope
	statusRevision      uint64
	verificationActive  *sshVerificationRequest
	verificationPending bool

	// QR/Copy displays (Model-owned overlays and terminal handoff)
	urlTarget         string
	qrLabel           string
	connectionDisplay *connectionActionMsg
	qrCopyOverlay     *qrCopyDisplayRequest
	copyTerminal      *qrCopyDisplayRequest

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
	cfg *config.AppConfig, prefs *config.Preferences,
	state *RuntimeState, version string,
) Model {
	theme.Init(prefs.Theme != "light")
	m := Model{
		cfg: cfg, prefs: prefs, state: state,
		version: version,
		subview: svNone, statusCollector: app.NewStatusCollector(),
		nav: NewNavSidebar(),
	}
	m.screenCtx = &ScreenContext{
		Syncthing:       app.NewSyncthing(),
		HelperWorkflows: app.NewHelperWorkflows(component.SyncthingVersion),
		Cfg:             cfg,
		State:           state,
		Version:         version,
	}
	m.screenCtx.ChannelHistory = &channelHistoryContext{reader: app.NewChannelHistoryReader()}
	m.screenCtx.OnChain = &OnChainContext{reader: app.NewOnChainReader()}
	m.screenCtx.PaymentHistory = &paymentHistoryContext{reader: app.NewPaymentHistoryReader()}
	m.sectionScreens[secChannels] =
		NewChannelsHomeScreen(m.screenCtx)
	m.sectionScreens[secWallet] =
		NewWalletHomeScreen(m.screenCtx)
	m.sectionScreens[secOnChain] =
		NewOnChainHomeScreen(m.screenCtx, m.screenCtx.OnChain)
	m.sectionScreens[secAddons] =
		NewAddonsHomeScreen(m.screenCtx)
	m.sectionScreens[secSystem] =
		NewSystemHomeScreen(m.screenCtx)
	return m
}

func serviceNames(cfg *config.AppConfig) []string { return app.StatusServices(*cfg) }

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
	if !m.state.WalletKnown || !m.state.KeyVerificationKnown {
		return 5 * time.Second
	}
	if !m.screenCtx.Status.Node.Fresh() && m.cfg.HasLND() &&
		m.state.WalletKnown && m.state.WalletExists {
		return 5 * time.Second
	}
	if m.nav.ActiveSection() == secOnChain && m.screenCtx.OnChain != nil &&
		(m.screenCtx.OnChain.Utxos.Err != nil || m.screenCtx.OnChain.OnChainTxs.Err != nil) {
		return 5 * time.Second
	}
	if m.nav.ActiveSection() == secWallet && m.screenCtx.PaymentHistory != nil && !m.screenCtx.PaymentHistory.Fresh() {
		return 5 * time.Second
	}
	if m.visibleChannelHistoryCmd() != nil && !m.screenCtx.ChannelHistory.Closed.Fresh() {
		return 5 * time.Second
	}
	if m.nav.ActiveSection() == secChannels && !m.screenCtx.Status.Channels.Fresh() {
		return 5 * time.Second
	}
	return 60 * time.Second
}

func Show(
	cfg *config.AppConfig, prefs *config.Preferences, version string,
) {
	state := observeRuntimeState(cfg)
	m := NewModel(cfg, prefs, state, version)
	// The installer handoff has no job-control shell to resume a suspended TUI.
	m.disableSuspend = os.Getenv("VPN_TUI_NO_SUSPEND") == "1"
	// Bubble Tea does not cancel or join commands on exit. The workflow owner
	// releases helper readers even when Run fails or provides no final model.
	defer func() {
		if m.screenCtx.WalletRuntime != nil {
			m.screenCtx.WalletRuntime.Close()
		}
		if m.screenCtx.Fees != nil {
			m.screenCtx.Fees.Close()
		}
		if m.screenCtx.AccountAccess != nil {
			m.screenCtx.AccountAccess.Close()
		}
		if m.screenCtx.SSHVerification != nil {
			m.screenCtx.SSHVerification.Close()
		}
		if m.screenCtx.ConnectionInfo != nil {
			m.screenCtx.ConnectionInfo.Close()
		}
		if m.screenCtx.Reboots != nil {
			m.screenCtx.Reboots.Close()
		}
		if m.screenCtx.PackageUpdates != nil {
			m.screenCtx.PackageUpdates.Close()
		}
		if m.screenCtx.ServiceControls != nil {
			m.screenCtx.ServiceControls.Close()
		}
		m.screenCtx.ChannelHistory.reader.Close()
		m.screenCtx.OnChain.reader.Close()
		m.screenCtx.PaymentHistory.reader.Close()
		m.statusCollector.Close()
		if m.screenCtx.AutoUnlock != nil {
			m.screenCtx.AutoUnlock.Close()
		}
		if m.screenCtx.WalletCreation != nil {
			m.screenCtx.WalletCreation.Close()
		}
		m.screenCtx.HelperWorkflows.Close()
		m.screenCtx.Syncthing.Close()
		if m.screenCtx.LndClient != nil {
			m.screenCtx.LndClient.Close()
		}
	}()
	p := tea.NewProgram(m)
	if _, err := p.Run(); err != nil {
		logger.TUI("terminal session: %v", err)
	}
}

func observeRuntimeState(cfg *config.AppConfig) *RuntimeState {
	state := &RuntimeState{}
	if enabled, err := app.NewSSHAccess().PasswordAuth(); err != nil {
		logger.TUI("read live SSH password authentication: %v", err)
	} else {
		state.SSHPasswordAuthDisabled = !enabled
		state.SSHPasswordAuthKnown = true
	}
	if cfg.SyncthingEnabled {
		runtime := app.NewSyncthing()
		state.SyncthingDevices, state.SyncthingDevicesErr = runtime.ListDevices()
		runtime.Close()
		state.SyncthingDevicesKnown = state.SyncthingDevicesErr == nil
		state.SyncthingDevicesChecked = time.Now()
	}
	return state
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		requestStatusCmd,
		fetchWalletStateCmd(m.screenCtx),
		requestSSHVerificationCmd,
		fetchLatestVersionCmd(),
		tickEveryCmd(m.pollInterval()))
}
