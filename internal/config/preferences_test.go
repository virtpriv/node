package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPreferencesStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "vpn")
	store := &PreferencesStore{Dir: dir, Path: filepath.Join(dir, "preferences.json")}
	prefs, err := store.Load()
	if err != nil || prefs.Theme != "dark" {
		t.Fatalf("missing preferences: %+v, %v", prefs, err)
	}
	prefs.Theme = "light"
	if err := store.Save(prefs); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{dir: 0o700, store.Path: 0o600} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s mode %04o, want %04o", path, info.Mode().Perm(), want)
		}
	}
	loaded, err := store.Load()
	if err != nil || loaded.Theme != "light" {
		t.Fatalf("loaded preferences: %+v, %v", loaded, err)
	}
}

func TestDefaultPreferencesStoreUsesXDGConfigHome(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("XDG path is the certified Linux behavior")
	}
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	store, err := DefaultPreferencesStore()
	if err != nil {
		t.Fatal(err)
	}
	wantDir := filepath.Join(xdg, "vpn")
	if store.Dir != wantDir ||
		store.Path != filepath.Join(wantDir, "preferences.json") {
		t.Fatalf("store = %+v, want directory %s", store, wantDir)
	}
}

func TestPreferencesStrictAndSymlinkSafe(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "vpn")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	store := &PreferencesStore{Dir: dir, Path: filepath.Join(dir, "preferences.json")}
	cases := []string{
		`{"schema":2,"theme":"dark"}`,
		`{"schema":1,"theme":"blue"}`,
		`{"schema":1,"theme":"dark","extra":true}`,
		`{"schema":1,"schema":1,"theme":"dark"}`,
		`{"schema":1}`,
		`{"schema":1,"theme":null}`,
	}
	for _, input := range cases {
		if err := os.WriteFile(store.Path, []byte(input), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Load(); err == nil {
			t.Errorf("accepted %s", input)
		}
	}
	if err := os.Remove(store.Path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte(strings.Repeat("x", 5)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, store.Path); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(DefaultPreferences()); err == nil {
		t.Fatal("saved preferences through symlink")
	}
}
