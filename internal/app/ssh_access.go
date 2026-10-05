package app

import (
	"errors"
	"fmt"
	"strings"

	"github.com/virtpriv/node/internal/helper"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/sshkeys"
)

type SSHKey = sshkeys.Key

// SSHAuth is the narrow privileged boundary. Key files are edited as the operator.
type SSHAuth interface {
	PasswordAuth() (bool, error)
	SetPasswordAuthDisabled(disabled bool) error
}

type SSHAccess struct {
	Keys sshkeys.Store
	Auth SSHAuth
}

func NewSSHAccess() *SSHAccess {
	return &SSHAccess{Keys: sshkeys.Store{Path: paths.AuthorizedKeysFile}, Auth: helperSSHAuth{}}
}

type helperSSHAuth struct{}

func (helperSSHAuth) PasswordAuth() (bool, error) {
	var result helper.SSHAuthResult
	err := helper.Call(helper.VerbReadSSHAuth, nil, &result)
	return result.PasswordAuthEnabled, err
}
func (helperSSHAuth) SetPasswordAuthDisabled(disabled bool) error {
	return helper.Call(helper.VerbRebuildSSHConfig, helper.RebuildSSHConfigParams{PasswordAuthDisabled: disabled}, nil)
}

func ParseSSHKey(line string) (SSHKey, error) { return sshkeys.Parse(line) }
func (s *SSHAccess) ListKeys() ([]SSHKey, error) {
	data, err := s.Keys.Read()
	if err != nil {
		return nil, err
	}
	return sshkeys.Keys(data), nil
}

func (s *SSHAccess) AddKey(line string) error {
	key, err := sshkeys.Parse(line)
	if err != nil {
		return err
	}
	changed, err := s.Keys.Update(func(data []byte) ([]byte, error) {
		for _, existing := range sshkeys.Keys(data) {
			if existing.Fingerprint == key.Fingerprint {
				return nil, errors.New("this SSH key is already configured")
			}
		}
		content := string(data)
		if content != "" && !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		return []byte(content + key.RawLine + "\n"), nil
	})
	return keyWriteError(changed, err)
}

// RemoveKey evaluates the current file and password setting at apply time. All
// copies of the selected fingerprint are removed; duplicates are one credential.
func (s *SSHAccess) RemoveKey(fingerprint string) error {
	changed, err := s.Keys.Update(func(data []byte) ([]byte, error) {
		keys := sshkeys.Keys(data)
		found := false
		for _, key := range keys {
			if key.Fingerprint == fingerprint {
				found = true
			}
		}
		if !found {
			return nil, errors.New("this SSH key is no longer present; refresh the key list")
		}
		if len(keys) == 1 {
			enabled, err := s.Auth.PasswordAuth()
			if err != nil {
				return nil, fmt.Errorf("cannot verify password authentication; keep the last SSH key: %w", err)
			}
			if !enabled {
				return nil, errors.New("cannot remove the last SSH key while password authentication is disabled")
			}
		}
		var kept []string
		for _, line := range strings.Split(string(data), "\n") {
			key, err := sshkeys.Parse(line)
			if err == nil && key.Fingerprint == fingerprint {
				continue
			}
			kept = append(kept, line)
		}
		return []byte(strings.Join(kept, "\n")), nil
	})
	return keyWriteError(changed, err)
}

func keyWriteError(changed bool, err error) error {
	if changed && err != nil {
		return fmt.Errorf("key file changed, but durability could not be confirmed; refresh before retrying: %w", err)
	}
	return err
}

func (s *SSHAccess) PasswordAuth() (bool, error) { return s.Auth.PasswordAuth() }
func (s *SSHAccess) SetPasswordAuthDisabled(disabled bool) error {
	if err := s.Auth.SetPasswordAuthDisabled(disabled); err != nil {
		return fmt.Errorf("SSH password authentication was not confirmed; keep this session open and check the current setting before retrying: %w", err)
	}
	return nil
}
