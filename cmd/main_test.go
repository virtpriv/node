// cmd/main_test.go

package main

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/virtpriv/node/internal/config"
)

// Explicit dispatch: the command line alone decides the mode.
func TestParseArgs(t *testing.T) {
	cmd, _, err := parseArgs(nil)
	if err != nil || cmd != cmdConsole {
		t.Errorf("no args: got (%v,%v), want console", cmd, err)
	}

	cmd, opts, err := parseArgs([]string{"install"})
	if err != nil || cmd != cmdInstall || opts.Unattended {
		t.Fatalf("install: got (%v,%+v,%v)", cmd, opts, err)
	}
	cmd, opts, err = parseArgs([]string{"install", "--unattended"})
	if err != nil || cmd != cmdInstall || !opts.Unattended {
		t.Fatalf("install --unattended: got (%v,%+v,%v)", cmd, opts, err)
	}

	// The last three chose a Bitcoin network. There is none to choose now.
	for _, args := range [][]string{
		{"install", "--bogus"},
		{"install", "--until=bake"},
		{"install", "--allow-console-only"},
		{"install", "mainnet"},
		{"install", "--unattended", "extra"},
		{"install", "--testnet4"},
		{"install", "--signet"},
		{"install", "--testnet4", "--unattended"},
	} {
		if _, _, err := parseArgs(args); err == nil {
			t.Errorf("install arguments %v accepted", args)
		}
	}
	// A mistyped option is named the way the owner typed it.
	for _, typed := range []string{"--unatended", "-unatended", "--unatended=1"} {
		_, _, err := parseArgs([]string{"install", typed})
		name, _, _ := strings.Cut(typed, "=")
		if err == nil || !strings.Contains(err.Error(), " "+name) ||
			strings.Contains(err.Error(), "-"+name) {
			t.Errorf("install %s: got %v, want the option as typed", typed, err)
		}
	}
	if _, _, err := parseArgs([]string{"bogus"}); err == nil {
		t.Error("unknown command accepted")
	}

	for _, v := range []string{"version", "--version", "-v"} {
		if cmd, _, err := parseArgs([]string{v}); err != nil ||
			cmd != cmdVersion {
			t.Errorf("%s: got (%v,%v), want version", v, cmd, err)
		}
	}

	cmd, _, err = parseArgs([]string{"vpnd"})
	if err != nil || cmd != cmdVPND {
		t.Errorf("vpnd: got (%v,%v)", cmd, err)
	}
	if _, _, err := parseArgs(
		[]string{"vpnd", "--flag"}); err == nil {
		t.Error("vpnd with arguments accepted")
	}
	cmd, _, err = parseArgs([]string{"run-job", "20261109-183205-3f9a2c"})
	if err != nil || cmd != cmdRunJob {
		t.Errorf("run-job: got (%v,%v)", cmd, err)
	}
	for _, bad := range [][]string{{"run-job"}, {"run-job", "../etc"}, {"run-job", "20261109-183205-3f9a2c", "x"}} {
		if _, _, err := parseArgs(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}

	// Commands systemd ran for LND. No unit runs them now.
	for _, args := range [][]string{
		{"stage-lnd-cert"},
		{"publish-lnd-backup", "mainnet"},
	} {
		if _, _, err := parseArgs(args); err == nil || !strings.Contains(err.Error(), "unknown command") {
			t.Errorf("%v: got %v, want an unknown command", args, err)
		}
	}
}

func TestConsoleIdentityCheckedBeforeConfigurationLoad(t *testing.T) {
	lookup := func(string) (*user.User, error) {
		return &user.User{Uid: "1001", Username: "vpn"}, nil
	}
	loaded := false
	load := func() (*config.AppConfig, error) {
		loaded = true
		return config.Default(), nil
	}
	for _, euid := range []int{0, 1000} {
		loaded = false
		if _, err := loadConsoleConfig(euid, lookup, load); !errors.Is(err, errWrongTUIIdentity) {
			t.Errorf("euid %d error = %v", euid, err)
		}
		if loaded {
			t.Errorf("euid %d opened configuration", euid)
		}
	}
	loaded = false
	cfg, err := loadConsoleConfig(1001, lookup, load)
	if err != nil || cfg == nil || !loaded {
		t.Fatalf("vpn identity did not load config: cfg=%v loaded=%v err=%v", cfg, loaded, err)
	}
}

func TestConsoleIdentityLookupFailureDoesNotLoadConfig(t *testing.T) {
	loaded := false
	_, err := loadConsoleConfig(1001,
		func(string) (*user.User, error) { return nil, errors.New("lookup failed") },
		func() (*config.AppConfig, error) { loaded = true; return config.Default(), nil })
	if !errors.Is(err, errWrongTUIIdentity) || loaded {
		t.Fatalf("lookup failure: loaded=%v err=%v", loaded, err)
	}
}

// Before the install finishes there is no settings file. The TUI must say
// the node is not installed yet, not that a file needs repair.
func TestMissingSettingsMeansNotInstalled(t *testing.T) {
	dir := t.TempDir()
	missingDir := &config.Store{Dir: filepath.Join(dir, "vpn"), Path: filepath.Join(dir, "vpn", "config.json"), OwnerUID: os.Getuid(), GroupGID: os.Getgid()}
	_, err := missingDir.Load()
	if !notInstalled(err) {
		t.Fatalf("a missing settings folder was not read as not installed: %v", err)
	}
	if err := os.Mkdir(missingDir.Dir, 0o750); err != nil {
		t.Fatal(err)
	}
	_, err = missingDir.Load()
	if !notInstalled(err) {
		t.Fatalf("a missing settings file was not read as not installed: %v", err)
	}
	if err := os.WriteFile(missingDir.Path, []byte("{"), 0o640); err != nil {
		t.Fatal(err)
	}
	_, err = missingDir.Load()
	if err == nil || notInstalled(err) {
		t.Fatalf("a broken settings file was read as not installed: %v", err)
	}
}
