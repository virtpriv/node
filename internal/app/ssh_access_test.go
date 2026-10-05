package app

import (
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/virtpriv/node/internal/sshkeys"
	"golang.org/x/crypto/ssh"
)

type sshAuthFake struct {
	enabled    bool
	err        error
	reads      int
	duringRead func()
}

func (f *sshAuthFake) PasswordAuth() (bool, error) {
	f.reads++
	if f.duringRead != nil {
		f.duringRead()
	}
	return f.enabled, f.err
}
func (f *sshAuthFake) SetPasswordAuthDisabled(disabled bool) error { return f.err }
func accessFixture(t *testing.T) (*SSHAccess, string, string) {
	t.Helper()
	key := func(seed byte) string {
		public, err := ssh.NewPublicKey(ed25519.NewKeyFromSeed(bytesOf(seed)).Public())
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(public)))
	}
	return &SSHAccess{Keys: sshkeys.Store{Path: filepath.Join(t.TempDir(), ".ssh", "authorized_keys")}, Auth: &sshAuthFake{}}, key(1), key(2)
}
func bytesOf(b byte) []byte {
	out := make([]byte, 32)
	for i := range out {
		out[i] = b
	}
	return out
}

func TestSSHAddValidatesAndPreservesUnmanagedLines(t *testing.T) {
	access, a, _ := accessFixture(t)
	for _, invalid := range []string{"ssh-ed25519 YQ==", "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAAAAA=="} {
		if err := access.AddKey(invalid); err == nil {
			t.Fatalf("added malformed key %q", invalid)
		}
	}
	if _, err := os.Stat(filepath.Dir(access.Keys.Path)); !os.IsNotExist(err) {
		t.Fatal("invalid input touched storage")
	}
	before := "# operator comment\ncommand=\"false\" " + a + " restricted\nssh-ed25519 YQ== malformed"
	if _, err := access.Keys.Update(func([]byte) ([]byte, error) { return []byte(before), nil }); err != nil {
		t.Fatal(err)
	}
	if err := access.AddKey(a + " operator"); err != nil {
		t.Fatal(err)
	}
	data, _ := access.Keys.Read()
	if string(data) != before+"\n"+a+" operator\n" {
		t.Fatal("unmanaged content changed")
	}
	if err := access.AddKey(a + " another comment"); err == nil {
		t.Fatal("duplicate key added")
	}
}

func TestSSHRemoveLastDistinctKeyUsesFreshAuth(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled bool
		readErr error
		wantErr bool
	}{{"disabled", false, nil, true}, {"unknown", false, errors.New("unavailable"), true}, {"enabled", true, nil, false}} {
		t.Run(tc.name, func(t *testing.T) {
			access, a, _ := accessFixture(t)
			// The RSA entry has a zero modulus. Neither invalid entry is a fallback key.
			unmanaged := "ssh-ed25519 YQ== malformed\nssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAAAAA==\n# keep\n"
			original := a + " first\n" + a + " duplicate\n" + unmanaged
			if _, err := access.Keys.Update(func([]byte) ([]byte, error) { return []byte(original), nil }); err != nil {
				t.Fatal(err)
			}
			auth := access.Auth.(*sshAuthFake)
			auth.enabled = tc.enabled
			auth.err = tc.readErr
			auth.duringRead = func() {
				if err := access.Keys.WithLock(func() error { t.Fatal("disable operation could overlap last-key removal"); return nil }); err == nil {
					t.Fatal("guard released lock too soon")
				}
			}
			key, _ := ParseSSHKey(a)
			err := access.RemoveKey(key.Fingerprint)
			if (err != nil) != tc.wantErr || auth.reads != 1 {
				t.Fatalf("remove: %v reads %d", err, auth.reads)
			}
			data, _ := access.Keys.Read()
			if tc.wantErr && string(data) != original {
				t.Fatal("refused removal changed file")
			}
			if !tc.wantErr && string(data) != unmanaged {
				t.Fatal("did not remove all matching copies and preserve other lines")
			}
		})
	}
}

func TestSSHRemoveUsesCurrentKeySet(t *testing.T) {
	access, a, b := accessFixture(t)
	for _, line := range []string{a, b} {
		if err := access.AddKey(line); err != nil {
			t.Fatal(err)
		}
	}
	keyA, _ := ParseSSHKey(a)
	keyB, _ := ParseSSHKey(b)
	if err := access.RemoveKey(keyA.Fingerprint); err != nil {
		t.Fatal(err)
	}
	if access.Auth.(*sshAuthFake).reads != 0 {
		t.Fatal("read auth despite another credential")
	}
	// The earlier two-key snapshot cannot authorize removal of the remaining key.
	if err := access.RemoveKey(keyB.Fingerprint); err == nil {
		t.Fatal("stale key count bypassed guard")
	}
	if err := access.RemoveKey(keyA.Fingerprint); err == nil {
		t.Fatal("absent key reported removed")
	}
}
