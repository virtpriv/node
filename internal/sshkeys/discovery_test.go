package sshkeys

import "testing"

const (
	testKeyA = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEB alice@laptop"
	testKeyB = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgIC bob@desktop"
	// The wild cloud-init decoy shape: an options prefix with a
	// forced command ahead of real key material.
	testDecoy = `no-port-forwarding,no-agent-forwarding,` +
		`command="echo 'Please login as the user \"admin\" ` +
		`rather than the user \"root\".'" ` +
		`ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEB provisioning`
)

func TestClassifyAuthorizedKeysPlain(t *testing.T) {
	keys, excluded := Classify(
		testKeyA + "\n" + testKeyB + "\n")
	if len(keys) != 2 || excluded != 0 {
		t.Fatalf("got %d keys %d excluded, want 2/0",
			len(keys), excluded)
	}
	if keys[0].Comment != "alice@laptop" {
		t.Errorf("comment: got %q", keys[0].Comment)
	}
}

// The decoy trap (provider access restriction): a forced-command
// provider line must be EXCLUDED and COUNTED; never copied
// verbatim, never silently dropped.
func TestClassifyAuthorizedKeysExcludesDecoy(t *testing.T) {
	keys, excluded := Classify(
		testDecoy + "\n" + testKeyA + "\n")
	if len(keys) != 1 {
		t.Fatalf("got %d keys, want 1", len(keys))
	}
	if keys[0].Comment != "alice@laptop" {
		t.Errorf("wrong key survived: %+v", keys[0])
	}
	if excluded != 1 {
		t.Errorf("excluded: got %d, want 1", excluded)
	}
}

func TestClassifyAuthorizedKeysSkipsNoise(t *testing.T) {
	keys, excluded := Classify(
		"# a comment\n\n   \n" + testKeyA + "\n" +
			"complete garbage line\n")
	if len(keys) != 1 {
		t.Errorf("got %d keys, want 1", len(keys))
	}
	// Garbage without key material is noise, not an exclusion;
	// counting it would overstate what was on the box.
	if excluded != 0 {
		t.Errorf("excluded: got %d, want 0", excluded)
	}
}

func TestClassifyAuthorizedKeysReportsMalformedKeys(t *testing.T) {
	keys, excluded := Classify("ssh-ed25519 YQ== malformed\nssh-dss YQ== obsolete\n" + testKeyA + "\n")
	if len(keys) != 1 || excluded != 2 {
		t.Fatalf("got %d keys and %d exclusions", len(keys), excluded)
	}
}
