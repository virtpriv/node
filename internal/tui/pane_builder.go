package tui

import (
	"slices"
	"strings"
	"unicode"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/virtpriv/node/internal/theme"
)

// paneBuilder constructs consistently-formatted content
// panes. Every pane: blank line → centered title →
// blank line → content → optional buttons.
type paneBuilder struct {
	w     int
	lines []string
}

func newPane(w int) *paneBuilder {
	return &paneBuilder{w: w, lines: []string{""}}
}

func (p *paneBuilder) title(
	style lipgloss.Style, text string,
) *paneBuilder {
	p.lines = append(p.lines,
		centerPad(style.Render(text), p.w))
	p.lines = append(p.lines, "")
	return p
}

func (p *paneBuilder) blank() *paneBuilder {
	p.lines = append(p.lines, "")
	return p
}

func (p *paneBuilder) line(s string) *paneBuilder {
	p.lines = append(p.lines, s)
	return p
}

func (p *paneBuilder) field(
	label, value string,
) *paneBuilder {
	p.lines = append(p.lines,
		" "+theme.Label.Render(label)+
			theme.Value.Render(value))
	return p
}

func (p *paneBuilder) monoField(
	label, value string,
) *paneBuilder {
	p.lines = append(p.lines,
		" "+theme.Label.Render(label)+
			theme.Mono.Render(value))
	return p
}

func (p *paneBuilder) labelLine(
	label string,
) *paneBuilder {
	p.lines = append(p.lines,
		" "+theme.Label.Render(label))
	return p
}

func (p *paneBuilder) mono(text string) *paneBuilder {
	p.lines = append(p.lines,
		" "+theme.Mono.Render(text))
	return p
}

// monoWrap renders a long string across multiple lines,
// splitting at lineW characters per line. Used for txids,
// pubkeys, addresses, invoices, and other long values.
func (p *paneBuilder) monoWrap(text string) *paneBuilder {
	lineW := p.w - 2
	if lineW < 16 {
		lineW = 16
	}
	for len(text) > 0 {
		end := lineW
		if end > len(text) {
			end = len(text)
		}
		p.mono(text[:end])
		text = text[end:]
	}
	return p
}

// fieldAligned is like field but pads the label on
// the right to labelW characters so that a run of
// fieldAligned calls with the same labelW produces a
// clean vertical column of colons. Callers compute
// labelW from the longest label in their group.
//
// Only works correctly for plain-ASCII labels because
// padding is computed against len(label), not
// lipgloss.Width(label). If you need styled or
// wide-rune labels, widen to lipgloss.Width first.
func (p *paneBuilder) fieldAligned(
	label, value string, labelW int,
) *paneBuilder {
	padded := label
	if len(label) < labelW {
		padded = label +
			strings.Repeat(" ", labelW-len(label))
	}
	p.lines = append(p.lines,
		" "+theme.Label.Render(padded)+
			theme.Value.Render(value))
	return p
}

func (p *paneBuilder) dim(text string) *paneBuilder {
	p.lines = append(p.lines,
		" "+theme.Dim.Render(text))
	return p
}

func (p *paneBuilder) warn(text string) *paneBuilder {
	p.lines = append(p.lines,
		" "+theme.Warning.Render(text))
	return p
}

func (p *paneBuilder) warnWrap(
	text string,
) *paneBuilder {
	lineW := p.w - 2
	if lineW < 16 {
		lineW = 16
	}
	for len(text) > 0 {
		end := lineW
		if end > len(text) {
			end = len(text)
		}
		p.warn(text[:end])
		text = text[end:]
	}
	return p
}

// valueWrap renders prose at theme.Value style, breaking
// at word boundaries to fit the pane width. Use for
// long explanatory text on flow screens (e.g. confirm
// screens with paragraphs of warnings) instead of
// hand-wrapping with multiple p.line() calls.
func (p *paneBuilder) valueWrap(
	text string,
) *paneBuilder {
	return p.wrappedLines(text, theme.Value)
}

// warnWrapWords wraps prose at theme.Warning style,
// breaking at word boundaries. Use for warning blocks
// that span multiple lines. (warnWrap above is
// character-based and used for long opaque tokens like
// error messages — this is for prose warnings.)
func (p *paneBuilder) warnWrapWords(
	text string,
) *paneBuilder {
	return p.wrappedLines(text, theme.Warning)
}

// wrappedLines is the shared word-wrap implementation
// for valueWrap and warnWrapWords. Splits text on
// whitespace and packs words into lines that fit
// within p.w - 2 columns. Empty lines in the input
// (double newlines) become blank pane lines.
func (p *paneBuilder) wrappedLines(
	text string, style lipgloss.Style,
) *paneBuilder {
	lineW := p.w - 3 // leading space + right margin
	if lineW < 16 {
		lineW = 16
	}
	// Honor explicit paragraph breaks in the input.
	paragraphs := strings.Split(text, "\n")
	for pi, para := range paragraphs {
		if pi > 0 {
			p.lines = append(p.lines, "")
		}
		if para == "" {
			continue
		}
		words := strings.Fields(para)
		var current string
		for _, w := range words {
			if current == "" {
				current = w
				continue
			}
			if len(current)+1+len(w) > lineW {
				p.lines = append(p.lines,
					" "+style.Render(current))
				current = w
				continue
			}
			current += " " + w
		}
		if current != "" {
			p.lines = append(p.lines,
				" "+style.Render(current))
		}
	}
	return p
}

func (p *paneBuilder) success(
	text string,
) *paneBuilder {
	p.lines = append(p.lines,
		" "+theme.Success.Render(text))
	return p
}

func (p *paneBuilder) input(
	label string, view string, focused bool,
) *paneBuilder {
	labelStyle := theme.Header
	marker := " "
	if focused {
		marker = theme.NavActive.Render("▸")
	}
	p.lines = append(p.lines,
		" "+labelStyle.Render(label))
	p.lines = append(p.lines,
		marker+" "+view)
	return p
}

func (p *paneBuilder) buttons(
	labels []string, activeIdx int, focused bool,
) *paneBuilder {
	p.lines = append(p.lines,
		renderButtons(labels, activeIdx, focused, p.w))
	return p
}

func (p *paneBuilder) appendError(
	errMsg string,
) *paneBuilder {
	if errMsg != "" {
		p.lines = append(p.lines, "")
		p.warnWrap(errMsg)
	}
	return p
}

func (p *paneBuilder) render() string {
	return strings.Join(p.lines, "\n")
}

// fitText adds text as rows that fit the pane, broken between words. The
// text's own line breaks are kept and its empty lines dropped. A word longer
// than a row is split. Control characters other than line breaks become
// spaces, so command output inside an error cannot move the cursor or clear
// the screen. With maxRows above zero, at most that many rows are added and
// the last one ends with "…". It reports whether text was left out.
func (p *paneBuilder) fitText(
	style lipgloss.Style, text string, maxRows int,
) bool {
	width := max(p.w-2, 16)
	text = strings.Map(func(r rune) rune {
		if r != '\n' && unicode.IsControl(r) {
			return ' '
		}
		return r
	}, text)
	var rows []string
	for line := range strings.SplitSeq(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			rows = append(rows, strings.Split(ansi.Wrap(line, width, ""), "\n")...)
		}
	}
	cut := maxRows > 0 && len(rows) > maxRows
	if cut {
		rows = rows[:maxRows]
		rows[maxRows-1] = ansi.Truncate(rows[maxRows-1]+" …", width, "…")
	}
	for _, row := range rows {
		p.lines = append(p.lines, " "+style.Render(row))
	}
	return cut
}

// renderWithBottomButtons draws the content with the button row on the last
// of h rows. Rows are counted as drawn: one entry may hold several. Content
// that does not fit above the buttons loses its last rows, so the buttons
// are always visible.
func (p *paneBuilder) renderWithBottomButtons(
	labels []string, activeIdx int,
	focused bool, h int,
	disabled ...int,
) string {
	btnLine := renderButtons(
		labels, activeIdx, focused, p.w, disabled...)
	rows := strings.Split(strings.Join(p.lines, "\n"), "\n")
	room := max(h-lipgloss.Height(btnLine), 0)
	rows = rows[:min(len(rows), room)]
	for len(rows) < room {
		rows = append(rows, "")
	}
	return strings.Join(append(rows, btnLine), "\n")
}

// ── Shared button renderer ───────────────────────────────

func renderButtons(
	labels []string, activeIdx int,
	focused bool, w int,
	disabled ...int,
) string {
	btnW := w - 2
	if btnW < 20 {
		btnW = 20
	}
	numBtns := len(labels)
	if numBtns == 0 {
		return ""
	}
	totalGap := (numBtns - 1) * 2
	perBtn := (btnW - totalGap) / numBtns
	if perBtn < 8 {
		perBtn = 8
	}

	// Buttons share the row equally. When that would wrap a label, each
	// button takes the width of its own label instead, if the row has room.
	widths := make([]int, numBtns)
	needed, wraps := totalGap, false
	for i, label := range labels {
		widths[i] = perBtn
		own := lipgloss.Width(label) + 2 // one column of padding each side
		needed += own
		wraps = wraps || own > perBtn
	}
	if wraps && needed <= btnW {
		spare := (btnW - needed) / numBtns
		for i, label := range labels {
			widths[i] = lipgloss.Width(label) + 2 + spare
		}
	}

	var parts []string
	for i, label := range labels {
		if slices.Contains(disabled, i) {
			parts = append(parts,
				lipgloss.NewStyle().
					Foreground(theme.ColorGrayed).
					Width(widths[i]).
					AlignHorizontal(lipgloss.Center).
					Render(label))
			continue
		}

		isActive := focused && activeIdx == i
		if isActive {
			parts = append(parts,
				theme.BtnFocused.
					Width(widths[i]).
					AlignHorizontal(lipgloss.Center).
					Render(label))
		} else {
			parts = append(parts,
				theme.BtnNormal.
					Width(widths[i]).
					AlignHorizontal(lipgloss.Center).
					Render(label))
		}
	}
	// Join complete button blocks so a wrapped label cannot split the row.
	for i := 1; i < len(parts); i++ {
		parts[i] = lipgloss.NewStyle().PaddingLeft(2).Render(parts[i])
	}
	return lipgloss.NewStyle().PaddingLeft(1).Render(lipgloss.JoinHorizontal(lipgloss.Top, parts...))
}
