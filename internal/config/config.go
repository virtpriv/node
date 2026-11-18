// internal/config/config.go

package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/virtpriv/node/internal/paths"
)

const (
	DefaultDir  = paths.ConfigDir
	DefaultPath = paths.ConfigFile

	// SystemSchema is 2 since Bitcoin Core and LND left. A file with the
	// number 1 belongs to a Bitcoin and Lightning node and is refused.
	SystemSchema = 2
)

// AppConfig is the root-owned, non-secret desired system configuration.
// It deliberately contains no live daemon observations, credentials,
// installer lifecycle state, security-workflow markers, or TUI preferences.
// This release has nothing to configure, so the file holds its schema
// number alone. The install writes it once every step is recorded, which is
// how the TUI and the update tell a finished install.
type AppConfig struct {
	Schema int `json:"schema"`
}

// Store reads and atomically publishes one system-configuration file. The
// production store requires root for writes and resolves the vpn group at the
// moment of use. Tests use explicit numeric ownership under an isolated path.
type Store struct {
	Dir              string
	Path             string
	OwnerUID         int
	GroupGID         int
	GroupName        string
	RequireRootWrite bool
}

func DefaultStore() *Store {
	return &Store{
		Dir:              DefaultDir,
		Path:             DefaultPath,
		OwnerUID:         0,
		GroupName:        paths.AdminUser,
		RequireRootWrite: true,
	}
}

func Default() *AppConfig {
	return &AppConfig{Schema: SystemSchema}
}

func (s *Store) ownership() (int, int, error) {
	if s.GroupName == "" {
		return s.OwnerUID, s.GroupGID, nil
	}
	g, err := user.LookupGroup(s.GroupName)
	if err != nil {
		return 0, 0, fmt.Errorf("resolve group %q: %w", s.GroupName, err)
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return 0, 0, fmt.Errorf("group %q has non-numeric gid %q", s.GroupName, g.Gid)
	}
	return s.OwnerUID, gid, nil
}

func fileOwner(info os.FileInfo) (int, int, error) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, errors.New("filesystem does not expose numeric ownership")
	}
	return int(st.Uid), int(st.Gid), nil
}

func validateObject(path string, wantDir bool, mode os.FileMode, uid, gid int) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink", path)
	}
	if wantDir {
		if !info.IsDir() {
			return fmt.Errorf("%s is not a directory", path)
		}
	} else if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	if info.Mode().Perm() != mode {
		return fmt.Errorf("%s mode is %04o, want %04o", path, info.Mode().Perm(), mode)
	}
	gotUID, gotGID, err := fileOwner(info)
	if err != nil {
		return fmt.Errorf("stat ownership for %s: %w", path, err)
	}
	if gotUID != uid || gotGID != gid {
		return fmt.Errorf("%s owner is %d:%d, want %d:%d", path, gotUID, gotGID, uid, gid)
	}
	return nil
}

// systemFields are the fields this release knows. Each must be present. A
// field written by a later release is read and ignored, so an older vpn still
// running during an update can read the newer file. A later field that older
// code must obey raises the schema number instead, and older code refuses the
// file.
var systemFields = map[string]bool{
	"schema": true,
}

// decodeObject reads one JSON object whose known fields are all present,
// unique and not null. With readLater, a field this release does not know is
// read and ignored. Without it, such a field is refused.
func decodeObject(data []byte, allowed map[string]bool, readLater bool) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, errors.New("top-level JSON value must be an object")
	}
	fields := make(map[string]json.RawMessage, len(allowed))
	seen := make(map[string]bool, len(allowed))
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		name, ok := tok.(string)
		if !ok {
			return nil, errors.New("object key is not a string")
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate field %q", name)
		}
		seen[name] = true
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		if allowed[name] {
			// A known field always holds a value. JSON null would read as
			// empty or false.
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				return nil, fmt.Errorf("field %q is null", name)
			}
			fields[name] = raw
		} else if !readLater {
			return nil, fmt.Errorf("unknown field %q", name)
		}
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("multiple top-level JSON values")
		}
		return nil, err
	}
	for name := range allowed {
		if _, ok := fields[name]; !ok {
			return nil, fmt.Errorf("missing field %q", name)
		}
	}
	return fields, nil
}

func decodeSystem(data []byte) (*AppConfig, error) {
	fields, err := decodeObject(data, systemFields, true)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	// Decode the known fields only. Go matches names without regard to case,
	// so a later name such as "Schema" would otherwise set schema.
	known, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	var cfg AppConfig
	if err := json.Unmarshal(known, &cfg); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return &cfg, nil
}

func (c *AppConfig) Validate() error {
	if c.Schema != SystemSchema {
		return fmt.Errorf("unsupported schema %d", c.Schema)
	}
	return nil
}

func (s *Store) Load() (*AppConfig, error) {
	uid, gid, err := s.ownership()
	if err != nil {
		return nil, err
	}
	if err := validateObject(s.Dir, true, 0o750, uid, gid); err != nil {
		return nil, fmt.Errorf("config directory: %w", err)
	}
	if err := validateObject(s.Path, false, 0o640, uid, gid); err != nil {
		return nil, fmt.Errorf("config file: %w", err)
	}
	data, err := os.ReadFile(s.Path)
	if err != nil {
		return nil, err
	}
	return decodeSystem(data)
}

// Save validates and atomically publishes the desired system configuration.
// The production store can only be written by root. The parent is verified as
// a real root-controlled directory, the existing destination (if any) must be
// the expected regular file, and the replacement is synchronized before and
// after rename.
func (s *Store) Save(cfg *AppConfig) error {
	if s.RequireRootWrite && os.Geteuid() != 0 {
		return errors.New("system configuration may only be written by root")
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("refuse invalid config: %w", err)
	}
	uid, gid, err := s.ownership()
	if err != nil {
		return err
	}
	created := false
	if err := os.Mkdir(s.Dir, 0o750); err != nil {
		if !os.IsExist(err) {
			return fmt.Errorf("create config directory: %w", err)
		}
	} else {
		created = true
	}
	if created {
		if err := os.Chown(s.Dir, uid, gid); err != nil {
			return fmt.Errorf("chown config directory: %w", err)
		}
	}
	info, err := os.Lstat(s.Dir)
	if err != nil {
		return fmt.Errorf("stat config directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("config directory %s is not a real directory", s.Dir)
	}
	gotUID, gotGID, err := fileOwner(info)
	if err != nil {
		return err
	}
	if gotUID != uid || gotGID != gid {
		return fmt.Errorf("config directory %s owner is %d:%d, want %d:%d", s.Dir, gotUID, gotGID, uid, gid)
	}
	if err := os.Chmod(s.Dir, 0o750); err != nil {
		return fmt.Errorf("chmod config directory: %w", err)
	}
	if existing, err := os.Lstat(s.Path); err == nil {
		if existing.Mode()&os.ModeSymlink != 0 || !existing.Mode().IsRegular() {
			return fmt.Errorf("config destination %s is not a regular file", s.Path)
		}
		if existing.Mode().Perm() != 0o640 {
			return fmt.Errorf("config destination %s mode is %04o, want 0640", s.Path, existing.Mode().Perm())
		}
		existingUID, existingGID, ownErr := fileOwner(existing)
		if ownErr != nil {
			return ownErr
		}
		if existingUID != uid || existingGID != gid {
			return fmt.Errorf("config destination %s owner is %d:%d, want %d:%d", s.Path, existingUID, existingGID, uid, gid)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat config destination: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(s.Dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	fail := func(stage string, err error) error {
		tmp.Close()
		return fmt.Errorf("%s temp config: %w", stage, err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail("write", err)
	}
	if err := tmp.Chmod(0o640); err != nil {
		return fail("chmod", err)
	}
	if err := tmp.Chown(uid, gid); err != nil {
		return fail("chown", err)
	}
	if err := tmp.Sync(); err != nil {
		return fail("sync", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}
	if err := os.Rename(tmpPath, s.Path); err != nil {
		return fmt.Errorf("publish config: %w", err)
	}
	dir, err := os.Open(filepath.Clean(s.Dir))
	if err != nil {
		return fmt.Errorf("open config directory for sync: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync config directory: %w", err)
	}
	return nil
}

func Load() (*AppConfig, error) { return DefaultStore().Load() }
func Save(cfg *AppConfig) error { return DefaultStore().Save(cfg) }
