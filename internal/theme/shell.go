package theme

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"
)

// PrintShell prints lines for the shell, after a full screen program has
// closed, indented by two spaces. A command stands alone on its line, after
// a line that says what it does and an empty line. On a terminal a line
// wider than the window is broken between words, as Python's argparse does
// for help text. Saved to a file, every line is printed as it is.
func PrintShell(w *os.File, lines []string) {
	width := 0
	if cols, _, err := term.GetSize(int(w.Fd())); err == nil {
		// The indent and a margin, so a full line never touches the edge.
		width = cols - 4
	}
	for _, line := range lines {
		for _, part := range strings.Split(line, "\n") {
			if width <= 0 || ansi.StringWidth(part) <= width {
				fmt.Fprintln(w, strings.TrimRight("  "+part, " "))
				continue
			}
			for _, l := range Wrap(part, width) {
				fmt.Fprintf(w, "  %s\n", l)
			}
		}
	}
}
