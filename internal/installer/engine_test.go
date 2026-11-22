// internal/installer/engine_test.go

package installer

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

// testSteps builds a miniature install list with the shapes
// that matter: plain mutations and a gate. Fns count executions
// into calls. The keys are the first seven of the real install,
// in its order, because the ledger accepts only step sets a real
// install can reach.
func testSteps(calls map[string]int) []InstallStep {
	fn := func(key string) func() error {
		return func() error {
			calls[key]++
			return nil
		}
	}
	return []InstallStep{
		{Key: "binary.install", Name: "Installing binary",
			Run: plain(fn("binary.install"))},
		{Key: "apt.base", Name: "Installing base packages",
			Run: plain(fn("apt.base"))},
		{Key: "firewall", Name: "Verifying firewall",
			Kind: StepGate, Run: plain(fn("firewall"))},
		{Key: "host.prep", Name: "Preparing",
			Run: plain(fn("host.prep"))},
		{Key: "identity.access", Name: "Creating the account",
			Run: plain(fn("identity.access"))},
		{Key: "ipv6.disable", Name: "Disabling IPv6",
			Run: plain(fn("ipv6.disable"))},
		{Key: "mptcp.disable", Name: "Disabling MPTCP",
			Run: plain(fn("mptcp.disable"))},
	}
}

func mustReadLedger(t *testing.T, path string) *installLedger {
	t.Helper()
	l, err := readLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func keysToRun(steps []InstallStep, plan []stepPlan) []string {
	var out []string
	for i, p := range plan {
		if p.Run {
			out = append(out, steps[i].Key)
		}
	}
	return out
}

func sameKeys(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestValidateSteps(t *testing.T) {
	ok := testSteps(map[string]int{})
	if err := validateSteps(ok); err != nil {
		t.Errorf("valid steps rejected: %v", err)
	}

	dup := testSteps(map[string]int{})
	dup[1].Key = "binary.install"
	if err := validateSteps(dup); err == nil {
		t.Error("duplicate key accepted")
	}

	empty := testSteps(map[string]int{})
	empty[2].Key = ""
	if err := validateSteps(empty); err == nil {
		t.Error("empty key accepted")
	}
}

func TestPlanScenarios(t *testing.T) {
	all := []string{"binary.install", "apt.base", "firewall",
		"host.prep", "identity.access", "ipv6.disable",
		"mptcp.disable"}

	cases := []struct {
		name    string
		done    []string // pre-recorded ledger keys
		wantRun []string
	}{
		{
			name:    "fresh install runs everything",
			done:    nil,
			wantRun: all,
		},
		{
			name:    "all done: only the gate re-runs",
			done:    all,
			wantRun: []string{"firewall"},
		},
		{
			name: "resume after step 2: gate re-runs, rest forward",
			done: []string{"binary.install", "apt.base"},
			wantRun: []string{"firewall", "host.prep",
				"identity.access", "ipv6.disable", "mptcp.disable"},
		},
		{
			name: "resume after step 5: recorded steps stay done",
			done: []string{"binary.install", "apt.base",
				"firewall", "host.prep", "identity.access"},
			wantRun: []string{"firewall", "ipv6.disable", "mptcp.disable"},
		},
		{
			name: "unknown ledger keys ignored",
			done: []string{"binary.install", "no.such.step",
				"another.ghost"},
			wantRun: []string{"apt.base", "firewall",
				"host.prep", "identity.access", "ipv6.disable",
				"mptcp.disable"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			led := newLedger()
			for _, k := range c.done {
				if baseStepKeySet()[k] {
					if err := led.markDone(k, "0.6.3"); err != nil {
						t.Fatal(err)
					}
				} else {
					led.Steps[k] = ledgerEntry{
						CompletedAt: "2026-08-10T12:00:00Z", Version: "test"}
				}
			}
			steps := testSteps(map[string]int{})
			plan := planRun(steps, led)
			got := keysToRun(steps, plan)
			if !sameKeys(got, c.wantRun) {
				t.Errorf("run set = %v, want %v", got, c.wantRun)
			}
		})
	}
}

// The gate must re-run even when its own key is recorded. A
// recorded gate means "it held on some earlier pass", which is
// exactly what a gate must never rely on.
func TestPlanGateIgnoresOwnLedgerEntry(t *testing.T) {
	led := newLedger()
	if err := led.markDone("firewall", "0.6.3"); err != nil {
		t.Fatal(err)
	}
	steps := testSteps(map[string]int{})
	plan := planRun(steps, led)
	if !plan[2].Run {
		t.Fatal("gate was ledger-skipped")
	}
}

func TestClassifyRun(t *testing.T) {
	steps := testSteps(map[string]int{})
	total := len(steps)

	res := classifyRun(steps, true, false, total)
	if res.Outcome != RunComplete {
		t.Errorf("complete run classified %v", res.Outcome)
	}

	failed := testSteps(map[string]int{})
	failed[3].Status = StepFailed
	failed[3].Err = errors.New("preparation failed")
	res = classifyRun(failed, true, true, 3)
	if res.Outcome != RunFailed {
		t.Errorf("failed run classified %v", res.Outcome)
	}
	if res.StepNum != 4 || res.StepName != "Preparing" {
		t.Errorf("failure attributed to %d/%q",
			res.StepNum, res.StepName)
	}
	if res.Err == nil {
		t.Error("failed run lost its error")
	}

	// Quit mid-run: done=false gives interrupted, at the current
	// step, never complete.
	res = classifyRun(steps, false, false, 2)
	if res.Outcome != RunInterrupted {
		t.Errorf("interrupted run classified %v", res.Outcome)
	}
	if res.StepNum != 3 || res.StepName != "Verifying firewall" {
		t.Errorf("interrupt attributed to %d/%q",
			res.StepNum, res.StepName)
	}

	// Failed flag with no failed step: fail-safe, never complete.
	res = classifyRun(steps, true, true, total)
	if res.Outcome != RunFailed {
		t.Errorf("inconsistent state classified %v (must fail safe)",
			res.Outcome)
	}
}

func TestRunInstallUnattendedCompleteAndResume(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")

	// First pass: everything runs, outcome complete.
	calls := map[string]int{}
	led := newLedger()
	res, err := RunInstallUnattended(
		testSteps(calls), "0.6.3", led, path)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != RunComplete {
		t.Fatalf("first pass outcome %v", res.Outcome)
	}
	for k, n := range calls {
		if n != 1 {
			t.Errorf("first pass ran %s %d times", k, n)
		}
	}

	// Second pass over the same ledger: only the gate runs.
	calls2 := map[string]int{}
	led2 := mustReadLedger(t, path)
	res, err = RunInstallUnattended(
		testSteps(calls2), "0.6.3", led2, path)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != RunComplete {
		t.Fatalf("second pass outcome %v", res.Outcome)
	}
	for k, n := range calls2 {
		want := 0
		if k == "firewall" {
			want = 1
		}
		if n != want {
			t.Errorf("second pass ran %s %d times, want %d",
				k, n, want)
		}
	}
}

func TestRunInstallUnattendedFailureStops(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	calls := map[string]int{}
	steps := testSteps(calls)
	steps[2].Run = plain(func() error {
		calls["firewall"]++
		return fmt.Errorf("tor not routing")
	})

	led := newLedger()
	res, err := RunInstallUnattended(steps, "0.6.3", led, path)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != RunFailed {
		t.Fatalf("outcome %v, want RunFailed", res.Outcome)
	}
	if res.StepNum != 3 {
		t.Errorf("failed at %d, want 3", res.StepNum)
	}
	// Nothing after the failure ran.
	for _, k := range []string{"host.prep", "identity.access",
		"ipv6.disable", "mptcp.disable"} {
		if calls[k] != 0 {
			t.Errorf("%s ran after the failure", k)
		}
	}
	// Steps before it are recorded, the gate is not.
	led = mustReadLedger(t, path)
	if !led.done("binary.install") || !led.done("apt.base") {
		t.Error("pre-failure steps not recorded")
	}
	if led.done("firewall") {
		t.Error("failed gate recorded as done")
	}
}

// ── willRun (wizard screen-skip source) ──────────────────

func TestWillRun(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/ledger.json"
	steps := []InstallStep{
		{Key: "binary.install", Name: "a", Run: plain(func() error { return nil })},
		{Key: "apt.base", Name: "b", Run: plain(func() error { return nil })},
	}
	led := newLedger()
	if err := led.markDone("binary.install", "1.0"); err != nil {
		t.Fatal(err)
	}
	r, err := newStepRunner(steps, "1.0", led, path)
	if err != nil {
		t.Fatal(err)
	}
	if r.willRun("binary.install") {
		t.Error("recorded step reported as will-run")
	}
	if !r.willRun("apt.base") {
		t.Error("unrecorded step reported as skip")
	}
	// Unknown key: conservative side is "will run" (screen shows).
	if !r.willRun("no.such.key") {
		t.Error("unknown key reported as skip")
	}
}
