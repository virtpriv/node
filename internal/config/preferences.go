package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const PreferencesSchema = 1

type Preferences struct {
	Schema int    `json:"schema"`
	Theme  string `json:"theme"`
}

type PreferencesStore struct {
	Dir  string
	Path string
}

func DefaultPreferences() *Preferences {
	return &Preferences{Schema: PreferencesSchema, Theme: "dark"}
}

func DefaultPreferencesStore() (*PreferencesStore, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("resolve user config directory: %w", err)
	}
	dir = filepath.Join(dir, "vpn")
	return &PreferencesStore{Dir: dir, Path: filepath.Join(dir, "preferences.json")}, nil
}

func (p *Preferences) Validate() error {
	if p.Schema != PreferencesSchema {
		return fmt.Errorf("unsupported preferences schema %d", p.Schema)
	}
	if p.Theme != "dark" && p.Theme != "light" {
		return fmt.Errorf("unknown theme %q", p.Theme)
	}
	return nil
}

var preferenceFields = map[string]bool{"schema": true, "theme": true}

func (s *PreferencesStore) Load() (*Preferences, error) {
	info, err := os.Lstat(s.Path)
	if os.IsNotExist(err) {
		return DefaultPreferences(), nil
	}
	if err != nil {
		return nil, err
	}
	if err := validateObject(s.Dir, true, 0o700,
		os.Geteuid(), os.Getegid()); err != nil {
		return nil, fmt.Errorf("preferences directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("preferences file %s is not a regular file", s.Path)
	}
	if info.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("preferences file %s mode is %04o, want 0600", s.Path, info.Mode().Perm())
	}
	uid, gid, err := fileOwner(info)
	if err != nil {
		return nil, err
	}
	if uid != os.Geteuid() || gid != os.Getegid() {
		return nil, fmt.Errorf(
			"preferences file %s owner is %d:%d, want %d:%d",
			s.Path, uid, gid, os.Geteuid(), os.Getegid())
	}
	data, err := os.ReadFile(s.Path)
	if err != nil {
		return nil, err
	}
	if _, err := decodeObject(data, preferenceFields, false); err != nil {
		return nil, fmt.Errorf("preferences: %w", err)
	}
	var prefs Preferences
	if err := json.Unmarshal(data, &prefs); err != nil {
		return nil, fmt.Errorf("preferences: %w", err)
	}
	if err := prefs.Validate(); err != nil {
		return nil, fmt.Errorf("preferences: %w", err)
	}
	return &prefs, nil
}

func (s *PreferencesStore) Save(prefs *Preferences) error {
	if err := prefs.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(s.Dir)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("preferences directory %s is not a real directory", s.Dir)
	}
	uid, gid, err := fileOwner(info)
	if err != nil {
		return err
	}
	if uid != os.Geteuid() || gid != os.Getegid() {
		return fmt.Errorf(
			"preferences directory %s owner is %d:%d, want %d:%d",
			s.Dir, uid, gid, os.Geteuid(), os.Getegid())
	}
	if err := os.Chmod(s.Dir, 0o700); err != nil {
		return err
	}
	if info, err := os.Lstat(s.Path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("preferences destination %s is not a regular file", s.Path)
		}
		if info.Mode().Perm() != 0o600 {
			return fmt.Errorf("preferences destination %s mode is %04o, want 0600",
				s.Path, info.Mode().Perm())
		}
		uid, gid, ownErr := fileOwner(info)
		if ownErr != nil {
			return ownErr
		}
		if uid != os.Geteuid() || gid != os.Getegid() {
			return fmt.Errorf(
				"preferences destination %s owner is %d:%d, want %d:%d",
				s.Path, uid, gid, os.Geteuid(), os.Getegid())
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	data, err := json.MarshalIndent(prefs, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(s.Dir, ".preferences-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, s.Path); err != nil {
		return err
	}
	dir, err := os.Open(s.Dir)
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil && !errors.Is(err, os.ErrInvalid) {
		return err
	}
	return nil
}

func LoadPreferences() (*Preferences, error) {
	store, err := DefaultPreferencesStore()
	if err != nil {
		return nil, err
	}
	return store.Load()
}

func SavePreferences(prefs *Preferences) error {
	store, err := DefaultPreferencesStore()
	if err != nil {
		return err
	}
	return store.Save(prefs)
}
