package host

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/virtpriv/node/internal/config"
)

func mustBuildTorConfig(t *testing.T, cfg *config.AppConfig) string {
	t.Helper()
	content, err := BuildTorConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func TestTorConfigWithLND(t *testing.T) {
	cfg := config.Default()
	content := mustBuildTorConfig(t, cfg)

	required := []string{
		"SOCKSPort 9050",
		"ControlPort 9051",
		"CookieAuthentication 1",
		"CookieAuthFileGroupReadable 1",
		"bitcoin-p2p",
		"lnd-grpc",
		"lnd-rest",
		"HiddenServicePort 10009",
		"HiddenServicePort 8080",
	}
	for _, req := range required {
		if !strings.Contains(content, req) {
			t.Errorf("missing %q in LND torrc", req)
		}
	}
}

func TestTorConfigWithSyncthing(t *testing.T) {
	cfg := config.Default()
	cfg.SyncthingEnabled = true
	content := mustBuildTorConfig(t, cfg)

	// Web UI still accessible over Tor
	if !strings.Contains(content, "syncthing") {
		t.Error("missing syncthing hidden service")
	}
	if !strings.Contains(content, "HiddenServicePort 8384") {
		t.Error("missing Syncthing web UI port")
	}

	// Sync protocol goes over clearnet: no hidden service
	if strings.Contains(content, "syncthing-sync") {
		t.Error("should not have syncthing-sync hidden service")
	}
	if strings.Contains(content, "HiddenServicePort 22000") {
		t.Error("should not have port 22000 hidden service")
	}
}

func TestTorConfigNoSyncthingWithoutInstall(t *testing.T) {
	cfg := config.Default()
	content := mustBuildTorConfig(t, cfg)

	if strings.Contains(content, "syncthing") {
		t.Error("should not have syncthing without install")
	}
}

func TestTorConfigMainnetPorts(t *testing.T) {
	cfg := config.Default()
	content := mustBuildTorConfig(t, cfg)

	if !strings.Contains(content, "HiddenServicePort 8333") {
		t.Error("mainnet torrc should use port 8333 for P2P")
	}
	if strings.Contains(content, "HiddenServicePort 8332") {
		t.Error("mainnet torrc should not have RPC hidden service")
	}
}

func TestTorConfigTestnet4Ports(t *testing.T) {
	cfg := &config.AppConfig{Network: "testnet4"}
	content := mustBuildTorConfig(t, cfg)

	if !strings.Contains(content, "HiddenServicePort 48333") {
		t.Error("testnet4 torrc should use port 48333 for P2P")
	}
	if strings.Contains(content, "HiddenServicePort 48332") {
		t.Error("testnet4 torrc should not have RPC hidden service")
	}
}

func TestTorConfigPublicSignetPorts(t *testing.T) {
	cfg := config.Default()
	cfg.Network = config.NetworkPublicSignet
	content := mustBuildTorConfig(t, cfg)

	if !strings.Contains(content, "HiddenServicePort 38333 127.0.0.1:38333") {
		t.Error("public-signet torrc should use port 38333 for P2P")
	}
	if strings.Contains(content, "HiddenServicePort 38332") {
		t.Error("public-signet torrc should not have an RPC hidden service")
	}
}

func TestTorConfigRejectsUnknownProfile(t *testing.T) {
	cfg := config.Default()
	cfg.Network = "signet"
	if _, err := BuildTorConfig(cfg); err == nil {
		t.Fatal("raw signet profile generated a Tor config")
	}
}

func withTorAddonTestDeps(t *testing.T) {
	t.Helper()
	oldPresent := torBinaryPresentForAddon
	oldEnabled := torServiceEnabledForAddon
	oldActive := torServiceActiveForAddon
	oldReadConfig := readTorConfigForAddon
	oldReadOnion := readSyncthingOnionForAddon
	oldSleep := sleepForTorAddon
	oldValidate := validateTorConfigForAddon
	oldWrite := writeTorConfigForAddon
	oldRun := runTorServiceAction
	oldFirewallCheck := requireActiveFirewallForAddon
	oldWriteRule := writeTorRestartRule
	oldReload := reloadSystemdForTor
	t.Cleanup(func() {
		torBinaryPresentForAddon = oldPresent
		torServiceEnabledForAddon = oldEnabled
		torServiceActiveForAddon = oldActive
		readTorConfigForAddon = oldReadConfig
		readSyncthingOnionForAddon = oldReadOnion
		sleepForTorAddon = oldSleep
		validateTorConfigForAddon = oldValidate
		writeTorConfigForAddon = oldWrite
		runTorServiceAction = oldRun
		requireActiveFirewallForAddon = oldFirewallCheck
		writeTorRestartRule = oldWriteRule
		reloadSystemdForTor = oldReload
	})
}

func TestSyncthingPrerequisitesValidateCompleteProposedTorConfig(t *testing.T) {
	withTorAddonTestDeps(t)
	cfg := config.Default()
	torBinaryPresentForAddon = func() bool { return true }
	torServiceEnabledForAddon = func() bool { return true }
	torServiceActiveForAddon = func() bool { return true }
	requireActiveFirewallForAddon = func() error { return nil }
	readTorConfigForAddon = func(string) ([]byte, error) {
		return []byte(mustBuildTorConfig(t, cfg)), nil
	}

	validated := ""
	validateTorConfigForAddon = func(content []byte) error {
		validated = string(content)
		return nil
	}
	if err := verifySyncthingInstallPrerequisites(cfg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(validated,
		"HiddenServiceDir /var/lib/tor/syncthing/") {
		t.Fatalf("preflight did not validate proposed Syncthing torrc:\n%s",
			validated)
	}

	validateTorConfigForAddon = func([]byte) error {
		return errors.New("invalid proposed config")
	}
	if err := verifySyncthingInstallPrerequisites(cfg); err == nil ||
		!strings.Contains(err.Error(), "validate proposed Syncthing") {
		t.Fatalf("invalid proposed torrc error=%v", err)
	}
}

func TestSyncthingTorPrerequisiteRequiresExpectedBaseState(t *testing.T) {
	withTorAddonTestDeps(t)
	cfg := config.Default()
	torBinaryPresentForAddon = func() bool { return true }
	torServiceEnabledForAddon = func() bool { return true }
	torServiceActiveForAddon = func() bool { return true }
	readTorConfigForAddon = func(string) ([]byte, error) {
		return []byte(mustBuildTorConfig(t, cfg)), nil
	}
	if err := verifySyncthingTorPrerequisite(cfg); err != nil {
		t.Fatal(err)
	}

	torServiceEnabledForAddon = func() bool { return false }
	if err := verifySyncthingTorPrerequisite(cfg); err == nil {
		t.Fatal("disabled Tor accepted as Syncthing prerequisite")
	}
	torServiceEnabledForAddon = func() bool { return true }
	torServiceActiveForAddon = func() bool { return false }
	if err := verifySyncthingTorPrerequisite(cfg); err == nil {
		t.Fatal("inactive Tor accepted as Syncthing prerequisite")
	}
	torServiceActiveForAddon = func() bool { return true }
	readTorConfigForAddon = func(string) ([]byte, error) {
		return []byte("foreign configuration\n"), nil
	}
	if err := verifySyncthingTorPrerequisite(cfg); err == nil {
		t.Fatal("unexpected base torrc accepted")
	}
}

func setSyncthingTorTransactionDeps(
	t *testing.T, cfg *config.AppConfig, current *[]byte,
) {
	t.Helper()
	withTorAddonTestDeps(t)
	baseCfg := *cfg
	baseCfg.SyncthingEnabled = false
	*current = []byte(mustBuildTorConfig(t, &baseCfg))
	torBinaryPresentForAddon = func() bool { return true }
	torServiceEnabledForAddon = func() bool { return true }
	torServiceActiveForAddon = func() bool { return true }
	readTorConfigForAddon = func(string) ([]byte, error) {
		return append([]byte(nil), (*current)...), nil
	}
	readSyncthingOnionForAddon = func(string) ([]byte, error) {
		return []byte(strings.Repeat("a", 56) + ".onion\n"), nil
	}
	sleepForTorAddon = func(time.Duration) {}
	validateTorConfigForAddon = func([]byte) error { return nil }
}

func TestSyncthingTorConfigValidatesWritesAndReloads(t *testing.T) {
	cfg := config.Default()
	cfg.SyncthingEnabled = true
	var current []byte
	setSyncthingTorTransactionDeps(t, cfg, &current)

	var validated [][]byte
	validateTorConfigForAddon = func(content []byte) error {
		validated = append(validated, append([]byte(nil), content...))
		return nil
	}
	var writes [][]byte
	writeTorConfigForAddon = func(content []byte) error {
		current = append([]byte(nil), content...)
		writes = append(writes, append([]byte(nil), content...))
		return nil
	}
	var actions []string
	runTorServiceAction = func(action string) error {
		actions = append(actions, action)
		return nil
	}

	if err := configureAndReloadTorForSyncthing(cfg); err != nil {
		t.Fatal(err)
	}
	wantConfig := []byte(mustBuildTorConfig(t, cfg))
	if len(validated) != 1 || !reflect.DeepEqual(validated[0], wantConfig) {
		t.Fatalf("validated configs=%q want proposed config", validated)
	}
	if len(writes) != 1 || !reflect.DeepEqual(writes[0], wantConfig) {
		t.Fatalf("writes=%q want proposed config", writes)
	}
	if want := []string{"reload"}; !reflect.DeepEqual(actions, want) {
		t.Fatalf("Tor actions=%v want=%v", actions, want)
	}
}

func TestSyncthingTorConfigValidationFailureDoesNotMutate(t *testing.T) {
	cfg := config.Default()
	cfg.SyncthingEnabled = true
	var current []byte
	setSyncthingTorTransactionDeps(t, cfg, &current)
	original := append([]byte(nil), current...)

	validateTorConfigForAddon = func([]byte) error {
		return errors.New("invalid config")
	}
	writes := 0
	writeTorConfigForAddon = func([]byte) error {
		writes++
		return nil
	}
	actions := 0
	runTorServiceAction = func(string) error {
		actions++
		return nil
	}

	if err := configureAndReloadTorForSyncthing(cfg); err == nil {
		t.Fatal("invalid Tor configuration accepted")
	}
	if writes != 0 || actions != 0 || !reflect.DeepEqual(current, original) {
		t.Fatalf("validation failure mutated state: writes=%d actions=%d",
			writes, actions)
	}
}

func TestSyncthingTorReloadFailureRollsBack(t *testing.T) {
	cfg := config.Default()
	cfg.SyncthingEnabled = true
	var current []byte
	setSyncthingTorTransactionDeps(t, cfg, &current)
	original := append([]byte(nil), current...)

	var writes [][]byte
	writeTorConfigForAddon = func(content []byte) error {
		current = append([]byte(nil), content...)
		writes = append(writes, append([]byte(nil), content...))
		return nil
	}
	var actions []string
	runTorServiceAction = func(action string) error {
		actions = append(actions, action)
		if len(actions) == 1 {
			return errors.New("reload failed")
		}
		return nil
	}

	err := configureAndReloadTorForSyncthing(cfg)
	if err == nil || !strings.Contains(err.Error(), "previous configuration restored") {
		t.Fatalf("reload failure=%v, want successful rollback", err)
	}
	if len(writes) != 2 || !reflect.DeepEqual(current, original) {
		t.Fatalf("writes=%d current config was not restored", len(writes))
	}
	if want := []string{"reload", "reload"}; !reflect.DeepEqual(actions, want) {
		t.Fatalf("Tor actions=%v want=%v", actions, want)
	}
}

func TestSyncthingTorOnionFailureRollsBack(t *testing.T) {
	cfg := config.Default()
	cfg.SyncthingEnabled = true
	var current []byte
	setSyncthingTorTransactionDeps(t, cfg, &current)
	original := append([]byte(nil), current...)

	writes := 0
	writeTorConfigForAddon = func(content []byte) error {
		current = append([]byte(nil), content...)
		writes++
		return nil
	}
	var actions []string
	runTorServiceAction = func(action string) error {
		actions = append(actions, action)
		return nil
	}
	readSyncthingOnionForAddon = func(string) ([]byte, error) {
		return nil, os.ErrNotExist
	}

	err := configureAndReloadTorForSyncthing(cfg)
	if err == nil || !strings.Contains(err.Error(), "previous configuration restored") {
		t.Fatalf("onion failure=%v, want successful rollback", err)
	}
	if writes != 2 || !reflect.DeepEqual(current, original) {
		t.Fatalf("writes=%d current config was not restored", writes)
	}
	if want := []string{"reload", "reload"}; !reflect.DeepEqual(actions, want) {
		t.Fatalf("Tor actions=%v want=%v", actions, want)
	}
}

func TestSyncthingTorRollbackFailureIsSurfaced(t *testing.T) {
	cfg := config.Default()
	cfg.SyncthingEnabled = true
	var current []byte
	setSyncthingTorTransactionDeps(t, cfg, &current)

	writes := 0
	writeTorConfigForAddon = func(content []byte) error {
		writes++
		if writes == 2 {
			return errors.New("restore failed")
		}
		current = append([]byte(nil), content...)
		return nil
	}
	runTorServiceAction = func(string) error {
		return errors.New("reload failed")
	}

	err := configureAndReloadTorForSyncthing(cfg)
	if err == nil || !strings.Contains(err.Error(), "rollback failed") {
		t.Fatalf("rollback failure not surfaced: %v", err)
	}
}

// systemd must have loaded the retry rule before Tor is restarted, so the rule
// applies from the first crash. A rule that cannot be written or loaded fails
// the step before Tor is touched, so a resume runs it again.
func TestInitialTorOperationLoadsRetryRuleBeforeRestart(t *testing.T) {
	withTorAddonTestDeps(t)
	for _, tc := range []struct {
		name      string
		writeErr  error
		reloadErr error
		wantSteps []string
	}{
		{"rule written", nil, nil, []string{"write retry rule", "daemon-reload", "enable", "restart"}},
		{"rule not written", errors.New("disk full"), nil, []string{"write retry rule"}},
		{"rule not loaded", nil, errors.New("reload failed"), []string{"write retry rule", "daemon-reload"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var steps []string
			writeTorRestartRule = func() error {
				steps = append(steps, "write retry rule")
				return tc.writeErr
			}
			reloadSystemdForTor = func() error {
				steps = append(steps, "daemon-reload")
				return tc.reloadErr
			}
			runTorServiceAction = func(action string) error {
				steps = append(steps, action)
				return nil
			}
			err := EnableAndRestartTor()
			wantErr := tc.writeErr != nil || tc.reloadErr != nil
			if (err != nil) != wantErr {
				t.Fatalf("err=%v, want error %t", err, wantErr)
			}
			if !reflect.DeepEqual(steps, tc.wantSteps) {
				t.Fatalf("steps=%v want=%v", steps, tc.wantSteps)
			}
		})
	}
}

// Debian's tor.service only runs /bin/true and stays active while the real
// Tor, tor@default.service, is stopped. The Syncthing install must follow the
// real Tor in both directions.
func TestSyncthingTorCheckReadsTheRealTor(t *testing.T) {
	withTorAddonTestDeps(t)
	cfg := config.Default()
	torBinaryPresentForAddon = func() bool { return true }
	torServiceEnabledForAddon = func() bool { return true }
	readTorConfigForAddon = func(string) ([]byte, error) {
		return []byte(mustBuildTorConfig(t, cfg)), nil
	}
	for _, tc := range []struct {
		name            string
		wrapper, worker string
		wantRefused     bool
	}{
		{"real Tor stopped, wrapper active", "0", "3", true},
		{"real Tor running, wrapper inactive", "3", "0", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			script := "#!/bin/sh\ncase \"$*\" in\n" +
				" 'is-active --quiet tor@default.service') exit " + tc.worker + ";;\n" +
				" 'is-active --quiet tor.service'|'is-active --quiet tor') exit " + tc.wrapper + ";;\n" +
				" *) exit 2;;\nesac\n"
			if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir)
			err := verifySyncthingTorPrerequisite(cfg)
			if (err != nil) != tc.wantRefused {
				t.Fatalf("err=%v, want refused %t", err, tc.wantRefused)
			}
		})
	}
}

func TestWaitForSyncthingOnionRejectsMalformedHostname(t *testing.T) {
	withTorAddonTestDeps(t)
	readSyncthingOnionForAddon = func(string) ([]byte, error) {
		return []byte("short.onion\n"), nil
	}
	// End the fixed retry loop without wall-clock delay.
	sleepForTorAddon = func(time.Duration) {}
	if err := waitForSyncthingOnion(); err == nil {
		t.Fatal("malformed Syncthing onion accepted")
	}
	readSyncthingOnionForAddon = func(string) ([]byte, error) {
		return nil, os.ErrNotExist
	}
	if err := waitForSyncthingOnion(); err == nil {
		t.Fatal("missing Syncthing onion accepted")
	}
}
