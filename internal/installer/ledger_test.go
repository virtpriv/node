package installer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validLedgerJSON = `{
  "schema": 2,
  "status": "in_progress",
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
	if !l.done("binary.install") || l.Status != ledgerInProgress {
		t.Fatalf("parsed ledger lost state: %+v", l)
	}
}

func TestParseLedgerRefusesAmbiguousOrInvalidState(t *testing.T) {
	const entry = `{"completed_at":"2026-08-10T12:00:00Z","version":"x"}`
	cases := map[string]string{
		"empty":                  "",
		"garbage":                "not json",
		"truncated":              `{"schema":2`,
		"trailing value":         validLedgerJSON + `{}`,
		"missing schema":         `{"status":"in_progress","steps":{}}`,
		"future schema":          `{"schema":3,"status":"in_progress","steps":{}}`,
		"missing status":         `{"schema":2,"steps":{}}`,
		"unknown status":         `{"schema":2,"status":"done","steps":{}}`,
		"null steps":             `{"schema":2,"status":"in_progress","steps":null}`,
		"unknown root field":     `{"schema":2,"status":"in_progress","steps":{},"extra":true}`,
		"unknown step":           `{"schema":2,"status":"in_progress","steps":{"addon.install":` + entry + `}}`,
		"unknown entry field":    `{"schema":2,"status":"in_progress","steps":{"binary.install":{"completed_at":"2026-08-10T12:00:00Z","version":"x","extra":true}}}`,
		"invalid timestamp":      `{"schema":2,"status":"in_progress","steps":{"binary.install":{"completed_at":"yesterday","version":"x"}}}`,
		"empty version":          `{"schema":2,"status":"in_progress","steps":{"binary.install":{"completed_at":"2026-08-10T12:00:00Z","version":""}}}`,
		"complete missing steps": `{"schema":2,"status":"complete","steps":{}}`,
		"unreachable subset":     `{"schema":2,"status":"in_progress","steps":{"identity.access":` + entry + `}}`,
		"duplicate root":         `{"schema":2,"schema":2,"status":"in_progress","steps":{}}`,
		"duplicate step":         `{"schema":2,"status":"in_progress","steps":{"binary.install":` + entry + `,"binary.install":` + entry + `}}`,
		"duplicate entry":        `{"schema":2,"status":"in_progress","steps":{"binary.install":{"completed_at":"2026-08-10T12:00:00Z","version":"x","version":"y"}}}`,
		// The records of a Bitcoin and Lightning node. This installer wrote
		// none of them, so it never continues or completes one.
		"an earlier node's new record": `{"schema":1,"status":"in_progress","context":{"network":"mainnet","initial_p2p_mode":"tor","bitcoin":"core","lightning":"lnd"},"steps":{}}`,
		"an earlier node's record":     `{"schema":1,"status":"in_progress","context":{"network":"mainnet","initial_p2p_mode":"tor","bitcoin":"core","lightning":"lnd","db_cache_mb":512},"steps":{"binary.install":` + entry + `}}`,
		"schema 1 in this shape":       `{"schema":1,"status":"in_progress","steps":{}}`,
		"a context":                    `{"schema":2,"status":"in_progress","context":{"network":"mainnet"},"steps":{}}`,
		"an empty context":             `{"schema":2,"status":"in_progress","context":{},"steps":{}}`,
		"an earlier node's step":       `{"schema":2,"status":"in_progress","steps":{"btc.download":` + entry + `}}`,
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseLedger([]byte(data)); err == nil {
				t.Fatal("invalid ledger accepted")
			}
		})
	}
}

// An install records its steps in list order, and a resumed run continues
// that order, so the steps done are always the first steps of the list.
func TestLedgerAcceptsOnlyTheFirstStepsInOrder(t *testing.T) {
	for n := 0; n <= len(baseInstallStepKeys); n++ {
		l := newLedger()
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
	l := newLedger()
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

// readLedger reads the record as a test wrote it. The installer itself reads
// it only after checking the file's owner and mode.
func readLedger(path string) (*installLedger, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read install ledger %s: %w", path, err)
	}
	l, err := parseLedger(data)
	if err != nil {
		return nil, fmt.Errorf("invalid install ledger %s: %w", path, err)
	}
	return l, nil
}

func TestLedgerRoundTripAndAtomicReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "install-state.json")
	l := newLedger()
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

// The record is complete only with every step of the install, the last one
// included, and it takes no step of anything installed later.
func TestLedgerCompletionRequiresBoundedBaseSteps(t *testing.T) {
	l := newLedger()
	if err := l.markComplete(); err == nil {
		t.Fatal("incomplete ledger marked complete")
	}
	last := len(baseInstallStepKeys) - 1
	for _, key := range baseInstallStepKeys[:last] {
		if err := l.markDone(key, "0.7.0"); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.markComplete(); err == nil {
		t.Fatal("ledger without its last step marked complete")
	}
	if err := l.markDone(baseInstallStepKeys[last], "0.7.0"); err != nil {
		t.Fatal(err)
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

// The record accepts only step sets this installer can produce. With the
// package upgrade behind Tor, an install stopped just before or just after it
// must resume, and a record in the earlier order is refused, not reinterpreted.
func TestLedgerFollowsThePackageUpgradeBehindTor(t *testing.T) {
	beforeUpgrade := []string{"binary.install", "apt.base", "firewall", "host.prep", "identity.access",
		"ipv6.disable", "mptcp.disable", "tor.configure", "tor.gate", "apt.tor"}
	record := func(keys ...string) *installLedger {
		t.Helper()
		l := newLedger()
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
	steps := buildInstallSteps(&InstallDecisions{}, nil)
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
