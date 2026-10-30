package theme

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// The keys as both programs name them in their help lines, with the symbols
// macOS prints on its keys. The space bar has no symbol, so it keeps its
// name, and letter keys are written as the letter.
const (
	KeyEnter     = "↩"
	KeyCtrlC     = "⌃C"
	KeyTab       = "⇥"
	KeyShiftTab  = "⇧⇥"
	KeyBackspace = "⌫"
	KeyUp        = "↑"
	KeyDown      = "↓"
	KeyLeft      = "←"
	KeyRight     = "→"
	KeySpace     = "space"
)

// HelpLine draws a help line from key and description pairs: the key bright,
// its description dim, thin bars between them. Pairs that do not fit in maxW
// are left off from the end, but the first is always drawn.
func HelpLine(maxW int, pairs ...string) string {
	if maxW <= 0 {
		return ""
	}
	var parts []string
	for i := 0; i+1 < len(pairs); i += 2 {
		if pairs[i] != "" {
			parts = append(parts, HelpKey.Render(pairs[i])+" "+HelpDesc.Render(pairs[i+1]))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	sep := HelpSep.Render(" │ ")
	for n := len(parts); n > 1; n-- {
		if line := strings.Join(parts[:n], sep); lipgloss.Width(line) <= maxW {
			return line
		}
	}
	return parts[0]
}
