package config

import (
	"encoding/json"
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

func TestDefaultSystemConfig(t *testing.T) {
	cfg := Default()
	if cfg.Schema != SystemSchema || cfg.Network != "mainnet" ||
		cfg.PruneSize != 25 || cfg.DbCache != 512 ||
		cfg.P2PMode != "tor" || cfg.AutoUnlock || cfg.SyncthingEnabled {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if !cfg.HasLND() {
		t.Fatal("LND must be part of the base installation")
	}
}

func TestSystemConfigContainsOnlyDesiredNonSecretFields(t *testing.T) {
	data, err := json.Marshal(Default())
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) != len(systemFields) {
		t.Fatalf("serialized %d fields, want %d: %s", len(raw), len(systemFields), data)
	}
	for name := range systemFields {
		if _, ok := raw[name]; !ok {
			t.Errorf("missing desired field %q", name)
		}
	}
	for _, forbidden := range []string{
		"components", "lnd_installed", "wallet_created",
		"syncthing_installed", "syncthing_password",
		"syncthing_devices", "theme", "ssh_ports",
		"key_verification_pending", "ssh_password_auth_disabled",
	} {
		if _, ok := raw[forbidden]; ok {
			t.Errorf("authority-mixed field %q remains", forbidden)
		}
	}
}

func TestDecodeSystemStrict(t *testing.T) {
	valid := `{"schema":1,"network":"testnet4","prune_size_gb":25,"db_cache_mb":1024,"p2p_mode":"hybrid","p2p_address":"203.0.113.7","auto_unlock_enabled":true,"syncthing_enabled":false}`
	cfg, err := decodeSystem([]byte(valid))
	if err != nil {
		t.Fatalf("valid config: %v", err)
	}
	if cfg.Network != "testnet4" || cfg.DbCache != 1024 ||
		cfg.P2PMode != "hybrid" || cfg.P2PAddress != "203.0.113.7" ||
		!cfg.AutoUnlock || cfg.SyncthingEnabled {
		t.Fatalf("decoded wrong values: %+v", cfg)
	}
	tor := strings.Replace(strings.Replace(valid, `"hybrid"`, `"tor"`, 1), `"203.0.113.7"`, `""`, 1)
	if _, err := decodeSystem([]byte(tor)); err != nil {
		t.Fatalf("valid Tor config: %v", err)
	}

	withAddress := func(address string) string {
		return strings.Replace(valid, `"203.0.113.7"`, address, 1)
	}
	cases := map[string]string{
		"malformed":               `{`,
		"not object":              `[]`,
		"duplicate":               strings.Replace(valid, `"schema":1`, `"schema":1,"schema":1`, 1),
		"duplicate later field":   strings.TrimSuffix(valid, `}`) + `,"later":1,"later":2}`,
		"missing":                 strings.Replace(valid, `"network":"testnet4",`, ``, 1),
		"missing p2p address":     strings.Replace(valid, `"p2p_address":"203.0.113.7",`, ``, 1),
		"missing boolean":         strings.Replace(valid, `"auto_unlock_enabled":true,`, ``, 1),
		"future schema":           strings.Replace(valid, `"schema":1`, `"schema":2`, 1),
		"bad network":             strings.Replace(valid, `"testnet4"`, `"signet"`, 1),
		"bad p2p":                 strings.Replace(valid, `"hybrid"`, `"clearnet"`, 1),
		"bad db cache":            strings.Replace(valid, `1024`, `999`, 1),
		"zero prune":              strings.Replace(valid, `"prune_size_gb":25`, `"prune_size_gb":0`, 1),
		"trailing value":          valid + ` {}`,
		"wrong boolean type":      strings.Replace(valid, `"syncthing_enabled":false`, `"syncthing_enabled":"false"`, 1),
		"tor with an address":     strings.Replace(valid, `"hybrid"`, `"tor"`, 1),
		"hybrid without address":  withAddress(`""`),
		"hybrid private address":  withAddress(`"10.1.2.3"`),
		"hybrid loopback address": withAddress(`"127.0.0.1"`),
		"hybrid link local":       withAddress(`"169.254.1.2"`),
		"hybrid IPv6 address":     withAddress(`"2001:db8::7"`),
		"hybrid mapped IPv4":      withAddress(`"::ffff:203.0.113.7"`),
		"hybrid address and port": withAddress(`"203.0.113.7:9735"`),
		"hybrid malformed":        withAddress(`"203.0.113"`),
		"hybrid address type":     withAddress(`7`),
		"null address":            strings.Replace(tor, `"p2p_address":""`, `"p2p_address":null`, 1),
		"null boolean":            strings.Replace(valid, `"syncthing_enabled":false`, `"syncthing_enabled":null`, 1),
		"null network":            strings.Replace(valid, `"network":"testnet4"`, `"network":null`, 1),
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
	later := `{"schema":1,"network":"public-signet","prune_size_gb":25,"db_cache_mb":512,"p2p_mode":"hybrid","p2p_address":"203.0.113.7","auto_unlock_enabled":true,"syncthing_enabled":true,"lnd_alias":"node","later_settings":{"list":[1,2],"on":true},"later_unset":null}`
	cfg, err := decodeSystem([]byte(later))
	if err != nil {
		t.Fatalf("later file refused: %v", err)
	}
	if cfg.Network != NetworkPublicSignet || cfg.P2PMode != "hybrid" ||
		cfg.P2PAddress != "203.0.113.7" || !cfg.AutoUnlock || !cfg.SyncthingEnabled {
		t.Fatalf("decoded wrong values: %+v", cfg)
	}
	// Go's JSON decoder matches names without regard to case. A later or
	// mistyped name that only differs in case must not reach a known field.
	shadow := strings.TrimSuffix(later, `}`) + `,"P2P_Mode":"tor","P2P_ADDRESS":"","Network":"mainnet"}`
	if cfg, err := decodeSystem([]byte(shadow)); err != nil ||
		cfg.P2PMode != "hybrid" || cfg.P2PAddress != "203.0.113.7" || cfg.Network != NetworkPublicSignet {
		t.Fatalf("a name differing only in case changed a known field: %+v, %v", cfg, err)
	}
	for name, input := range map[string]string{
		"known field still validated": strings.Replace(later, `"db_cache_mb":512`, `"db_cache_mb":3`, 1),
		"known field still required":  strings.Replace(later, `"p2p_mode":"hybrid",`, ``, 1),
		"schema still checked":        strings.Replace(later, `"schema":1`, `"schema":2`, 1),
		"later value still JSON":      strings.Replace(later, `"lnd_alias":"node"`, `"lnd_alias":node`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeSystem([]byte(input)); err == nil {
				t.Fatalf("accepted %s", input)
			}
		})
	}
}

func TestStoreKeepsTheReviewedHybridAddress(t *testing.T) {
	store := testStore(t)
	cfg := Default()
	cfg.P2PMode, cfg.P2PAddress = "hybrid", "203.0.113.7"
	if err := store.Save(cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil || loaded.P2PMode != "hybrid" || loaded.P2PAddress != "203.0.113.7" {
		t.Fatalf("loaded %+v, %v", loaded, err)
	}
	for name, bad := range map[string]func(*AppConfig){
		"tor with an address":    func(c *AppConfig) { c.P2PMode, c.P2PAddress = "tor", "203.0.113.7" },
		"hybrid without address": func(c *AppConfig) { c.P2PMode, c.P2PAddress = "hybrid", "" },
	} {
		t.Run(name, func(t *testing.T) {
			c := Default()
			bad(c)
			if err := store.Save(c); err == nil {
				t.Fatal("saved an address that does not fit the P2P mode")
			}
			if again, err := store.Load(); err != nil || *again != *loaded {
				t.Fatalf("refused save changed the file: %+v, %v", again, err)
			}
		})
	}
}

func TestDecodeSystemAcceptsPublicSignetProfile(t *testing.T) {
	data := []byte(`{"schema":1,"network":"public-signet","prune_size_gb":25,"db_cache_mb":512,"p2p_mode":"tor","p2p_address":"","auto_unlock_enabled":false,"syncthing_enabled":false}`)
	cfg, err := decodeSystem(data)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Network != NetworkPublicSignet {
		t.Fatalf("network = %q", cfg.Network)
	}
}

func TestStoreSaveLoadAndMetadata(t *testing.T) {
	store := testStore(t)
	cfg := Default()
	cfg.Network = "testnet4"
	cfg.DbCache = 2048
	cfg.SyncthingEnabled = true
	if err := store.Save(cfg); err != nil {
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
	if loaded.Network != "testnet4" || loaded.DbCache != 2048 || !loaded.SyncthingEnabled {
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
	cfg := Default()
	cfg.P2PMode, cfg.P2PAddress = "hybrid", "203.0.113.7"
	if err := store.Save(cfg); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(before, after) {
		t.Fatal("config inode was modified in place instead of replaced")
	}
	loaded, err := store.Load()
	if err != nil || loaded.P2PMode != "hybrid" {
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
	cfg.P2PMode = "bogus"
	if err := store.Save(cfg); err == nil {
		t.Fatal("saved invalid config")
	}
	if _, err := os.Lstat(store.Dir); !os.IsNotExist(err) {
		t.Fatalf("invalid save mutated filesystem: %v", err)
	}
}

func TestNetworkConfigRouting(t *testing.T) {
	mainnet := Default()
	mainProfile, err := mainnet.NetworkConfig()
	if err != nil {
		t.Fatal(err)
	}
	if mainProfile.RPCPort != 8332 {
		t.Fatalf("mainnet RPC port %d", mainProfile.RPCPort)
	}
	testnet := Default()
	testnet.Network = NetworkTestnet4
	testProfile, err := testnet.NetworkConfig()
	if err != nil {
		t.Fatal(err)
	}
	if testProfile.RPCPort != 48332 {
		t.Fatalf("testnet4 RPC port %d", testProfile.RPCPort)
	}
}
