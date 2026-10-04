package release

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/virtualprivatenode/vpn/internal/artifact/gpgtest"
)

// The release key comes from a file in the repository and from the key
// server. What either one knows must be used, and neither may be needed alone.
func TestReleaseKeyIsReadFromBothSources(t *testing.T) {
	h := gpgtest.NewHome(t)
	sums := h.File("SHA256SUMS", "0000  vpn-0.7.1-amd64.tar.gz\n")

	master := h.Master("release", "")
	firstSub := h.Subkey("release", "never", "")
	firstSig := h.Sign(firstSub, "", sums, "first.sig")
	early := h.Export("release", "early.key") // before anything was revoked
	h.RevokeSubkey("release", firstSub)
	secondSub := h.Subkey("release", "never", "")
	secondSig := h.Sign(secondSub, "", sums, "second.sig")
	current := h.Export("release", "current.key") // first subkey revoked, second added
	unnamed := h.WithoutUserID(current, "unnamed.key")
	rubbish := h.File("rubbish.key", "<html>not a key</html>\n")

	const down = ""
	cases := []struct {
		name          string
		file, website string
		sig           string
		accepted      bool
		noticed       bool
		refusal       string
	}{
		{name: "revocation only on the website", file: early, website: current, sig: firstSig, refusal: "revoked"},
		{name: "revocation only in the repository file", file: current, website: early, sig: firstSig, refusal: "revoked"},
		{name: "revocation on a website copy without a name", file: early, website: unnamed, sig: firstSig, refusal: "revoked"},
		{name: "new subkey known only to the website", file: early, website: current, sig: secondSig, accepted: true},
		{name: "new subkey known only to the repository file", file: current, website: early, sig: secondSig, accepted: true},
		{name: "new subkey not known to the only source left", file: early, website: down, sig: secondSig, refusal: "not from the release signing key"},
		{name: "website answers with something that is not a key", file: current, website: rubbish, sig: secondSig, accepted: true, noticed: true},
		{name: "neither source holds a key", file: rubbish, website: rubbish, sig: secondSig, refusal: "could not be downloaded"},
		{name: "website unreachable", file: current, website: down, sig: secondSig, accepted: true, noticed: true},
		{name: "repository file unreachable", file: down, website: current, sig: secondSig, accepted: true},
		{name: "repository file is not a key", file: rubbish, website: current, sig: secondSig, accepted: true},
		{name: "both unreachable", file: down, website: down, sig: secondSig, refusal: "could not be downloaded"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			work := t.TempDir()
			copyFile(t, sums, filepath.Join(work, "SHA256SUMS"))
			copyFile(t, c.sig, filepath.Join(work, "SHA256SUMS.asc"))
			download := func(url, dest string) error {
				source := c.website
				switch {
				case url == keyFileURL:
					source = c.file
				case url != keyWebsiteURL+master:
					t.Errorf("unexpected download %s", url)
				}
				if source == down {
					return errors.New("connection refused")
				}
				copyFile(t, source, dest)
				return nil
			}
			check, err := verifySignature(work, master, download)
			if (err == nil) != c.accepted {
				t.Fatalf("accepted = %v, want %v (error: %v)", err == nil, c.accepted, err)
			}
			if err != nil && !strings.Contains(err.Error(), c.refusal) {
				t.Fatalf("refusal %q does not say %q", err, c.refusal)
			}
			// The notice is shown with a review, so it only matters when the
			// release was accepted.
			if err == nil && check.WebsiteUnreachable != c.noticed {
				t.Fatalf("website reported unreachable = %v, want %v", check.WebsiteUnreachable, c.noticed)
			}
		})
	}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	b, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, b, 0600); err != nil {
		t.Fatal(err)
	}
}

// Installed nodes read this file, so a wrong file would silently take away
// one of their two sources.
func TestRepositoryKeyFileHoldsThePinnedKey(t *testing.T) {
	out, err := exec.Command("gpg", "--batch", "--show-keys", "--with-colons", filepath.Join("..", "..", "keys", "release-key.asc")).Output()
	if err != nil {
		t.Fatalf("read keys/release-key.asc with gpg: %v", err)
	}
	if !strings.Contains(string(out), "\nfpr:::::::::"+SigningFingerprint+":") {
		t.Fatalf("keys/release-key.asc does not hold the pinned key %s", SigningFingerprint)
	}
}
