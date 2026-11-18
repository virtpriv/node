package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "etc-vpn")
	return &Store{
		Dir:      dir,
		Path:     filepath.Join(dir, "config.json"),
		OwnerUID: os.Geteuid(),
		GroupGID: os.Getegid(),
	}
}

func TestDecodeSystemStrict(t *testing.T) {
	cfg, err := decodeSystem([]byte(`{"schema":2}`))
	if err != nil {
		t.Fatalf("valid config: %v", err)
	}
	if cfg.Schema != SystemSchema {
		t.Fatalf("decoded wrong values: %+v", cfg)
	}
	// The last one is the file a Bitcoin and Lightning node holds. This
	// release refuses it by its number.
	cases := map[string]string{
		"malformed":              `{`,
		"not object":             `[]`,
		"empty object":           `{}`,
		"duplicate":              `{"schema":2,"schema":2}`,
		"duplicate later field":  `{"schema":2,"later":1,"later":2}`,
		"earlier schema":         `{"schema":1}`,
		"future schema":          `{"schema":3}`,
		"null schema":            `{"schema":null}`,
		"wrong schema type":      `{"schema":"2"}`,
		"trailing value":         `{"schema":2} {}`,
		"an earlier node's file": `{"schema":1,"network":"mainnet","bitcoin":"core","lightning":"lnd","prune_size_gb":25,"db_cache_mb":512,"p2p_mode":"tor","p2p_address":"","auto_unlock_enabled":false,"syncthing_enabled":false}`,
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeSystem([]byte(input)); err == nil {
				t.Fatalf("accepted %s", input)
			}
		})
	}
}

// A later release may add fields this one does not know. An older vpn still
// running during an update must read that file, and validate what it knows.
func TestDecodeSystemReadsALaterFile(t *testing.T) {
	later := `{"schema":2,"later_name":"node","later_settings":{"list":[1,2],"on":true},"later_unset":null}`
	if _, err := decodeSystem([]byte(later)); err != nil {
		t.Fatalf("later file refused: %v", err)
	}
	// Go's JSON decoder matches names without regard to case. A later or
	// mistyped name that only differs in case must not reach a known field.
	shadow := strings.TrimSuffix(later, `}`) + `,"Schema":1}`
	if cfg, err := decodeSystem([]byte(shadow)); err != nil || cfg.Schema != SystemSchema {
		t.Fatalf("a name differing only in case changed a known field: %+v, %v", cfg, err)
	}
	for name, input := range map[string]string{
		"known field still required": `{"later_name":"node"}`,
		"schema still checked":       strings.Replace(later, `"schema":2`, `"schema":3`, 1),
		"later value still JSON":     strings.Replace(later, `"later_name":"node"`, `"later_name":node`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeSystem([]byte(input)); err == nil {
				t.Fatalf("accepted %s", input)
			}
		})
	}
}

func TestStoreSaveLoadAndMetadata(t *testing.T) {
	store := testStore(t)
	if err := store.Save(Default()); err != nil {
		t.Fatalf("save: %v", err)
	}
	for path, want := range map[string]os.FileMode{
		store.Dir: 0o750, store.Path: 0o640,
	} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s mode %04o, want %04o", path, info.Mode().Perm(), want)
		}
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if *loaded != *Default() {
		t.Fatalf("loaded wrong config: %+v", loaded)
	}
}

func TestStoreAtomicallyReplacesConfig(t *testing.T) {
	store := testStore(t)
	if err := store.Save(Default()); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(Default()); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(before, after) {
		t.Fatal("config inode was modified in place instead of replaced")
	}
	if loaded, err := store.Load(); err != nil {
		t.Fatalf("replacement not readable: %+v, %v", loaded, err)
	}
}

func TestStoreRefusesUnsafeObjects(t *testing.T) {
	t.Run("directory symlink", func(t *testing.T) {
		base := t.TempDir()
		realDir := filepath.Join(base, "real")
		if err := os.Mkdir(realDir, 0o750); err != nil {
			t.Fatal(err)
		}
		store := &Store{Dir: filepath.Join(base, "link"), Path: filepath.Join(base, "link", "config.json"), OwnerUID: os.Geteuid(), GroupGID: os.Getegid()}
		if err := os.Symlink(realDir, store.Dir); err != nil {
			t.Fatal(err)
		}
		if err := store.Save(Default()); err == nil {
			t.Fatal("saved through directory symlink")
		}
	})

	t.Run("destination symlink", func(t *testing.T) {
		store := testStore(t)
		if err := os.Mkdir(store.Dir, 0o750); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "target")
		if err := os.WriteFile(target, []byte("protected"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, store.Path); err != nil {
			t.Fatal(err)
		}
		if err := store.Save(Default()); err == nil {
			t.Fatal("replaced destination symlink")
		}
		data, _ := os.ReadFile(target)
		if string(data) != "protected" {
			t.Fatal("symlink target changed")
		}
	})

	t.Run("load wrong mode", func(t *testing.T) {
		store := testStore(t)
		if err := store.Save(Default()); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(store.Path, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Load(); err == nil {
			t.Fatal("loaded config with wrong mode")
		}
	})
}

func TestStoreRefusesInvalidConfigBeforeMutation(t *testing.T) {
	store := testStore(t)
	cfg := Default()
	cfg.Schema = 1
	if err := store.Save(cfg); err == nil {
		t.Fatal("saved invalid config")
	}
	if _, err := os.Lstat(store.Dir); !os.IsNotExist(err) {
		t.Fatalf("invalid save mutated filesystem: %v", err)
	}
}
