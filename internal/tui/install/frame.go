package install

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/virtpriv/node/internal/theme"
)

// The install screens draw the node TUI's frame. The measurements come from
// theme, which the TUI reads too, so the install shows the shape the owner
// meets after logging in. The drawing itself is separate from the TUI's,
// because the install animates it.
const (
	frameInside = theme.FrameWidth - 2
	contentW    = frameInside - theme.SidebarWidth - 1
	dialogW     = 54
	// sideRows is the sidebar's height: the content's rows beside the five
	// sections, then the row under them that the TUI uses for its theme
	// toggle and the install leaves empty.
	sideRows = theme.ContentRows + 1
)

// The opening animation, in frames of frameRate: the top bar's line runs
// across, the sidebar's divider runs down, its section lines run across,
// then the five names appear one after another.
const (
	lineAcross    = 10
	dividerDown   = 22
	sectionsDrawn = 32
	namesShown    = sectionsDrawn + 3*len(theme.Sections)
)

func padTo(s string, w int) string {
	if lipgloss.Width(s) > w {
		return ansi.Truncate(s, w, "…")
	}
	return s + strings.Repeat(" ", w-lipgloss.Width(s))
}

func spread(left, right string, w int) string {
	gap := max(1, w-lipgloss.Width(left)-lipgloss.Width(right))
	return padTo(left+strings.Repeat(" ", gap)+right, w)
}

// center places lines in the middle of a w by h area.
func center(lines []string, w, h int) []string {
	out := make([]string, 0, h)
	for range max(0, (h-len(lines))/2) {
		out = append(out, "")
	}
	for _, l := range lines {
		out = append(out, strings.Repeat(" ", max(0, (w-lipgloss.Width(l))/2))+l)
	}
	for len(out) < h {
		out = append(out, "")
	}
	for i := range out {
		out[i] = padTo(out[i], w)
	}
	return out[:h]
}

func wrap(s string, w int, style lipgloss.Style) []string {
	var out []string
	for _, l := range strings.Split(ansi.Wordwrap(s, w, ""), "\n") {
		out = append(out, style.Render(l))
	}
	return out
}

func button(label string, focused bool) string {
	if focused {
		return theme.BtnFocused.Render(" " + label + " ")
	}
	return theme.BtnNormal.Render(" " + label + " ")
}

// dialog draws a small window in the style of a desktop dialog: the title,
// a line or two of description, the body, and the buttons at the bottom
// right, with space around each.
func dialog(title, desc string, body []string, buttons string) []string {
	border := theme.FrameBorder
	inner := dialogW - 2
	const margin = "  "
	row := func(s string) string {
		return border.Render("│") + padTo(margin+s, inner) + border.Render("│")
	}
	edge := func(l, r string) string { return border.Render(l + strings.Repeat("─", inner) + r) }
	bold := lipgloss.NewStyle().Bold(true).Foreground(theme.ColorPrimary)
	out := []string{edge("╭", "╮"), row(""), row(bold.Render(title)), row("")}
	for _, l := range wrap(desc, inner-2*len(margin)-1, theme.Dim) {
		out = append(out, row(l))
	}
	out = append(out, row(""))
	for _, l := range body {
		out = append(out, row(l))
	}
	out = append(out, row(""))
	out = append(out, border.Render("│")+padTo(strings.Repeat(" ", max(0, inner-len(margin)-lipgloss.Width(buttons)))+buttons, inner)+border.Render("│"))
	return append(out, row(""), edge("╰", "╯"))
}

// sidebar draws the TUI's sidebar for h rows at opening frame f: the five
// sections, each closed by a line, then an empty row. lit shows the
// section names in full color, otherwise they are faint. It returns the
// sidebar cells, the divider column beside them and the rows of the section
// lines, where the frame's left border joins them.
func sidebar(h, f int, lit bool) (cells, divider []string, sections map[int]bool) {
	border := theme.FrameBorder
	sections = map[int]bool{}
	var starts []int
	for i := range theme.Sections {
		start := i * (theme.SectionRows + 1)
		starts = append(starts, start)
		sections[start+theme.SectionRows] = true
	}
	down := min(h, max(0, f-lineAcross)*h/(dividerDown-lineAcross))
	across := min(theme.SidebarWidth, max(0, f-dividerDown)*theme.SidebarWidth/(sectionsDrawn-dividerDown))
	cells = make([]string, h)
	divider = make([]string, h)
	for r := range h {
		cells[r] = strings.Repeat(" ", theme.SidebarWidth)
		if sections[r] && across > 0 {
			cells[r] = border.Render(strings.Repeat("─", across)) + strings.Repeat(" ", theme.SidebarWidth-across)
		}
		divider[r] = " "
		if r < down {
			divider[r] = border.Render("│")
			if sections[r] && across == theme.SidebarWidth {
				divider[r] = border.Render("┤")
			}
		}
	}
	name := lipgloss.NewStyle().Foreground(theme.ColorFaint)
	if lit {
		name = theme.NavItem
	}
	cell := lipgloss.NewStyle().Width(theme.SidebarWidth).Align(lipgloss.Center)
	for i, label := range theme.Sections {
		if f < sectionsDrawn+3*(i+1) {
			break
		}
		cells[starts[i]+theme.SectionRows/2] = cell.Render(name.Render(label))
	}
	return cells, divider, sections
}

// frame joins the top bar's rows, the sidebar at opening frame f and the
// content into the TUI's shape, with the help line, already styled, below.
func frame(bar []string, f int, lit bool, content []string, help string) string {
	border := theme.FrameBorder
	h := sideRows
	cells, divider, sections := sidebar(h, f, lit)
	out := []string{border.Render("╭" + strings.Repeat("─", frameInside) + "╮")}
	for _, b := range bar {
		out = append(out, border.Render("│")+padTo(b, frameInside)+border.Render("│"))
	}
	// The top bar's line runs across first, then joins the divider.
	across := min(frameInside, f*frameInside/lineAcross)
	line := strings.Repeat("─", across) + strings.Repeat(" ", frameInside-across)
	left, right := "├", "┤"
	if across < frameInside {
		right = "│"
	}
	if across == 0 {
		left = "│"
	}
	if f > lineAcross {
		line = strings.Repeat("─", theme.SidebarWidth) + "┬" + strings.Repeat("─", contentW)
	}
	out = append(out, border.Render(left+line+right))
	for r := range h {
		edge := "│"
		if sections[r] && f >= sectionsDrawn {
			edge = "├"
		}
		c := ""
		if r < len(content) {
			c = content[r]
		}
		out = append(out, border.Render(edge)+cells[r]+divider[r]+padTo(c, contentW)+border.Render("│"))
	}
	bottom := strings.Repeat("─", frameInside)
	if f >= dividerDown {
		bottom = strings.Repeat("─", theme.SidebarWidth) + "┴" + strings.Repeat("─", contentW)
	}
	out = append(out, border.Render("╰"+bottom+"╯"))
	// The help line is padded on both sides to the frame's width, so
	// centring the whole screen leaves it where it is.
	pad := max(0, (theme.FrameWidth-lipgloss.Width(help))/2)
	out = append(out, strings.Repeat(" ", pad)+help+strings.Repeat(" ", max(0, theme.FrameWidth-pad-lipgloss.Width(help))))
	return strings.Join(out, "\n")
}
