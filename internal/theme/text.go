package theme

import (
	"strings"
	"unicode"
)

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
