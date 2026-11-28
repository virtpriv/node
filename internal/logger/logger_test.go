package logger

import (
	"bytes"
	"strings"
	"testing"
)

// Every entry is one line of the file, so every line starts with its time and
// a reader of the log never meets a line without one. A failure's paragraphs
// read as sentences there, and a program's own output cannot send a control
// character to a terminal that shows the log.
func TestEveryEntryIsOneLine(t *testing.T) {
	for _, tc := range []struct{ name, msg, want string }{
		{"a finding and its fix",
			"found the firewall rule ufw allow 8080/tcp, which vpn did not set\n\nremove it, so only SSH answers from outside",
			"found the firewall rule ufw allow 8080/tcp, which vpn did not set. Remove it, so only SSH answers from outside"},
		{"a command's own output",
			"sh -c x: exit status 3: E: first line\nE: second line\n",
			"sh -c x: exit status 3: E: first line E: second line"},
		{"a control character", "found \x1b[2Jthis", "found  [2Jthis"},
		{"a bidi override and a line separator", "found \u202eevil\u202c and\u2028more", "found  evil  and more"},
		{"an empty paragraph", "found it.\n\n\n\nremove it", "found it. Remove it"},
		{"one line", "step 3/16 complete: Closing every port except SSH", "step 3/16 complete: Closing every port except SSH"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b bytes.Buffer
			prev := SetOutput(&b)
			defer SetOutput(prev)
			Install("%s", tc.msg)
			line := b.String()
			if strings.Count(line, "\n") != 1 || !strings.HasSuffix(line, "\n") {
				t.Fatalf("the entry %q is not one line", line)
			}
			if _, got, _ := strings.Cut(strings.TrimSuffix(line, "\n"), "] [install] "); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
