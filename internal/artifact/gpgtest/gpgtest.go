// Package gpgtest builds throwaway OpenPGP keys and signatures with the real
// gpg program. It is imported only by tests.
package gpgtest

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Home is one private gpg keyring holding the secret keys of a test.
type Home struct {
	t   *testing.T
	dir string
}

// NewHome fails the test when gpg is missing: a skipped signature test would
// look like a pass.
func NewHome(t *testing.T) *Home {
	t.Helper()
	if _, err := exec.LookPath("gpg"); err != nil {
		t.Fatalf("these tests need the real gpg program: %v", err)
	}
	// gpg-agent's socket path has a short length limit, so the keyring sits
	// directly under the system temporary directory.
	dir, err := os.MkdirTemp("", "vpn-gpg-")
	if err != nil {
		t.Fatal(err)
	}
	h := &Home{t: t, dir: dir}
	t.Cleanup(func() {
		_ = exec.Command("gpgconf", "--homedir", dir, "--kill", "gpg-agent").Run()
		_ = os.RemoveAll(dir)
	})
	return h
}

func (h *Home) gpg(args ...string) string {
	h.t.Helper()
	return h.run("", append([]string{"--batch"}, args...)...)
}

// answer drives one of gpg's question-and-answer commands, which refuse to
// run in batch mode.
func (h *Home) answer(answers string, args ...string) string {
	h.t.Helper()
	return h.run(answers, append([]string{"--command-fd", "0"}, args...)...)
}

func (h *Home) run(stdin string, args ...string) string {
	h.t.Helper()
	base := []string{"--homedir", h.dir, "--no-tty", "--pinentry-mode", "loopback", "--passphrase", ""}
	cmd := exec.Command("gpg", append(base, args...)...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		h.t.Fatalf("gpg %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return out.String()
}

// fingerprints lists the primary fingerprint first, then each subkey in the
// order gpg stores them.
func (h *Home) fingerprints(name string) []string {
	h.t.Helper()
	var out []string
	for _, line := range strings.Split(h.gpg("--with-colons", "--list-keys", name), "\n") {
		if f := strings.Split(line, ":"); f[0] == "fpr" && len(f) > 9 {
			out = append(out, f[9])
		}
	}
	if len(out) == 0 {
		h.t.Fatalf("no key named %s", name)
	}
	return out
}

// Past is a moment two years before these tests were written. A key or subkey
// made then with a one-year life has expired by the time a test checks it.
const Past = "20241001T000000"

// at turns a moment into gpg's option for pretending it is that time.
func at(when string) []string {
	if when == "" {
		return nil
	}
	return []string{"--faked-system-time", when}
}

// Key makes a key that signs with its primary key and returns the primary
// fingerprint. An empty when means now.
func (h *Home) Key(name, expire, when string) string {
	h.t.Helper()
	h.gpg(append(at(when), "--quick-generate-key", name+" <"+name+"@test.invalid>", "ed25519", "sign,cert", expire)...)
	return h.fingerprints(name)[0]
}

// Master makes a key whose primary key only certifies, as a master key kept
// away from the signing machine does. It never expires.
func (h *Home) Master(name, when string) string {
	h.t.Helper()
	h.gpg(append(at(when), "--quick-generate-key", name+" <"+name+"@test.invalid>", "ed25519", "cert", "never")...)
	return h.fingerprints(name)[0]
}

// Subkey adds a signing subkey and returns its fingerprint.
func (h *Home) Subkey(name, expire, when string) string {
	h.t.Helper()
	fprs := h.fingerprints(name)
	h.gpg(append(at(when), "--quick-add-key", fprs[0], "ed25519", "sign", expire)...)
	fprs = h.fingerprints(name)
	return fprs[len(fprs)-1]
}

// Expire gives the whole key a life counted from the moment when.
func (h *Home) Expire(name, expire, when string) {
	h.t.Helper()
	h.gpg(append(at(when), "--quick-set-expire", h.fingerprints(name)[0], expire)...)
}

// Revocation reasons as gpg numbers them.
const (
	Compromised = "1"
	Superseded  = "2"
)

// RevokeKey revokes the whole key.
func (h *Home) RevokeKey(name, reason string) {
	h.t.Helper()
	cert := h.answer("y\n"+reason+"\n\ny\n", "--armor", "--gen-revoke", h.fingerprints(name)[0])
	path := filepath.Join(h.dir, "revocation-"+name+".asc")
	if err := os.WriteFile(path, []byte(cert), 0600); err != nil {
		h.t.Fatal(err)
	}
	h.gpg("--import", path)
}

// RevokeSubkey revokes one subkey by fingerprint.
func (h *Home) RevokeSubkey(name, subkey string) {
	h.t.Helper()
	fprs := h.fingerprints(name)
	for i, f := range fprs[1:] {
		if f == subkey {
			h.answer(fmt.Sprintf("key %d\nrevkey\ny\n%s\n\ny\nsave\n", i+1, Compromised), "--edit-key", fprs[0])
			return
		}
	}
	h.t.Fatalf("%s has no subkey %s", name, subkey)
}

// Export writes the public key as it stands now and returns the file.
func (h *Home) Export(name, file string) string {
	h.t.Helper()
	path := filepath.Join(h.dir, file)
	if err := os.WriteFile(path, []byte(h.gpg("--armor", "--export", h.fingerprints(name)[0])), 0600); err != nil {
		h.t.Fatal(err)
	}
	return path
}

// File writes data into the keyring directory and returns the path.
func (h *Home) File(name, data string) string {
	h.t.Helper()
	path := filepath.Join(h.dir, name)
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		h.t.Fatal(err)
	}
	return path
}

// Sign makes a detached signature with exactly the named key or subkey
// fingerprint and returns the signature file.
func (h *Home) Sign(fingerprint, when, data, file string) string {
	h.t.Helper()
	sig := filepath.Join(h.dir, file)
	h.gpg(append(at(when), "--armor", "--local-user", fingerprint+"!", "--detach-sign", "--output", sig, data)...)
	return sig
}

// SignExpiring makes a detached signature that is itself only valid for the
// given time after when.
func (h *Home) SignExpiring(fingerprint, when, life, data, file string) string {
	h.t.Helper()
	sig := filepath.Join(h.dir, file)
	h.gpg(append(at(when), "--default-sig-expire", life, "--armor", "--local-user", fingerprint+"!", "--detach-sign", "--output", sig, data)...)
	return sig
}

// Clearsign makes one file holding the data and its signature.
func (h *Home) Clearsign(fingerprint, data, file string) string {
	h.t.Helper()
	out := filepath.Join(h.dir, file)
	h.gpg("--armor", "--local-user", fingerprint+"!", "--clearsign", "--output", out, data)
	return out
}

// Join concatenates signature files, as a release signed by several people
// does.
func (h *Home) Join(file string, parts ...string) string {
	h.t.Helper()
	var all []byte
	for _, p := range parts {
		b, err := os.ReadFile(p)
		if err != nil {
			h.t.Fatal(err)
		}
		all = append(all, b...)
	}
	path := filepath.Join(h.dir, file)
	if err := os.WriteFile(path, all, 0600); err != nil {
		h.t.Fatal(err)
	}
	return path
}

// WithoutUserID rewrites an exported public key without its name and email
// and the signatures on them, which is how a key server serves a key whose
// address was never confirmed.
func (h *Home) WithoutUserID(keyFile, file string) string {
	h.t.Helper()
	raw := []byte(h.gpg("--dearmor", "--output", "-", keyFile))
	var out []byte
	drop, dropped := false, false
	for len(raw) > 0 {
		tag, size, err := packet(raw)
		if err != nil {
			h.t.Fatal(err)
		}
		switch tag {
		case 13, 17: // user ID, user attribute
			drop = true
		case 2: // signature: belongs to whatever came before it
		default:
			drop = false
		}
		if !drop {
			out = append(out, raw[:size]...)
		}
		dropped = dropped || drop
		raw = raw[size:]
	}
	if !dropped {
		h.t.Fatalf("%s has no user ID to remove", keyFile)
	}
	path := filepath.Join(h.dir, file)
	if err := os.WriteFile(path, out, 0600); err != nil {
		h.t.Fatal(err)
	}
	return path
}

// packet returns the tag and total size of the OpenPGP packet at the start of
// b (RFC 4880, section 4.2). Partial body lengths do not occur in keys.
func packet(b []byte) (tag, size int, err error) {
	bad := errors.New("unsupported OpenPGP packet header")
	if len(b) < 2 || b[0]&0x80 == 0 {
		return 0, 0, bad
	}
	var header, body int
	if b[0]&0x40 == 0 {
		tag = int(b[0]&0x3c) >> 2
		switch b[0] & 3 {
		case 0:
			header, body = 2, int(b[1])
		case 1:
			if len(b) < 3 {
				return 0, 0, bad
			}
			header, body = 3, int(b[1])<<8|int(b[2])
		case 2:
			if len(b) < 5 {
				return 0, 0, bad
			}
			header, body = 5, int(b[1])<<24|int(b[2])<<16|int(b[3])<<8|int(b[4])
		default:
			return 0, 0, bad
		}
	} else {
		tag = int(b[0] & 0x3f)
		switch {
		case b[1] < 192:
			header, body = 2, int(b[1])
		case b[1] < 224:
			if len(b) < 3 {
				return 0, 0, bad
			}
			header, body = 3, (int(b[1])-192)<<8+int(b[2])+192
		case b[1] == 255:
			if len(b) < 6 {
				return 0, 0, bad
			}
			header, body = 6, int(b[2])<<24|int(b[3])<<16|int(b[4])<<8|int(b[5])
		default:
			return 0, 0, bad
		}
	}
	if header+body > len(b) {
		return 0, 0, bad
	}
	return tag, header + body, nil
}
