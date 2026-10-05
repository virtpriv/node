// cmd/main_test.go

package main

import (
	"errors"
	"os/user"
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
	if err != nil || cmd != cmdInstall {
		t.Fatalf("install: got (%v,%v)", cmd, err)
	}
	if opts.Network != "" || opts.Unattended || opts.UntilBake {
		t.Errorf("install: unexpected opts %+v", opts)
	}

	cmd, opts, err = parseArgs(
		[]string{"install", "--testnet4", "--unattended"})
	if err != nil || cmd != cmdInstall {
		t.Fatalf("install flags: got (%v,%v)", cmd, err)
	}
	if opts.Network != "testnet4" || !opts.Unattended {
		t.Errorf("install flags: got %+v", opts)
	}
	cmd, opts, err = parseArgs([]string{"install", "--signet"})
	if err != nil || cmd != cmdInstall ||
		opts.Network != config.NetworkPublicSignet {
		t.Errorf("signet install: got (%v,%+v,%v)", cmd, opts, err)
	}
	if _, _, err := parseArgs(
		[]string{"install", "--testnet4", "--signet"}); err == nil {
		t.Error("conflicting install network flags accepted")
	}

	if _, _, err := parseArgs(
		[]string{"install", "--bogus"}); err == nil {
		t.Error("unknown install flag accepted")
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

	cmd, opts, err = parseArgs([]string{
		"install", "--unattended", "--allow-console-only"})
	if err == nil {
		t.Errorf("allow-console-only: got (%v,%+v,%v)",
			cmd, opts, err)
	}

	cmd, _, err = parseArgs([]string{"helperd"})
	if err != nil || cmd != cmdHelperd {
		t.Errorf("helperd: got (%v,%v)", cmd, err)
	}
	if _, _, err := parseArgs(
		[]string{"helperd", "--flag"}); err == nil {
		t.Error("helperd with arguments accepted")
	}

	cmd, _, err = parseArgs([]string{"stage-lnd-cert"})
	if err != nil || cmd != cmdStageLNDCert {
		t.Errorf("stage-lnd-cert: got (%v,%v)", cmd, err)
	}
	if _, _, err := parseArgs(
		[]string{"stage-lnd-cert", "--flag"}); err == nil {
		t.Error("stage-lnd-cert with arguments accepted")
	}

	for _, network := range config.SupportedNetworks() {
		cmd, opts, err = parseArgs(
			[]string{"publish-lnd-backup", network})
		if err != nil || cmd != cmdPublishLNDBackup ||
			opts.Network != network {
			t.Errorf("publisher %s: got (%v,%+v,%v)",
				network, cmd, opts, err)
		}
	}
	for _, args := range [][]string{
		{"publish-lnd-backup"},
		{"publish-lnd-backup", "signet"},
		{"publish-lnd-backup", "controlled-signet-v1"},
		{"publish-lnd-backup", "mainnet", "/tmp/destination"},
	} {
		if _, _, err := parseArgs(args); err == nil {
			t.Errorf("invalid publisher arguments accepted: %v", args)
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
