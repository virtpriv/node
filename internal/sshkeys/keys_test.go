package sshkeys

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func publicKey(t *testing.T) string {
	t.Helper()
	key, err := ssh.NewPublicKey(ed25519.NewKeyFromSeed(make([]byte, 32)).Public())
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
}

func TestParseRejectsMalformedAndUnsupportedKeys(t *testing.T) {
	valid := publicKey(t)
	fields := strings.Fields(valid)
	data, _ := base64.StdEncoding.DecodeString(fields[1])
	for _, line := range []string{valid + " " + strings.Repeat("x", 8192), "ssh-ed25519 YQ==", "ssh-ed25519 !!!", "ssh-rsa " + fields[1], "ssh-ed25519 " + base64.StdEncoding.EncodeToString(append(data, 0)), "ssh-dss " + fields[1], "command=\"false\" " + valid, valid + "\n" + valid, "ssh-ed25519-cert-v01@openssh.com " + fields[1]} {
		if _, err := Parse(line); err == nil {
			t.Errorf("accepted %q", line)
		}
	}
	key, err := Parse(valid + " workstation")
	if err != nil {
		t.Fatal(err)
	}
	parsed, _, _, _, err := ssh.ParseAuthorizedKey([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	if key.Fingerprint != ssh.FingerprintSHA256(parsed) || key.Comment != "workstation" {
		t.Fatalf("incorrect key: %+v", key)
	}
}

func TestKeysDeduplicatesAndExcludesMalformedEntries(t *testing.T) {
	valid := publicKey(t)
	content := strings.Join([]string{
		valid + " first", valid + " second",
		"ssh-ed25519 YQ==",
		"ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAAAAA==",
	}, "\n")
	got := Keys([]byte(content))
	if len(got) != 1 || got[0].RawLine != valid+" first" {
		t.Fatalf("want the first distinct valid credential, got %+v", got)
	}
}

func TestParseUsesOpenSSHWhitespace(t *testing.T) {
	valid := publicKey(t)
	for _, whitespace := range []string{"\u00a0", "\v", "\f", "\u2003"} {
		for _, line := range []string{
			whitespace + valid,
			strings.Replace(valid, " ", whitespace, 1),
			valid + whitespace,
		} {
			if _, err := Parse(line); err == nil {
				t.Errorf("accepted unusable whitespace in %q", line)
			}
		}
	}
	for _, line := range []string{valid, " \t" + valid, strings.Replace(valid, " ", "\t", 1), valid + "\n", valid + "\r\n", valid + " café workstation"} {
		if _, err := Parse(line); err != nil {
			t.Errorf("rejected supported line %q: %v", line, err)
		}
	}
}

func TestStoreAtomicUpdateAndRefusal(t *testing.T) {
	store := Store{Path: filepath.Join(t.TempDir(), ".ssh", "authorized_keys")}
	original := []byte("# preserved\n" + publicKey(t) + "\n")
	changed, err := store.Update(func([]byte) ([]byte, error) { return original, nil })
	if !changed || err != nil {
		t.Fatalf("write: %v %v", changed, err)
	}
	info, err := os.Stat(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal("unsafe key file mode")
	}
	dir, err := os.Stat(filepath.Dir(store.Path))
	if err != nil {
		t.Fatal(err)
	}
	if dir.Mode().Perm() != 0700 {
		t.Fatal("unsafe SSH directory mode")
	}
	refused := errors.New("refused")
	changed, err = store.Update(func([]byte) ([]byte, error) { return nil, refused })
	if changed || !errors.Is(err, refused) {
		t.Fatalf("refusal: %v %v", changed, err)
	}
	data, err := store.Read()
	if err != nil || string(data) != string(original) {
		t.Fatal("refusal changed key file")
	}
	// A separately opened descriptor must fail immediately while an edit owns the
	// directory, even after the authorized_keys inode has been replaced.
	err = store.WithLock(func() error {
		called := false
		if err := store.WithLock(func() error { called = true; return nil }); err == nil || called {
			t.Fatal("concurrent access bypassed lock")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestStoreRefusesSymlinkAndDirectory(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	store := Store{Path: filepath.Join(dir, "authorized_keys")}
	if err := os.Symlink(target, store.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(); err == nil {
		t.Fatal("read followed symlink")
	}
	if changed, err := store.Update(func([]byte) ([]byte, error) { t.Fatal("edit called for symlink"); return nil, nil }); changed || err == nil {
		t.Fatal("updated symlink")
	}
	store.Path = dir
	if _, err := store.Read(); err == nil {
		t.Fatal("accepted directory as keys")
	}
}

func TestParseSupportedAlgorithms(t *testing.T) {
	ed := ed25519.NewKeyFromSeed(make([]byte, 32)).Public().(ed25519.PublicKey)
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var keys []ssh.PublicKey
	for _, public := range []any{ed, rsaKey.Public()} {
		key, err := ssh.NewPublicKey(public)
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key)
	}
	var p256Point []byte
	for _, tc := range []struct {
		curve elliptic.Curve
		size  int
	}{{elliptic.P256(), 32}, {elliptic.P384(), 48}, {elliptic.P521(), 66}} {
		// Fixed scalars keep public fixtures deterministic without raw coordinates.
		scalar := make([]byte, tc.size)
		scalar[len(scalar)-1] = 1
		private, err := ecdsa.ParseRawPrivateKey(tc.curve, scalar)
		if err != nil {
			t.Fatal(err)
		}
		public := private.Public().(*ecdsa.PublicKey)
		key, err := ssh.NewPublicKey(public)
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key)
		if tc.curve == elliptic.P256() {
			p256Point, err = public.Bytes()
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, key := range keys {
		if parsed, err := Parse(strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))); err != nil || parsed.Type != key.Type() {
			t.Fatalf("%s: %v", key.Type(), err)
		}
	}
	skEd := ssh.Marshal(struct {
		Type        string
		Key         []byte
		Application string
	}{"sk-ssh-ed25519@openssh.com", ed, "ssh:"})
	skEC := ssh.Marshal(struct {
		Type, Curve string
		Key         []byte
		Application string
	}{"sk-ecdsa-sha2-nistp256@openssh.com", "nistp256", p256Point, "ssh:"})
	for _, data := range [][]byte{skEd, skEC} {
		key, err := ssh.ParsePublicKey(data)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Parse(key.Type() + " " + base64.StdEncoding.EncodeToString(data)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestParseRSAParametersAndEncoding(t *testing.T) {
	// These boundary fixtures test structural acceptance, not login capability.
	modulus := func(bits uint) *big.Int {
		return new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), bits), big.NewInt(1))
	}
	for _, tc := range []struct {
		name string
		n    *big.Int
		e    int64
		want bool
	}{
		{"zero", big.NewInt(0), 65537, false},
		{"negative", new(big.Int).Neg(modulus(2048)), 65537, false},
		{"even", new(big.Int).Sub(modulus(2048), big.NewInt(1)), 65537, false},
		{"below minimum", modulus(1023), 65537, false},
		{"minimum", modulus(1024), 65537, true},
		{"maximum", modulus(16384), 65537, true},
		{"above maximum", modulus(16385), 65537, false},
		{"invalid exponent", modulus(2048), 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := ssh.Marshal(struct {
				Type string
				E, N *big.Int
			}{ssh.KeyAlgoRSA, big.NewInt(tc.e), tc.n})
			line := ssh.KeyAlgoRSA + " " + base64.StdEncoding.EncodeToString(data)
			_, err := Parse(line)
			if (err == nil) != tc.want {
				t.Fatalf("accepted=%v, want %v; error=%v", err == nil, tc.want, err)
			}
		})
	}
	// Extra leading zeros must not hide an oversized wire integer behind valid bit length.
	for _, padding := range []int{2, 2049} {
		data := ssh.Marshal(struct {
			Type string
			E    *big.Int
			N    []byte
		}{ssh.KeyAlgoRSA, big.NewInt(65537), append(make([]byte, padding), modulus(2048).Bytes()...)})
		line := ssh.KeyAlgoRSA + " " + base64.StdEncoding.EncodeToString(data)
		if _, err := Parse(line); err == nil {
			t.Fatal("accepted redundantly padded RSA key")
		}
	}
}
