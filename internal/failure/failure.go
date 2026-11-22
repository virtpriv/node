// Package failure is the one form of what an owner is told when something
// fails. It is a plain sentence. The failing program's own words are the
// cause, which goes to the log and never to a screen.
package failure

import (
	"strings"
	"unicode"
)

// A Failure is a failure that vpn has a plain sentence for.
//
// Text says what was seen and no more, in vpn's own words. It holds no
// output of a command. Cause is the error underneath, with the program's own
// words. The install shows Text and says where the log is.
// An error that is not a Failure has no plain sentence yet, and is shown
// with its own words.
type Failure struct {
	Text  string
	Cause error
}

func (f *Failure) Error() string { return f.Text }

// Unwrap gives the cause, so code that asks an error what it is still gets
// its answer.
func (f *Failure) Unwrap() error { return f.Cause }

// CauseLines returns the cause's own words for a log, one entry for each
// line that is not empty, with control characters turned into spaces.
func (f *Failure) CauseLines() []string {
	if f.Cause == nil {
		return nil
	}
	var out []string
	for line := range strings.SplitSeq(f.Cause.Error(), "\n") {
		line = strings.TrimSpace(strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return ' '
			}
			return r
		}, line))
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}
