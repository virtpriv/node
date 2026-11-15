package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/virtpriv/node/internal/artifact/gpgtest"
	"github.com/virtpriv/node/internal/component"
	"github.com/virtpriv/node/internal/update/protocol"
	"github.com/virtpriv/node/internal/upstream"
)

// pincheck gives the maintainer the hash to pin at a bump. It must print no
// hash at all when the release fails any rule nodes used to apply: a bad
// signature, fewer listed signers than the threshold, a revoked key. The
// cases use the real gpg program with throwaway keys and serve the files
// from the test, as LND publishes them: one signature file per developer.
func TestPincheckPrintsNoHashWhenARuleFails(t *testing.T) {
	const version = "9.9.9-beta"
	archiveName := component.ArchiveName(protocol.LND, version)
	archive := []byte("the archive LND published")
	sum := sha256.Sum256(archive)
	hash := hex.EncodeToString(sum[:])

	h := gpgtest.NewHome(t)
	list := h.File("manifest.txt", hash+"  "+archiveName+"\n")
	other := h.File("other.txt", strings.Repeat("0", 64)+"  "+archiveName+"\n")
	archiveFile := h.File(archiveName, string(archive))

	names := []string{"alice", "bob", "carol"}
	var signers []upstream.Signer
	served := map[string]string{
		"list":    list,
		"archive": archiveFile,
	}
	for _, name := range names {
		fp := h.Key(name, "never", "")
		signers = append(signers, upstream.Signer{Name: name, Fingerprint: fp, KeyURL: "key-" + name})
		served["key-"+name] = h.Export(name, name+".asc")
		served["sig-"+name] = h.Sign(fp, "", list, name+".sig")
	}
	lnd := upstream.Program{
		Component:    protocol.LND,
		Name:         "LND",
		SignersAre:   "developers",
		Signers:      signers,
		MinSigners:   3,
		ListURL:      func(string) string { return "list" },
		SignatureURL: func(_ string, s upstream.Signer) string { return "sig-" + s.Name },
		ArchiveURL:   func(string) string { return "archive" },
	}

	// LND's pinned version, published with other bytes than the pin's.
	pinnedName := component.ArchiveName(protocol.LND, component.LNDVersion)
	pinnedList := h.File("pinned-manifest.txt", hash+"  "+pinnedName+"\n")
	served["pinned-list"] = pinnedList
	for _, name := range names {
		served["pinned-sig-"+name] = h.Sign(fingerprintOf(signers, name), "", pinnedList, name+"-pinned.sig")
	}
	pinned := lnd
	pinned.ListURL = func(string) string { return "pinned-list" }
	pinned.SignatureURL = func(_ string, s upstream.Signer) string { return "pinned-sig-" + s.Name }
	// A second program whose release cannot be downloaded at all.
	missing := lnd
	missing.Component = protocol.Syncthing
	missing.Name = "Syncthing"
	missing.ListURL = func(string) string { return "nowhere" }

	// Carol's signature over another list, her signature gone, her key
	// revoked. The other cases' key files were exported before the revocation.
	badSig := h.Sign(signers[2].Fingerprint, "", other, "carol-other.sig")
	h.RevokeKey("carol", gpgtest.Compromised)
	revokedKey := h.Export("carol", "carol-revoked.asc")

	for _, tc := range []struct {
		name     string
		args     []string
		programs []upstream.Program
		change   map[string]string
		printed  bool
	}{
		{name: "the pinned version with another hash than the pin", args: []string{"-lnd", component.LNDVersion}, programs: []upstream.Program{pinned}},
		{name: "a good release beside one that fails", args: []string{"-lnd", version, "-syncthing", "9.9.9"}, programs: []upstream.Program{lnd, missing}},
		{name: "three good signatures", printed: true},
		{name: "a bad signature", change: map[string]string{"sig-carol": badSig}},
		{name: "two of three signers", change: map[string]string{"sig-carol": ""}},
		{name: "a revoked key", change: map[string]string{"key-carol": revokedKey}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fetch := func(url, path string) error {
				src, ok := served[url]
				if changed, ok2 := tc.change[url]; ok2 {
					src, ok = changed, changed != ""
				}
				if !ok {
					return errors.New("not found")
				}
				b, err := os.ReadFile(src)
				if err != nil {
					return err
				}
				return os.WriteFile(path, b, 0o600)
			}
			var out bytes.Buffer
			args, programs := tc.args, tc.programs
			if args == nil {
				args, programs = []string{"-lnd", version}, []upstream.Program{lnd}
			}
			err := run(args, &out, programs, fetch)
			printed := strings.Contains(out.String(), "Hash to pin")
			if printed != tc.printed || (err == nil) != tc.printed {
				t.Fatalf("printed a hash %v, error %v, want a hash %v\n%s", printed, err, tc.printed, out.String())
			}
			if printed && !strings.Contains(out.String(), "Hash to pin: "+hash) {
				t.Fatalf("printed another hash\n%s", out.String())
			}
		})
	}
}

func fingerprintOf(signers []upstream.Signer, name string) string {
	for _, s := range signers {
		if s.Name == name {
			return s.Fingerprint
		}
	}
	return ""
}
