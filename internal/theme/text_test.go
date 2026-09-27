package theme

import (
	"strings"
	"testing"
)

func TestPlainTextDoesNotLetObservedMetadataControlTheTerminal(t *testing.T) {
	for _, text := range []string{"laptop\x1b[2J", "\x1b]52;c;AAAA\a", "name\rspoof", "name\u202etxt", "name\x9b31m"} {
		got := PlainText(text)
		if strings.ContainsAny(got, "\x1b\x07\r\u009b\u202e\ufffd") {
			t.Fatalf("terminal control retained: %q", got)
		}
	}
	if got := PlainText("laptop · café"); got != "laptop · café" {
		t.Fatalf("ordinary text changed: %q", got)
	}
}
