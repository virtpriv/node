package tui

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/virtpriv/node/internal/theme"
)

// Section IDs — 5 parents + theme toggle
const (
	secChannels    = 0
	secWallet      = 1
	secOnChain     = 2
	secAddons      = 3
	secSystem      = 4
	secThemeToggle = 5
)

const numSections = 5

type NavItem struct {
	Label   string
	Section int
}

type NavSidebar struct {
	Items      []NavItem
	Cursor     int
	ActiveItem int
	Width      int
	Height     int
	Focused    bool
}

// Nav styles are accessed directly from theme.NavItem,
// theme.NavActive, theme.NavCursor — no aliases needed.
// This ensures styles stay current after theme.Toggle().

func NewNavSidebar() NavSidebar {
	items := []NavItem{
		{"Channels", secChannels},
		{"Wallet", secWallet},
		{"On-Chain", secOnChain},
		{"Add-On", secAddons},
		{"System", secSystem},
		{theme.ThemeIcon(), secThemeToggle},
	}
	return NavSidebar{
		Items:      items,
		Cursor:     0,
		ActiveItem: 0,
		Width:      12,
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
	// Theme toggle is not a real section — don't
	// activate it via this path.
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

// BlockRows returns styled rows for each section block.
// Active section label is yellow (always visible).
// Cursor position shows ▸ when sidebar is focused.
// Cursor-only (not active) shows bright white.
// The theme toggle icon is rendered inside the System
// block, one row below the System label.
func (n NavSidebar) BlockRows(
	w int, blockHeights [numSections]int,
) [numSections][]string {
	var blocks [numSections][]string

	// Find the theme toggle item (last in Items).
	themeIdx := -1
	for i, it := range n.Items {
		if it.Section == secThemeToggle {
			themeIdx = i
			break
		}
	}

	// Lipgloss cell style for centering labels.
	// Width(w) + Align(Center) handles ANSI-aware
	// centering correctly for all label lengths.
	cellBase := lipgloss.NewStyle().
		Width(w).
		Align(lipgloss.Center)

	for si := 0; si < numSections; si++ {
		bh := blockHeights[si]
		if bh < 1 {
			bh = 1
		}

		item := n.Items[si]
		isActive := n.ActiveItem == si
		isCursor := n.Cursor == si && n.Focused

		style := theme.NavItem
		if isActive {
			style = theme.NavActive
		} else if isCursor {
			style = theme.NavCursor
		}

		label := item.Label

		titleRow := bh / 2

		// Theme icon goes in the last row of the
		// System block, left-aligned.
		isSystemBlock := si == secSystem
		themeRow := -1
		if isSystemBlock && themeIdx >= 0 && bh >= 2 {
			themeRow = bh - 1
		}

		var rows []string
		for r := 0; r < bh; r++ {
			if r == titleRow {
				styled := style.Render(label)
				if isCursor {
					// Center "▸ Label" as a single unit
					// so the cursor doesn't push the
					// label off-center relative to
					// non-cursor rows.
					combined := theme.NavActive.Render("▸") +
						" " + styled
					rows = append(rows,
						cellBase.Render(combined))
				} else {
					rows = append(rows,
						cellBase.Render(styled))
				}
			} else if r == themeRow && themeIdx >= 0 {
				// Render theme toggle icon in bottom-
				// left corner. Highlight when cursor
				// is on it, dim otherwise. No ▸ marker
				// — just the icon, enter to toggle.
				icon := n.Items[themeIdx].Label
				isThemeCursor := n.Cursor == themeIdx &&
					n.Focused

				iconStyle := theme.Dim
				if isThemeCursor {
					iconStyle = theme.NavActive
				}

				styledIcon := iconStyle.Render(icon)
				iconVis := lipgloss.Width(styledIcon)
				iconPad := w - 1 - iconVis
				if iconPad < 0 {
					iconPad = 0
				}
				rows = append(rows,
					" "+styledIcon+
						strings.Repeat(" ", iconPad))
			} else {
				rows = append(rows,
					strings.Repeat(" ", w))
			}
		}

		blocks[si] = rows
	}

	return blocks
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
