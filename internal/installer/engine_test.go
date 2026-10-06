// internal/installer/engine_test.go

package installer

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

// testSteps builds a miniature install list with the shapes
// that matter: plain mutations, a gate, and a 3-member
// pipeline group. Fns count executions into calls. The keys are
// the first seven of the real install, in its order, because the
// ledger accepts only step sets a real install can reach.
func testSteps(calls map[string]int) []InstallStep {
	fn := func(key string) func() error {
		return func() error {
			calls[key]++
			return nil
		}
	}
	return []InstallStep{
		{Key: "binary.install", Name: "Installing binary",
			Fn: fn("binary.install")},
		{Key: "apt.base", Name: "Installing base packages",
			Fn: fn("apt.base")},
		{Key: "firewall", Name: "Verifying firewall",
			Kind: StepGate, Fn: fn("firewall")},
		{Key: "host.prep", Group: "pipeline", Name: "Downloading",
			Fn: fn("host.prep")},
		{Key: "identity.access", Group: "pipeline", Name: "Verifying",
			Fn: fn("identity.access")},
		{Key: "service-identities.v1", Group: "pipeline", Name: "Installing",
			Fn: fn("service-identities.v1")},
		{Key: "ipv6.disable", Name: "Disabling IPv6",
			Fn: fn("ipv6.disable")},
	}
}

func testLedger() *installLedger {
	db := 512
	l, err := newLedger(installContext{
		Network: "mainnet", InitialP2PMode: "tor", DbCacheMB: &db,
	})
	if err != nil {
		panic(err)
	}
	return l
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
		"host.prep", "identity.access", "service-identities.v1",
		"ipv6.disable"}

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
				"identity.access", "service-identities.v1", "ipv6.disable"},
		},
		{
			// THE group test: download+verify recorded but the
			// terminal (service-identities.v1) is not, so the whole
			// group re-runs (the workdir died with the old process).
			name: "incomplete group re-runs whole",
			done: []string{"binary.install", "apt.base",
				"firewall", "host.prep", "identity.access"},
			wantRun: []string{"firewall", "host.prep",
				"identity.access", "service-identities.v1", "ipv6.disable"},
		},
		{
			// Terminal recorded: the group is complete even if
			// member entries are absent (only the terminal is
			// consulted for group members).
			name: "group judged by terminal only",
			done: []string{"binary.install", "apt.base",
				"firewall", "service-identities.v1"},
			wantRun: []string{"firewall", "ipv6.disable"},
		},
		{
			name: "unknown ledger keys ignored",
			done: []string{"binary.install", "no.such.step",
				"another.ghost"},
			wantRun: []string{"apt.base", "firewall",
				"host.prep", "identity.access", "service-identities.v1",
				"ipv6.disable"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			led := testLedger()
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

// The gate must re-run even when its own key is recorded —
// a recorded gate means "it held on some earlier pass," which
// is exactly what a gate must never rely on.
func TestPlanGateIgnoresOwnLedgerEntry(t *testing.T) {
	led := testLedger()
	if err := led.markDone("firewall", "0.6.3"); err != nil {
		t.Fatal(err)
	}
	steps := testSteps(map[string]int{})
	plan := planRun(steps, led)
	if !plan[2].Run {
		t.Fatal("gate was ledger-skipped")
	}
}

func TestFilterPhase(t *testing.T) {
	steps := []InstallStep{
		{Key: "a", Phase: PhaseBake},
		{Key: "b", Phase: PhaseFirstBoot},
		{Key: "c", Phase: PhaseBake},
	}
	bake := FilterPhase(steps, PhaseBake)
	if len(bake) != 2 || bake[0].Key != "a" || bake[1].Key != "c" {
		t.Errorf("bake filter = %v", bake)
	}
	allSteps := FilterPhase(steps, PhaseFirstBoot)
	if len(allSteps) != 3 {
		t.Errorf("first-boot filter kept %d of 3", len(allSteps))
	}
}

func TestRunnerSkipDoesNotExecute(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	led := testLedger()
	if err := led.markDone("binary.install", "0.6.3"); err != nil {
		t.Fatal(err)
	}
	if err := led.save(path); err != nil {
		t.Fatal(err)
	}

	calls := map[string]int{}
	r, err := newStepRunner(testSteps(calls), "0.6.3", led, path)
	if err != nil {
		t.Fatal(err)
	}
	skipped, err := r.runIndex(0)
	if err != nil {
		t.Fatalf("runIndex: %v", err)
	}
	if !skipped {
		t.Error("recorded step not skipped")
	}
	if calls["binary.install"] != 0 {
		t.Error("skipped step's Fn executed")
	}
}

func TestRunnerRecordsAfterSuccessOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	calls := map[string]int{}
	steps := testSteps(calls)
	boom := errors.New("boom")
	steps[1].Fn = func() error { return boom }

	led := testLedger()
	r, err := newStepRunner(steps, "0.6.3", led, path)
	if err != nil {
		t.Fatal(err)
	}

	// Success: entry lands on disk.
	if _, err := r.runIndex(0); err != nil {
		t.Fatalf("step 0: %v", err)
	}
	if !mustReadLedger(t, path).done("binary.install") {
		t.Error("completed step not recorded on disk")
	}

	// Failure: no entry, error surfaced.
	_, err = r.runIndex(1)
	if !errors.Is(err, boom) {
		t.Fatalf("step 1 err = %v, want boom", err)
	}
	if mustReadLedger(t, path).done("apt.base") {
		t.Error("failed step recorded as done")
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
	failed[3].Err = errors.New("download failed")
	res = classifyRun(failed, true, true, 3)
	if res.Outcome != RunFailed {
		t.Errorf("failed run classified %v", res.Outcome)
	}
	if res.StepNum != 4 || res.StepName != "Downloading" {
		t.Errorf("failure attributed to %d/%q",
			res.StepNum, res.StepName)
	}
	if res.Err == nil {
		t.Error("failed run lost its error")
	}

	// Quit mid-run: done=false → interrupted, at the current
	// step — never complete.
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
	led := testLedger()
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
	steps[2].Fn = func() error {
		calls["firewall"]++
		return fmt.Errorf("tor not routing")
	}

	led := testLedger()
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
		"service-identities.v1", "ipv6.disable"} {
		if calls[k] != 0 {
			t.Errorf("%s ran after the failure", k)
		}
	}
	// Steps before it are recorded; the gate is not.
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
		{Key: "service-identities.v1", Name: "a", Fn: func() error { return nil }},
		{Key: "btc.install", Name: "b", Fn: func() error { return nil }},
	}
	led := testLedger()
	if err := led.markDone("service-identities.v1", "1.0"); err != nil {
		t.Fatal(err)
	}
	r, err := newStepRunner(steps, "1.0", led, path)
	if err != nil {
		t.Fatal(err)
	}
	if r.willRun("service-identities.v1") {
		t.Error("recorded step reported as will-run")
	}
	if !r.willRun("btc.install") {
		t.Error("unrecorded step reported as skip")
	}
	// Unknown key: conservative side is "will run" (screen shows).
	if !r.willRun("no.such.key") {
		t.Error("unknown key reported as skip")
	}
}
