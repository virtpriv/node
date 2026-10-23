package tui

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/virtpriv/node/internal/theme"
)

// Section IDs: the five sections and the theme toggle.
const (
	secChannels    = 0
	secWallet      = 1
	secOnChain     = 2
	secAddons      = 3
	secSystem      = 4
	secThemeToggle = 5
)

const numSections = len(theme.Sections)

type NavItem struct {
	Label   string
	Section int
}

type NavSidebar struct {
	Items      []NavItem
	Cursor     int
	ActiveItem int
	Height     int
	Focused    bool
}

// Nav styles are accessed directly from theme.NavItem,
// theme.NavActive and theme.NavCursor, with no aliases.
// This ensures styles stay current after theme.Toggle().

func NewNavSidebar() NavSidebar {
	items := []NavItem{
		{theme.Sections[secChannels], secChannels},
		{theme.Sections[secWallet], secWallet},
		{theme.Sections[secOnChain], secOnChain},
		{theme.Sections[secAddons], secAddons},
		{theme.Sections[secSystem], secSystem},
		{theme.ThemeIcon(), secThemeToggle},
	}
	return NavSidebar{
		Items:      items,
		Cursor:     0,
		ActiveItem: 0,
		Height:     24,
		Focused:    true,
	}
}

func (n *NavSidebar) Focus() { n.Focused = true }
func (n *NavSidebar) Blur()  { n.Focused = false }

func (n *NavSidebar) clamp() {
	if n.Cursor < 0 {
		n.Cursor = 0
	}
	if n.Cursor >= len(n.Items) {
		n.Cursor = len(n.Items) - 1
	}
	if n.ActiveItem < 0 {
		n.ActiveItem = 0
	}
	// ActiveItem must stay on a real section, never
	// on the theme toggle.
	if n.ActiveItem >= numSections {
		n.ActiveItem = numSections - 1
	}
}

func (n *NavSidebar) MoveUp() {
	if n.Cursor > 0 {
		n.Cursor--
	}
}

func (n *NavSidebar) MoveDown() {
	if n.Cursor < len(n.Items)-1 {
		n.Cursor++
	}
}

func (n *NavSidebar) Activate() int {
	n.clamp()
	// The theme toggle is not a real section. Do not
	// activate it through this path.
	if n.Items[n.Cursor].Section == secThemeToggle {
		return n.Items[n.ActiveItem].Section
	}
	n.ActiveItem = n.Cursor
	return n.Items[n.Cursor].Section
}

func (n *NavSidebar) ActiveSection() int {
	n.clamp()
	return n.Items[n.ActiveItem].Section
}

// IsOnThemeToggle reports whether the cursor is on the
// theme toggle item.
func (n *NavSidebar) IsOnThemeToggle() bool {
	n.clamp()
	return n.Items[n.Cursor].Section == secThemeToggle
}

// UpdateThemeLabel refreshes the theme icon label after
// a toggle so the sidebar shows the new icon.
func (n *NavSidebar) UpdateThemeLabel() {
	for i := range n.Items {
		if n.Items[i].Section == secThemeToggle {
			n.Items[i].Label = theme.ThemeIcon()
			return
		}
	}
}

func (n *NavSidebar) SetActive(section int) {
	for i, it := range n.Items {
		if it.Section == section {
			n.Cursor = i
			n.ActiveItem = i
			return
		}
	}
}

// BlockRows returns the styled rows of each section's box, theme.SectionRows
// tall, with the label in the middle. The active section is shown inverted,
// and the cursor shows ▸ while the sidebar has focus.
func (n NavSidebar) BlockRows(w int) [numSections][]string {
	var blocks [numSections][]string
	cell := lipgloss.NewStyle().Width(w).Align(lipgloss.Center)
	for si := range numSections {
		item := n.Items[si]
		isActive := n.ActiveItem == si
		isCursor := n.Cursor == si && n.Focused

		style := theme.NavItem
		if isActive {
			style = theme.NavActive
		} else if isCursor {
			style = theme.NavCursor
		}
		label := style.Render(item.Label)
		if isCursor {
			// Centre "▸ Label" as one unit so the label stays where it is
			// on rows without the cursor.
			label = theme.NavActive.Render("▸") + " " + label
		}

		rows := make([]string, theme.SectionRows)
		for r := range rows {
			rows[r] = strings.Repeat(" ", w)
		}
		rows[theme.SectionRows/2] = cell.Render(label)
		blocks[si] = rows
	}
	return blocks
}

// ThemeIconCell draws the theme toggle in its own row under the sections,
// bright while the cursor is on it.
func (n NavSidebar) ThemeIconCell(w int) string {
	style := theme.Dim
	if n.Focused && n.Cursor >= 0 && n.Cursor < len(n.Items) && n.Items[n.Cursor].Section == secThemeToggle {
		style = theme.NavActive
	}
	return lipgloss.NewStyle().Width(w).Align(lipgloss.Center).Render(style.Render(theme.ThemeIcon()))
}

// ── String helpers ───────────────────────────────────────

func pad(s string, w int) string {
	vis := lipgloss.Width(s)
	if vis >= w {
		return s
	}
	return s + strings.Repeat(" ", w-vis)
}

func centerPad(s string, w int) string {
	vis := lipgloss.Width(s)
	if vis >= w {
		return s
	}
	left := (w - vis) / 2
	right := w - vis - left
	return strings.Repeat(" ", left) + s +
		strings.Repeat(" ", right)
}
