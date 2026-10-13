package tui

import (
	"charm.land/bubbles/v2/key"
)

// ── Helper ──────────────────────────────────────────────

func bind(helpKey, helpDesc string, keys ...string) key.Binding {
	return key.NewBinding(
		key.WithKeys(keys...),
		key.WithHelp(helpKey, helpDesc))
}

// ── Shared constants ────────────────────────────────────

var (
	// Navigation
	kQuit    = bind("ctrl+c", "quit", "ctrl+c")
	kSidebar = bind("←", "sidebar", "left")
	kEnter   = bind("enter", "select", "enter")
	kBack    = bind("⌫", "back", "backspace")

	// Confirm dialog
	kYesConfirm = bind("y", "confirm", "y")
	kNoCancel   = bind("n", "cancel", "n")

	// Directional
	kLeftRightButtons = bind("←→", "buttons", "left", "right")
	kRightButton      = bind("→", "button", "right")
	kLeftRightCursor  = bind("←→", "cursor", "left", "right")
	kUpTabBar         = bind("↑", "tab bar", "up")
	kShiftTabBar      = bind("⇧tab", "tab bar", "shift+tab")
	kUpShiftTabBar    = bind("↑/⇧tab", "tab bar", "up", "shift+tab")
	kShiftTabButtons  = bind("⇧tab", "buttons", "shift+tab")
	kShiftTabInput    = bind("⇧tab", "input", "shift+tab")
	kTabNext          = bind("tab", "next", "tab")
	kTabButtons       = bind("tab", "buttons", "tab")
	kTabNextField     = bind("tab", "next field", "tab")
	kUpDownSelect     = bind("↑↓", "select", "up", "down")
	kUpDownFields     = bind("↑↓", "fields", "up", "down")
	kUpDownChannels   = bind("↑↓", "channels", "up", "down")

	// Enter variants
	kEnterOpen            = bind("enter", "open", "enter")
	kEnterClose           = bind("enter", "close", "enter")
	kEnterDetails         = bind("enter", "details", "enter")
	kEnterNext            = bind("enter", "next", "enter")
	kEnterConfirm         = bind("enter", "confirm", "enter")
	kEnterCreate          = bind("enter", "create", "enter")
	kEnterCreateWallet    = bind("enter", "create wallet", "enter")
	kEnterRetryWalletRead = bind("enter", "retry wallet state", "enter")
)

// ── Button navigation helper ────────────────────────────
// Shared by all screens with a two-button row. When the
// cursor is on the first button, left goes to sidebar and
// right goes to the next button. Otherwise both arrows
// navigate between buttons.

func buttonNav(btnIdx int) []key.Binding {
	if btnIdx == 0 {
		return []key.Binding{kSidebar, kRightButton}
	}
	return []key.Binding{kLeftRightButtons}
}

// ── Archetype: section home, button zone ───────────────
// Buttons above a list/table on a section home screen.
// downLabel names the content below (e.g. "channels").

func homeButtonBindings(
	downLabel string, btnIdx int, hasTabs bool,
) []key.Binding {
	var binds []key.Binding
	if btnIdx == 0 {
		binds = []key.Binding{
			bind("←/⌫", "sidebar", "left", "backspace"),
			kRightButton,
		}
	} else {
		binds = []key.Binding{kLeftRightButtons}
	}
	binds = append(binds,
		bind("↓", downLabel, "down"),
		kEnter)
	if hasTabs {
		binds = append(binds, kUpTabBar)
	}
	if btnIdx > 0 {
		binds = append(binds, kBack)
	}
	binds = append(binds, kQuit)
	return binds
}

// ── Archetype: section home, list zone ─────────────────
// Scrollable list/table below buttons on a home screen.
// itemsLabel names the items (e.g. "channels").
// enterLabel names the enter action (e.g. "details").
// shiftTabLabel names the zone above (e.g. "buttons").

func homeListBindings(
	itemsLabel, enterLabel, shiftTabLabel string,
) []key.Binding {
	return []key.Binding{
		bind("↑↓", itemsLabel, "up", "down"),
		bind("enter", enterLabel, "enter"),
		bind("⇧tab", shiftTabLabel, "shift+tab"),
		bind("←/⌫", "sidebar", "left", "backspace"),
		kQuit,
	}
}

// ── Archetype: view-only detail tab ─────────────────────
// Read-only tab with no buttons. Navigate away via
// backspace (parent) or sidebar. Used by PaymentDetail,
// OnChainTx, UtxoDetail, pending ChannelDetail.

func viewDetailBindings(hasTabs bool) []key.Binding {
	binds := []key.Binding{kSidebar}
	if hasTabs {
		binds = append(binds, kUpShiftTabBar)
	}
	binds = append(binds, kBack, kQuit)
	return binds
}

// ── Archetype: tab with buttons ─────────────────────────
// Tab content showing buttons (e.g. pairing, receive,
// post-action views). No list below.

func tabButtonBindings(hasTabs bool) []key.Binding {
	binds := []key.Binding{kEnter, kLeftRightButtons, kSidebar}
	if hasTabs {
		binds = append(binds, kShiftTabBar)
	}
	binds = append(binds, kBack, kQuit)
	return binds
}

// ── Archetype: action button screen ─────────────────────
// Two-button confirm/action screen (Go Back + action).
// Used by install screens and flow confirm steps.

func actionButtonBindings(
	btnIdx int, hasTabs bool,
) []key.Binding {
	binds := buttonNav(btnIdx)
	binds = append(binds, kEnter)
	if hasTabs {
		binds = append(binds, kUpTabBar)
	}
	binds = append(binds, kBack, kQuit)
	return binds
}

// ── Archetype: detail tab with action ───────────────────
// Tab showing an item with Cancel + action button
// (e.g. "Cancel" / "Close Channel"). Cancel is always
// index 0, action is index 1. Arrow-down from tab bar
// lands on Cancel (safe default).

func detailActionBindings(
	enterLabel string, btnIdx int, hasTabs bool,
) []key.Binding {
	binds := buttonNav(btnIdx)
	binds = append(binds,
		bind("enter", enterLabel, "enter"))
	if hasTabs {
		binds = append(binds, kShiftTabBar)
	}
	binds = append(binds, kBack, kQuit)
	return binds
}

// ── Archetype: waiting / in-flight ──────────────────────
// Screen is busy, only quit is available.

func waitingBindings() []key.Binding {
	return []key.Binding{kQuit}
}

// inFlightBindings is for fund-moving operations where
// ctrl+c is blocked. Shows a status hint instead of keys.
func inFlightBindings() []key.Binding {
	return []key.Binding{
		bind("···", "in progress"),
	}
}

// ── Archetype: result screen ────────────────────────────
// Completed action. Enter closes tab, backspace goes to
// parent, standard navigation (left→sidebar, up→tab bar).

func resultBindings(hasTabs bool) []key.Binding {
	binds := []key.Binding{
		kEnterClose,
		kSidebar,
	}
	if hasTabs {
		binds = append(binds, kUpShiftTabBar)
	}
	binds = append(binds, kBack, kQuit)
	return binds
}

// ── Archetype: confirm dialog ───────────────────────────
// y/n confirmation overlay.

func confirmDialogBindings() []key.Binding {
	return []key.Binding{kYesConfirm, kNoCancel}
}

// ── Archetype: manage screen, button zone ──────────────
// Buttons above a list on a tab content screen
// (Syncthing devices, SSH keys).
// Uses shift+tab to reach tab bar (tab content context).

func manageButtonBindings(
	downLabel string, btnIdx int, hasTabs bool,
) []key.Binding {
	binds := buttonNav(btnIdx)
	binds = append(binds,
		bind("↓", downLabel, "down"),
		kEnter)
	if hasTabs {
		binds = append(binds, kShiftTabBar)
	}
	binds = append(binds, kBack, kQuit)
	return binds
}

// ── Model-level: sidebar ────────────────────────────────

func sidebarBindings() []key.Binding {
	return []key.Binding{
		bind("↑↓", "navigate", "up"),
		kEnter,
		kQuit,
	}
}

// ── Model-level: tab bar ────────────────────────────────

func tabBarBindings(viewOnly, onTab bool) []key.Binding {
	lr := bind("←→", "tabs", "left", "right")
	down := bind("↓", "content", "down", "tab")
	enter := bind("enter", "select", "enter")

	binds := []key.Binding{lr}
	if !viewOnly {
		binds = append(binds, down, enter)
	}
	if onTab {
		binds = append(binds,
			bind("⌫", "close tab", "backspace"))
	}
	binds = append(binds, kQuit)
	return binds
}
