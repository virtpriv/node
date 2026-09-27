package installer

import (
	"github.com/virtualprivatenode/vpn/internal/loginpassword"
	"strings"
	"testing"
)

func TestGenerateAdminPassword(t *testing.T) {
	pw := generateAdminPassword()
	if len(pw) != 25 {
		t.Errorf("length: got %d, want 25", len(pw))
	}
	for _, c := range pw {
		if !strings.ContainsRune(
			"ABCDEFGHIJKLMNOPQRSTUVWXYZ"+
				"abcdefghijklmnopqrstuvwxyz0123456789", c) {
			t.Errorf("character %q outside alphabet", c)
		}
	}
	// The generated fallback must satisfy the same policy the
	// interactive prompt enforces.
	if _, err := loginpassword.New(pw); err != nil {
		t.Errorf("generated password fails validation: %v", err)
	}
	// Two draws must differ (sanity, not a randomness test).
	pw2 := generateAdminPassword()
	if pw == pw2 {
		t.Error("two generated passwords identical")
	}
}
