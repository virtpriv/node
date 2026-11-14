// Package upstream checks the releases of Bitcoin Core, LND and Syncthing
// against their developers' signatures. Only the maintainer's pincheck tool
// uses it, at a bump. Nodes check these programs by the hash vpn pins.
package upstream

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/virtpriv/node/internal/artifact"
	"github.com/virtpriv/node/internal/component"
	"github.com/virtpriv/node/internal/update/protocol"
)

// Signer is one person or key whose signature counts on a release.
type Signer struct {
	Name string
	// Fingerprint is the primary key's, also when a subkey signs.
	Fingerprint string
	KeyURL      string
}

// Program is how one upstream program publishes and signs a release.
type Program struct {
	Component protocol.Component
	Name      string
	// SignersAre names the signers in pincheck's lines, as in "LND's listed
	// developers".
	SignersAre string
	Signers    []Signer
	MinSigners int
	// ListURL is the signed checksum list. Empty means the signature file
	// holds the list, clearsigned.
	ListURL func(version string) string
	// SignatureURL is the file holding s's signature. Several signers may
	// share one file.
	SignatureURL func(version string, s Signer) string
	ArchiveURL   func(version string) string
}

// Fetch saves what url serves as the file path.
type Fetch func(url, path string) error

// Checked is what a check proved. Hash is set only when every rule held.
type Checked struct {
	Signers []string
	Hash    string
}

// Check downloads version's checksum list, signatures, signer keys and
// archive into work, and returns the archive's SHA-256 only when at least
// MinSigners listed signers signed the list, no signature is bad and the
// archive matches the list. Each signer's signature is checked with that
// signer's key alone, so the result names who signed. A revoked key never
// counts and an expired one does. A signer whose signature or key cannot be
// downloaded is left out: LND removes the key of a developer who stops
// signing.
func (p Program) Check(version string, fetch Fetch, work string) (Checked, error) {
	if !protocol.ValidVersion(p.Component, version) {
		return Checked{}, fmt.Errorf("%q is not a %s version", version, p.Name)
	}
	list := ""
	if p.ListURL != nil {
		list = filepath.Join(work, "checksums")
		if err := fetch(p.ListURL(version), list); err != nil {
			return Checked{}, fmt.Errorf("download the checksum list: %w", err)
		}
	}
	signatures := map[string]string{}
	var signed []string
	var signedText []byte
	revoked := 0
	for i, s := range p.Signers {
		url := p.SignatureURL(version, s)
		sig, seen := signatures[url]
		if !seen {
			sig = filepath.Join(work, fmt.Sprintf("signature-%d", i))
			if err := fetch(url, sig); err != nil {
				sig = ""
			}
			signatures[url] = sig
		}
		if sig == "" {
			continue
		}
		key := filepath.Join(work, fmt.Sprintf("key-%d", i))
		if err := fetch(s.KeyURL, key); err != nil {
			continue
		}
		result, err := artifact.VerifySignature([]string{key}, sig, list, map[string]bool{s.Fingerprint: true})
		if err != nil {
			return Checked{}, err
		}
		if result.Bad {
			return Checked{}, fmt.Errorf("%s's signature does not match %s %s", s.Name, p.Name, version)
		}
		if len(result.Revoked) > 0 {
			revoked++
			continue
		}
		if result.Signers == 1 {
			signed = append(signed, s.Name)
			signedText = result.SignedText
		}
	}
	if len(signed) < p.MinSigners {
		msg := fmt.Sprintf("%d of %s's listed %s signed %s %s, %d needed", len(signed), p.Name, p.SignersAre, p.Name, version, p.MinSigners)
		if revoked > 0 {
			msg += fmt.Sprintf(", %d with a revoked key", revoked)
		}
		return Checked{}, errors.New(msg)
	}
	if list != "" {
		b, err := os.ReadFile(list)
		if err != nil {
			return Checked{}, err
		}
		signedText = b
	}
	name := component.ArchiveName(p.Component, version)
	want, err := artifact.SignedSHA256(signedText, name)
	if err != nil {
		return Checked{}, err
	}
	archive := filepath.Join(work, name)
	if err := fetch(p.ArchiveURL(version), archive); err != nil {
		return Checked{}, fmt.Errorf("download %s: %w", name, err)
	}
	got, err := sha256File(archive)
	if err != nil {
		return Checked{}, err
	}
	if got != want {
		return Checked{}, fmt.Errorf("%s differs from %s's signed list", name, p.Name)
	}
	return Checked{Signers: signed, Hash: got}, nil
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Programs are the three, in the order pincheck checks them.
var Programs = []Program{BitcoinCore, LND, Syncthing}

// BitcoinCore's signers are its builders.
// Source: github.com/bitcoin-core/guix.sigs/tree/main/builder-keys.
// Each key's fingerprint was checked against the .gpg file at its address.
// All five signed Bitcoin Core 31.2's SHA256SUMS.asc (November 9 2026), and
// each attested every final release from 29.3 to 31.2 in guix.sigs.
// Primary key fingerprints, not subkeys. Three of five are required, the
// default of Core's own contrib/verify-binaries script.
var BitcoinCore = Program{
	Component:  protocol.Bitcoin,
	Name:       component.Name(protocol.Bitcoin),
	SignersAre: "builders",
	Signers: []Signer{
		guixSigner("fanquake", "E777299FC265DD04793070EB944D35F9AC3DB76A"),
		guixSigner("guggero", "F4FC70F07310028424EFC20A8E4256593F177720"),
		guixSigner("hebasto", "D1DBF2C4B96F2DEBF4C16654410108112E7EA81F"),
		guixSigner("pinheadmz", "E61773CD6E01040E2F1BD78CE7E2984B6289C93A"),
		guixSigner("willcl-ark", "67AA5B46E7AF78053167FE343B8F814A784218F8"),
	},
	MinSigners:   3,
	ListURL:      func(v string) string { return component.ReleaseURL(protocol.Bitcoin, v, "SHA256SUMS") },
	SignatureURL: func(v string, _ Signer) string { return component.ReleaseURL(protocol.Bitcoin, v, "SHA256SUMS.asc") },
	ArchiveURL:   func(v string) string { return component.ArchiveURL(protocol.Bitcoin, v) },
}

func guixSigner(name, fingerprint string) Signer {
	return Signer{Name: name, Fingerprint: fingerprint,
		KeyURL: "https://raw.githubusercontent.com/bitcoin-core/guix.sigs/main/builder-keys/" + name + ".gpg"}
}

// LND's signers are the developers whose keys may sign a release, by the
// name in their signature and key file names, with each key's primary
// fingerprint.
// Source: scripts/verify-install.sh and scripts/keys in
// github.com/lightningnetwork/lnd at master 11f79be (November 6 2026). Each
// key file's primary fingerprint matched the script's list. Every LND final
// release from v0.20.0 to v0.21.6-beta carries at least four of their
// signatures (November 9 2026). LND's own script asks for five, but it is
// meant for LND's latest release only, and LND removes the key of a
// developer who stops signing, so three are required.
var LND = Program{
	Component:  protocol.LND,
	Name:       component.Name(protocol.LND),
	SignersAre: "developers",
	Signers: []Signer{
		lndSigner("roasbeef", "A5B61896952D9FDA83BC054CDC42612E89237182"),
		lndSigner("bhandras", "9FC6B0BFD597A94DBF09708280E5375C094198D8"),
		lndSigner("sputn1ck", "4DC235556B18694E08518DBB671103D881A5F0E4"),
		lndSigner("yyforyongyu", "E85497D2DBA0EB9ADB0024279BCD95C4FF296868"),
		lndSigner("hieblmi", "32F7EA1E7A0339F7D37164B9F82D456EA023C9BF"),
		lndSigner("suheb", "3E9BD4436C288039CA827A9200C9E2BC2E45666F"),
		lndSigner("ziggie1984", "5F75437E11695F86D50C11BB1AFF9C4DCED6D666"),
		lndSigner("ViktorT-11", "C20A78516A0944900EBFCA29961CC8259AE675D4"),
		lndSigner("georgetsagk", "1583B601BB57CC7CD2DF8A87E08DEA9B12B66AF6"),
		lndSigner("gijswijs", "7530B54D5E45A68760E68926019A44857735FD20"),
		lndSigner("boris", "BCEE34B0F9CD832214CE53005EA98470361ACB4F"),
	},
	MinSigners: 3,
	ListURL:    func(v string) string { return component.ReleaseURL(protocol.LND, v, "manifest-v"+v+".txt") },
	SignatureURL: func(v string, s Signer) string {
		return component.ReleaseURL(protocol.LND, v, "manifest-"+s.Name+"-v"+v+".sig")
	},
	ArchiveURL: func(v string) string { return component.ArchiveURL(protocol.LND, v) },
}

func lndSigner(name, fingerprint string) Signer {
	return Signer{Name: name, Fingerprint: fingerprint,
		KeyURL: "https://raw.githubusercontent.com/lightningnetwork/lnd/master/scripts/keys/" + name + ".asc"}
}

// Syncthing signs with one release key.
// Source: https://syncthing.net/release-key.txt, linked from
// https://syncthing.net/security/ ("Release Signatures"). Cross-checked on
// June 9 2026 against the apt keyring of a months-old install
// (/etc/apt/keyrings/syncthing-archive-keyring.gpg), which holds the same
// primary fingerprint, and against the VALIDSIG of v2.1.1's
// sha256sum.txt.asc.
//
// Syncthing signs with two keys while it changes keys: v2.1.1's checksum
// file also carries a signature from the earlier key (ending
// D26E6ED000654A3E), which gpg cannot check here and which makes it exit
// with an error on a genuine release. So only gpg's VALIDSIG lines count,
// never its exit code.
//
// The checksum list and its signature are one clearsigned file. Only the
// text inside the signed block is trusted.
var Syncthing = Program{
	Component:  protocol.Syncthing,
	Name:       component.Name(protocol.Syncthing),
	SignersAre: "release keys",
	Signers: []Signer{{
		Name:        "Syncthing Release Management",
		Fingerprint: "FBA2E162F2F44657B38F0309E5665F9BD5970C47",
		KeyURL:      "https://syncthing.net/release-key.txt",
	}},
	MinSigners: 1,
	SignatureURL: func(v string, _ Signer) string {
		return component.ReleaseURL(protocol.Syncthing, v, "sha256sum.txt.asc")
	},
	ArchiveURL: func(v string) string { return component.ArchiveURL(protocol.Syncthing, v) },
}
