package installer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/virtpriv/node/internal/config"
)

const validLedgerJSON = `{
  "schema": 1,
  "status": "in_progress",
  "context": {
    "network": "testnet4",
    "initial_p2p_mode": "tor",
    "db_cache_mb": 1024
  },
  "steps": {
    "binary.install": {
      "completed_at": "2026-08-10T12:00:00Z",
      "version": "0.7.0"
    }
  }
}`

func TestParseLedgerValid(t *testing.T) {
	l, err := parseLedger([]byte(validLedgerJSON))
	if err != nil {
		t.Fatal(err)
	}
	if !l.done("binary.install") || l.Context.Network != "testnet4" ||
		l.Context.DbCacheMB == nil || *l.Context.DbCacheMB != 1024 {
		t.Fatalf("parsed ledger lost state: %+v", l)
	}
}

func TestParseLedgerRefusesAmbiguousOrInvalidState(t *testing.T) {
	cases := map[string]string{
		"empty":                  "",
		"garbage":                "not json",
		"truncated":              `{"schema":1`,
		"trailing value":         validLedgerJSON + `{}`,
		"missing schema":         `{"status":"in_progress","context":{"network":"mainnet","initial_p2p_mode":"tor"},"steps":{}}`,
		"old schema":             `{"schema":0,"status":"in_progress","context":{"network":"mainnet","initial_p2p_mode":"tor"},"steps":{}}`,
		"future schema":          `{"schema":2,"status":"in_progress","context":{"network":"mainnet","initial_p2p_mode":"tor"},"steps":{}}`,
		"missing status":         `{"schema":1,"context":{"network":"mainnet","initial_p2p_mode":"tor"},"steps":{}}`,
		"unknown status":         `{"schema":1,"status":"done","context":{"network":"mainnet","initial_p2p_mode":"tor"},"steps":{}}`,
		"missing context":        `{"schema":1,"status":"in_progress","steps":{}}`,
		"bad network":            `{"schema":1,"status":"in_progress","context":{"network":"signet","initial_p2p_mode":"tor"},"steps":{}}`,
		"controlled signet":      `{"schema":1,"status":"in_progress","context":{"network":"controlled-signet-v1","initial_p2p_mode":"tor"},"steps":{}}`,
		"bad p2p":                `{"schema":1,"status":"in_progress","context":{"network":"mainnet","initial_p2p_mode":"open"},"steps":{}}`,
		"hybrid start":           `{"schema":1,"status":"in_progress","context":{"network":"mainnet","initial_p2p_mode":"hybrid"},"steps":{}}`,
		"bad dbcache":            `{"schema":1,"status":"in_progress","context":{"network":"mainnet","initial_p2p_mode":"tor","db_cache_mb":999},"steps":{}}`,
		"null steps":             `{"schema":1,"status":"in_progress","context":{"network":"mainnet","initial_p2p_mode":"tor"},"steps":null}`,
		"unknown root field":     `{"schema":1,"status":"in_progress","context":{"network":"mainnet","initial_p2p_mode":"tor"},"steps":{},"extra":true}`,
		"unknown context field":  `{"schema":1,"status":"in_progress","context":{"network":"mainnet","initial_p2p_mode":"tor","extra":true},"steps":{}}`,
		"unknown step":           `{"schema":1,"status":"in_progress","context":{"network":"mainnet","initial_p2p_mode":"tor"},"steps":{"addon.install":{"completed_at":"2026-08-10T12:00:00Z","version":"x"}}}`,
		"unknown entry field":    `{"schema":1,"status":"in_progress","context":{"network":"mainnet","initial_p2p_mode":"tor","db_cache_mb":512},"steps":{"binary.install":{"completed_at":"2026-08-10T12:00:00Z","version":"x","extra":true}}}`,
		"invalid timestamp":      `{"schema":1,"status":"in_progress","context":{"network":"mainnet","initial_p2p_mode":"tor","db_cache_mb":512},"steps":{"binary.install":{"completed_at":"yesterday","version":"x"}}}`,
		"empty version":          `{"schema":1,"status":"in_progress","context":{"network":"mainnet","initial_p2p_mode":"tor","db_cache_mb":512},"steps":{"binary.install":{"completed_at":"2026-08-10T12:00:00Z","version":""}}}`,
		"complete missing steps": `{"schema":1,"status":"complete","context":{"network":"mainnet","initial_p2p_mode":"tor"},"steps":{}}`,
		"btc without dbcache":    `{"schema":1,"status":"in_progress","context":{"network":"mainnet","initial_p2p_mode":"tor"},"steps":{"btc.install":{"completed_at":"2026-08-10T12:00:00Z","version":"x"}}}`,
		"unreachable subset":     `{"schema":1,"status":"in_progress","context":{"network":"mainnet","initial_p2p_mode":"tor","db_cache_mb":512},"steps":{"identity.access":{"completed_at":"2026-08-10T12:00:00Z","version":"x"}}}`,
		"duplicate root":         `{"schema":1,"schema":1,"status":"in_progress","context":{"network":"mainnet","initial_p2p_mode":"tor"},"steps":{}}`,
		"duplicate context":      `{"schema":1,"status":"in_progress","context":{"network":"mainnet","network":"testnet4","initial_p2p_mode":"tor"},"steps":{}}`,
		"duplicate step":         `{"schema":1,"status":"in_progress","context":{"network":"mainnet","initial_p2p_mode":"tor","db_cache_mb":512},"steps":{"binary.install":{"completed_at":"2026-08-10T12:00:00Z","version":"x"},"binary.install":{"completed_at":"2026-08-10T12:00:00Z","version":"x"}}}`,
		"duplicate entry":        `{"schema":1,"status":"in_progress","context":{"network":"mainnet","initial_p2p_mode":"tor","db_cache_mb":512},"steps":{"binary.install":{"completed_at":"2026-08-10T12:00:00Z","version":"x","version":"y"}}}`,
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseLedger([]byte(data)); err == nil {
				t.Fatal("invalid ledger accepted")
			}
		})
	}
}

func TestLedgerAcceptsEverySupportedImmutableProfile(t *testing.T) {
	for _, network := range []string{"mainnet", "testnet4", "public-signet"} {
		ledger, err := newLedger(installContext{
			Network: network, InitialP2PMode: "tor",
		})
		if err != nil {
			t.Errorf("%s ledger rejected: %v", network, err)
			continue
		}
		if ledger.Context.Network != network {
			t.Errorf("%s ledger recorded %q", network, ledger.Context.Network)
		}
	}
}

// An install records its steps in list order, and a resumed run continues
// that order, so the steps done are always the first steps of the list.
func TestLedgerAcceptsOnlyTheFirstStepsInOrder(t *testing.T) {
	for n := 0; n <= len(baseInstallStepKeys); n++ {
		l := testLedger()
		for _, key := range baseInstallStepKeys[:n] {
			if err := l.markDone(key, "0.7.0"); err != nil {
				t.Fatal(err)
			}
		}
		if err := validateLedger(l); err != nil {
			t.Fatalf("first %d steps refused: %v", n, err)
		}
	}
	// The first six steps without the owner account, which no install writes.
	l := testLedger()
	for _, key := range baseInstallStepKeys[:6] {
		if key == "identity.access" {
			continue
		}
		if err := l.markDone(key, "0.7.0"); err != nil {
			t.Fatal(err)
		}
	}
	if err := validateLedger(l); err == nil {
		t.Fatal("steps with a gap accepted")
	}
}

func TestLedgerRoundTripAndAtomicReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "install-state.json")
	l := testLedger()
	if err := l.markDone("binary.install", "0.7.0"); err != nil {
		t.Fatal(err)
	}
	if err := l.save(path); err != nil {
		t.Fatal(err)
	}
	first, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if first.Mode().Perm() != 0o600 {
		t.Fatalf("ledger mode %04o, want 0600", first.Mode().Perm())
	}
	if err := l.markDone("apt.base", "0.7.0"); err != nil {
		t.Fatal(err)
	}
	if err := l.save(path); err != nil {
		t.Fatal(err)
	}
	got, err := readLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	if !got.done("binary.install") || !got.done("apt.base") {
		t.Fatal("replacement lost completed entries")
	}
	data, _ := os.ReadFile(path)
	if !strings.HasSuffix(string(data), "\n") {
		t.Error("ledger lacks final newline")
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("temporary ledger files remain: %v", entries)
	}
}

func TestLedgerCompletionRequiresBoundedBaseSteps(t *testing.T) {
	l := testLedger()
	if err := l.markComplete(); err == nil {
		t.Fatal("incomplete ledger marked complete")
	}
	for _, key := range baseInstallStepKeys {
		if err := l.markDone(key, "0.7.0"); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.markComplete(); err != nil {
		t.Fatal(err)
	}
	if l.Status != ledgerComplete {
		t.Fatalf("status %q, want complete", l.Status)
	}
	if err := l.markDone("addon.install", "0.7.0"); err == nil {
		t.Fatal("add-on key entered base ledger")
	}
}

func TestLedgerDbCacheImmutable(t *testing.T) {
	l, err := newLedger(installContext{
		Network: "mainnet", InitialP2PMode: "tor",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.setDbCache(1024); err != nil {
		t.Fatal(err)
	}
	if err := l.setDbCache(1024); err != nil {
		t.Fatalf("same value not idempotent: %v", err)
	}
	if err := l.setDbCache(512); err == nil {
		t.Fatal("recorded dbcache changed")
	}
	if err := l.setDbCache(123); err == nil {
		t.Fatal("invalid dbcache accepted")
	}
}

// The record accepts only step sets this installer can produce. With the
// package upgrade behind Tor, an install stopped just before or just after it
// must resume, and a record in the earlier order is refused, not reinterpreted.
func TestLedgerFollowsThePackageUpgradeBehindTor(t *testing.T) {
	beforeUpgrade := []string{"binary.install", "apt.base", "firewall", "host.prep", "identity.access",
		"service-identities.v1", "ipv6.disable", "tor.configure", "tor.gate", "apt.torproxy"}
	record := func(keys ...string) *installLedger {
		t.Helper()
		l := testLedger()
		for _, key := range keys {
			if err := l.markDone(key, "0.7.0"); err != nil {
				t.Fatal(err)
			}
		}
		return l
	}
	stopped := record(beforeUpgrade...)
	if err := validateLedger(stopped); err != nil {
		t.Fatal("install stopped before the package upgrade cannot resume:", err)
	}
	steps := buildInstallSteps(config.Default(), &InstallDecisions{}, t.TempDir())
	var runs []string
	for i, p := range planRun(steps, stopped) {
		if p.Run && steps[i].Kind != StepGate {
			runs = append(runs, steps[i].Key)
		}
	}
	if len(runs) == 0 || runs[0] != "base.upgrade" {
		t.Fatal("resume does not continue with the package upgrade:", runs)
	}
	if err := validateLedger(record(append(beforeUpgrade, "base.upgrade")...)); err != nil {
		t.Fatal("install stopped after the package upgrade cannot resume:", err)
	}
	if validateLedger(record("binary.install", "apt.base", "firewall", "base.upgrade")) == nil {
		t.Fatal("accepted a record in the earlier order, where the upgrade ran before Tor")
	}
}

// "Restarting Tor" and "Configuring LND" repeated steps 8 and 15 and are
// gone. A record from a build that still had them is refused, like any
// record this installer did not write. That holds for a finished record
// too: on a node installed by such a build, sudo vpn install now refuses
// instead of saying it is already installed. Only test nodes hold one,
// since v0.7.0 installs fresh.
func TestLedgerRefusesTheRemovedSteps(t *testing.T) {
	for _, removed := range []string{"tor.lnd", "lnd.configure"} {
		l := testLedger()
		for _, key := range baseInstallStepKeys {
			if key == "lnd.start" {
				break
			}
			if err := l.markDone(key, "0.7.0"); err != nil {
				t.Fatal(err)
			}
		}
		l.Steps[removed] = ledgerEntry{CompletedAt: "2026-10-20T12:00:00Z", Version: "0.7.0-rc.13"}
		if err := validateLedger(l); err == nil {
			t.Errorf("a record holding %s was accepted", removed)
		}
	}
}
