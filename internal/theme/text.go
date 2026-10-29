package theme

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

// PadTo fills styled text with spaces to w cells, or cuts it with "…" when
// it is wider.
func PadTo(s string, w int) string {
	if ansi.StringWidth(s) > w {
		return ansi.Truncate(s, w, "…")
	}
	return s + strings.Repeat(" ", w-ansi.StringWidth(s))
}

// Spread puts left at the start of w cells and right at their end, with at
// least one space between them.
func Spread(left, right string, w int) string {
	gap := max(1, w-ansi.StringWidth(left)-ansi.StringWidth(right))
	return PadTo(left+strings.Repeat(" ", gap)+right, w)
}

// PlainText keeps observed comments and host metadata from controlling the
// terminal or disguising their displayed order. Stored key bytes stay intact.
func PlainText(text string) string {
	return strings.Map(func(r rune) rune {
		if r == unicode.ReplacementChar || unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			return ' '
		}
		return r
	}, text)
}

// Wrap breaks plain text into lines of at most w cells, between words, so
// a name such as install-state.json stays whole. A word longer than a line,
// a long path for example, breaks after its last "/" that fits, so a file
// name stays whole, else after its last "-", and is cut only when it has
// neither. ansi.Wordwrap is not used because it always breaks after a
// hyphen, even inside a word that fits.
func Wrap(s string, w int) []string {
	var out []string
	for _, para := range strings.Split(s, "\n") {
		line := ""
		flush := func() { out = append(out, line); line = "" }
		for _, word := range strings.Fields(para) {
			for ansi.StringWidth(word) > w {
				if line != "" {
					flush()
				}
				head := ansi.Truncate(word, max(w, 1), "")
				if head == "" {
					// One character wider than the line: give it its own.
					head = string([]rune(word)[:1])
				}
				i := strings.LastIndex(head, "/")
				if i <= 0 {
					i = strings.LastIndex(head, "-")
				}
				if i > 0 {
					head = head[:i+1]
				}
				line, word = head, word[len(head):]
				flush()
			}
			switch {
			case word == "":
			case line == "":
				line = word
			case ansi.StringWidth(line)+1+ansi.StringWidth(word) <= w:
				line += " " + word
			default:
				flush()
				line = word
			}
		}
		flush()
	}
	return out
}
