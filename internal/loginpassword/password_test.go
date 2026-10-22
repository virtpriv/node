package loginpassword

import (
	"fmt"
	"strings"
	"testing"
)

func TestNew(t *testing.T) {
	long := strings.Repeat("a", 16)

	cases := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"empty", "", true},
		{"one below minimum", strings.Repeat("a", 15), true},
		{"exactly minimum", long, false},
		// The minimum counts characters, the unit the owner is told.
		// Eight two-byte letters are 16 bytes but only 8 characters.
		{"eight two-byte letters", strings.Repeat("é", 8), true},
		{"sixteen two-byte letters", strings.Repeat("é", 16), false},
		{"above minimum", long + "extra", false},
		{"embedded NUL", long + "\x00suffix", true},
		{"embedded newline",
			long + "\n" + long, true},
		{"trailing newline", long + "\n", true},
		{"colon is allowed (only the username " +
			"side of chpasswd's line is delimited)",
			"with:colon:" + long, false},
		{"spaces allowed",
			"correct horse battery staple", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(tc.in)
			if tc.wantErr && err == nil {
				t.Errorf("New(%q): "+
					"expected error, got nil", tc.in)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("New(%q): "+
					"unexpected error: %v", tc.in, err)
			}
		})
	}
}

// The password must never leak through the fmt verbs that
// end up in logs and error chains.
func TestLoginPasswordRedaction(t *testing.T) {
	secret := strings.Repeat("s3cret!!", 4)
	pw, err := New(secret)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	for _, verb := range []string{
		"%v", "%s", "%#v", "%+v",
	} {
		got := fmt.Sprintf(verb, pw)
		if got != "[redacted]" {
			t.Errorf("fmt %s: got %q, want %q",
				verb, got, "[redacted]")
		}
	}
}
