package artifact

import (
	"slices"
	"testing"

	"github.com/virtualprivatenode/vpn/internal/artifact/gpgtest"
)

// A signer whose key was revoked must never count, and a signer in good
// standing must not be refused because of something else in the key file.
func TestSignerStandingDecidesWhetherASignatureCounts(t *testing.T) {
	h := gpgtest.NewHome(t)
	data := h.File("data.txt", "release checksums\n")

	// Signatures are made first, then the keys change. A revocation is never
	// allowed to depend on the date a signature claims.
	stolen := h.Key("stolen", "never", "")
	stolenSig := h.Sign(stolen, "", data, "stolen.sig")
	clearRevoked := h.Clearsign(stolen, data, "stolen-clearsigned.asc")
	h.RevokeKey("stolen", gpgtest.Compromised)

	retired := h.Key("retired", "never", "")
	retiredSig := h.Sign(retired, "", data, "retired.sig")
	h.RevokeKey("retired", gpgtest.Superseded)

	// A master key with a signing subkey that was replaced after a theft. It
	// also carries a long-expired subkey that signed nothing, as several
	// upstream key files do; gpg mentions such a subkey on every check.
	master := h.Master("master", gpgtest.Past)
	h.Subkey("master", "1y", gpgtest.Past)
	oldSub := h.Subkey("master", "never", "")
	oldSubSig := h.Sign(oldSub, "", data, "oldsub.sig")
	h.RevokeSubkey("master", oldSub)
	newSub := h.Subkey("master", "never", "")
	newSubSig := h.Sign(newSub, "", data, "newsub.sig")

	// Made and used in the past with a one-year life, so it is expired now.
	expired := h.Key("expired", "1y", gpgtest.Past)
	expiredSig := h.Sign(expired, gpgtest.Past, data, "expired.sig")

	// Expired and revoked at once. gpg then reports the signature as made by
	// an expired key, which must not hide the revocation.
	lapsed := h.Key("lapsed", "1y", gpgtest.Past)
	lapsedSig := h.Sign(lapsed, gpgtest.Past, data, "lapsed.sig")
	h.RevokeKey("lapsed", gpgtest.Compromised)
	lapsedMaster := h.Master("lapsedmaster", gpgtest.Past)
	lapsedSub := h.Subkey("lapsedmaster", "1y", gpgtest.Past)
	lapsedSubSig := h.Sign(lapsedSub, gpgtest.Past, data, "lapsedsub.sig")
	h.RevokeSubkey("lapsedmaster", lapsedSub)

	// A thief who holds a revoked key can still publish a copy of it that
	// looks merely expired. The owner's revocation in another copy must win.
	masked := h.Key("masked", "never", gpgtest.Past)
	maskedSig := h.Sign(masked, gpgtest.Past, data, "masked.sig")
	h.Expire("masked", "1y", gpgtest.Past)
	maskedExpiredCopy := h.Export("masked", "masked-expired.key")
	h.RevokeKey("masked", gpgtest.Compromised)
	maskedRevokedCopy := h.Export("masked", "masked-revoked.key")

	good := h.Key("good", "never", gpgtest.Past)
	goodSig := h.Sign(good, "", data, "good.sig")
	shortLived := h.SignExpiring(good, gpgtest.Past, "1y", data, "short-lived.sig")
	stranger := h.Key("stranger", "never", "")
	strangerSig := h.Sign(stranger, "", data, "stranger.sig")

	keys := map[string]string{}
	for _, name := range []string{"stolen", "retired", "master", "expired", "good", "lapsed", "lapsedmaster"} {
		keys[name] = h.Export(name, name+".key")
	}
	keys["masked, expired copy"], keys["masked, revoked copy"] = maskedExpiredCopy, maskedRevokedCopy

	cases := []struct {
		name    string
		keys    []string
		sig     string
		data    string
		pinned  []string
		signers int
		revoked []string
	}{
		{name: "revoked key", keys: []string{"stolen"}, sig: stolenSig, data: data, pinned: []string{stolen}, revoked: []string{stolen}},
		{name: "revoked key, reason superseded", keys: []string{"retired"}, sig: retiredSig, data: data, pinned: []string{retired}, revoked: []string{retired}},
		{name: "revoked signing subkey", keys: []string{"master"}, sig: oldSubSig, data: data, pinned: []string{master}, revoked: []string{master}},
		{name: "revoked key, clearsigned file", keys: []string{"stolen"}, sig: clearRevoked, pinned: []string{stolen}, revoked: []string{stolen}},
		{name: "one good and one revoked signer in one file", keys: []string{"good", "stolen"}, sig: h.Join("mixed.sig", goodSig, stolenSig), data: data, pinned: []string{good, stolen}, signers: 1, revoked: []string{stolen}},
		{name: "revoked signer listed before the good one", keys: []string{"good", "stolen"}, sig: h.Join("mixed-reversed.sig", stolenSig, goodSig), data: data, pinned: []string{good, stolen}, signers: 1, revoked: []string{stolen}},
		{name: "expired and revoked key", keys: []string{"lapsed"}, sig: lapsedSig, data: data, pinned: []string{lapsed}, revoked: []string{lapsed}},
		{name: "expired and revoked signing subkey", keys: []string{"lapsedmaster"}, sig: lapsedSubSig, data: data, pinned: []string{lapsedMaster}, revoked: []string{lapsedMaster}},
		{name: "revocation beside a copy that only looks expired", keys: []string{"masked, expired copy", "masked, revoked copy"}, sig: maskedSig, data: data, pinned: []string{masked}, revoked: []string{masked}},
		{name: "revocation loaded before a copy that only looks expired", keys: []string{"masked, revoked copy", "masked, expired copy"}, sig: maskedSig, data: data, pinned: []string{masked}, revoked: []string{masked}},
		{name: "signature that has itself expired", keys: []string{"good"}, sig: shortLived, data: data, pinned: []string{good}},

		{name: "expired key still counts", keys: []string{"expired"}, sig: expiredSig, data: data, pinned: []string{expired}, signers: 1},
		{name: "new subkey counts beside a revoked and an expired one", keys: []string{"master"}, sig: newSubSig, data: data, pinned: []string{master}, signers: 1},
		{name: "good new subkey outweighs a revoked old one of the same signer", keys: []string{"master"}, sig: h.Join("both-subkeys.sig", oldSubSig, newSubSig), data: data, pinned: []string{master}, signers: 1},
		{name: "unknown second signature does not spoil a good one", keys: []string{"good"}, sig: h.Join("extra.sig", goodSig, strangerSig), data: data, pinned: []string{good}, signers: 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var files []string
			for _, k := range c.keys {
				files = append(files, keys[k])
			}
			pinned := map[string]bool{}
			for _, p := range c.pinned {
				pinned[p] = true
			}
			got, err := VerifySignature(files, c.sig, c.data, pinned)
			if err != nil {
				t.Fatal(err)
			}
			if got.Bad {
				t.Fatal("signature reported as not matching the data")
			}
			if got.Signers != c.signers || !slices.Equal(got.Revoked, c.revoked) {
				t.Fatalf("accepted signers = %d, revoked = %v; want %d and %v", got.Signers, got.Revoked, c.signers, c.revoked)
			}
		})
	}
}
