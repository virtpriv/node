// Package sshkeys parses supported public keys and stores the operator's authorized_keys.
package sshkeys

import (
	"bytes"
	"crypto/rsa"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
)

type Key struct {
	Type        string
	Fingerprint string
	Comment     string
	RawLine     string
}

// RecognizedType includes obsolete types so account inspection can report exclusions.
func RecognizedType(t string) bool {
	switch t {
	case "ssh-rsa", "ssh-ed25519", "ssh-dss", "ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521", "sk-ssh-ed25519@openssh.com", "sk-ecdsa-sha2-nistp256@openssh.com":
		return true
	}
	return false
}

// Parse accepts one bare public key. Options, certificates and multiple lines are unsupported.
func Parse(line string) (Key, error) {
	// Accept a single line terminator and OpenSSH's space/tab separators.
	// Unicode whitespace must not make an unusable stored line count as a key.
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSuffix(line, "\r")
	line = strings.Trim(line, " \t")
	// sshd(8) limits an authorized_keys line to 8 KiB.
	if len(line) > 8<<10 {
		return Key{}, errors.New("public key line exceeds the 8 KiB OpenSSH limit")
	}
	if strings.ContainsAny(line, "\r\n") {
		return Key{}, errors.New("enter one public key")
	}
	fields := strings.FieldsFunc(line, func(r rune) bool { return r == ' ' || r == '\t' })
	if len(fields) < 2 {
		return Key{}, errors.New("public key needs a type and base64 data")
	}
	if !RecognizedType(fields[0]) || fields[0] == "ssh-dss" {
		return Key{}, errors.New("unsupported SSH key type")
	}
	data, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil {
		return Key{}, errors.New("invalid public key encoding")
	}
	key, err := ssh.ParsePublicKey(data)
	if err != nil {
		return Key{}, errors.New("invalid public key data")
	}
	if key.Type() != fields[0] {
		return Key{}, errors.New("public key type does not match its data")
	}
	if key.Type() == ssh.KeyAlgoRSA {
		// ParsePublicKey checks the RSA exponent but not the modulus. Match
		// OpenSSH's 1024-bit minimum and 16384-bit wire limit; RSA needs an odd N.
		public := key.(ssh.CryptoPublicKey).CryptoPublicKey().(*rsa.PublicKey)
		if public.N.Sign() <= 0 || public.N.Bit(0) == 0 || public.N.BitLen() < 1024 || public.N.BitLen() > 16384 {
			return Key{}, errors.New("invalid RSA modulus; require a positive odd value of 1024 to 16384 bits")
		}
		// RFC 4251 forbids redundant integer padding. Otherwise a numerically
		// small value could carry an encoding larger than OpenSSH can read.
		if !bytes.Equal(data, key.Marshal()) {
			return Key{}, errors.New("noncanonical RSA public key encoding")
		}
	}
	return Key{Type: key.Type(), Fingerprint: ssh.FingerprintSHA256(key), Comment: strings.Join(fields[2:], " "), RawLine: line}, nil
}

// Keys returns distinct supported credentials. Unmanaged lines are preserved by edits.
func Keys(content []byte) []Key {
	var keys []Key
	seen := make(map[string]bool)
	for _, line := range strings.Split(string(content), "\n") {
		key, err := Parse(line)
		if err == nil && !seen[key.Fingerprint] {
			seen[key.Fingerprint] = true
			keys = append(keys, key)
		}
	}
	return keys
}

// Store uses the same directory lock for operator edits and the helper's disable guard.
// Locking the directory keeps the lock stable when authorized_keys is atomically replaced.
// External editors do not participate in this coordination.
type Store struct{ Path string }

func (s Store) Read() ([]byte, error) {
	f, err := os.OpenFile(s.Path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read authorized_keys: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("authorized_keys must be a regular file")
	}
	return io.ReadAll(f)
}

// WithLock refuses contention instead of waiting while the serialized helper might
// itself be needed by the current editor to read effective password authentication.
func (s Store) WithLock(action func() error) error {
	f, err := os.OpenFile(filepath.Dir(s.Path), os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open SSH directory: %w", err)
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return fmt.Errorf("SSH access is busy or cannot be locked; retry after checking current state: %w", err)
	}
	defer unix.Flock(int(f.Fd()), unix.LOCK_UN)
	return action()
}

// Update evaluates the change while locked. changed is true once rename succeeds,
// even if syncing the directory then fails. Runtime callers retain their own UID.
func (s Store) Update(edit func([]byte) ([]byte, error)) (changed bool, err error) {
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return false, err
	}
	err = s.WithLock(func() error {
		before, err := s.Read()
		if err != nil {
			return err
		}
		after, err := edit(before)
		if err != nil {
			return err
		}
		f, err := os.CreateTemp(dir, ".authorized_keys.tmp-")
		if err != nil {
			return err
		}
		defer os.Remove(f.Name())
		defer f.Close()
		if _, err = f.Write(after); err != nil {
			return err
		}
		if err = f.Chmod(0600); err != nil {
			return err
		}
		if err = f.Sync(); err != nil {
			return err
		}
		if err = f.Close(); err != nil {
			return err
		}
		if err = os.Rename(f.Name(), s.Path); err != nil {
			return err
		}
		changed = true
		parent, err := os.Open(dir)
		if err != nil {
			return err
		}
		defer parent.Close()
		return parent.Sync()
	})
	return changed, err
}
