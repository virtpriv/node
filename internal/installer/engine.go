// internal/installer/engine.go

package installer

import (
	"fmt"
	"unicode"
	"unicode/utf8"

	"github.com/virtpriv/node/internal/logger"
	"github.com/virtpriv/node/internal/paths"
)

// ── Install engine core ──────────────────────────────────
//
// The interactive session and unattended loop use the same
// step runner for resume decisions, ledger recording, and
// logging. The TUI observes progress without executing steps.
//
// Resume rules, in one place:
//
//   - MUTATION steps trust the ledger: a recorded completion
//     is skipped. The ledger records completion, not present
//     correctness, see the ledger.go header for the residual.
//   - GATE steps assert a property of the world RIGHT NOW
//     (Tor routing) and are ALWAYS re-executed, never
//     ledger-skipped. No download step can run in a pass whose
//     Tor routing was not verified in that same pass, by step
//     type, not by ordering luck.
//   - GROUP steps pass ephemeral material hand-to-hand (a
//     MkdirTemp workdir captured in closures: download →
//     verify → install). The workdir does not survive the
//     process, so a group is ATOMIC for resume: it counts as
//     complete only if its TERMINAL step (last member in list
//     order) is recorded. Otherwise every member re-runs.
//     Re-running a download pipeline is self-verifying by
//     construction (fresh GPG + checksum).

// StepKind classifies a step for resume.
type StepKind int

const (
	// StepMutation changes the box. Its recorded completion is
	// trusted for skipping on resume.
	StepMutation StepKind = iota
	// StepGate asserts the world now. It re-runs on every pass
	// regardless of the ledger.
	StepGate
)

type StepStatus int

const (
	StepPending StepStatus = iota
	StepRunning
	StepDone
	StepFailed
	// StepSkipped renders a resume skip: the ledger records a
	// prior completion, the step did not execute this pass.
	StepSkipped
)

type InstallStep struct {
	// Key is the stable, versionless ledger key ("tor.install",
	// "btc.download"). Names carry versions and copy edits. Keys
	// identify the step across binary versions.
	Key string
	// Name is display-only.
	Name  string
	Fn    func() error
	Kind  StepKind
	Group string // resume-atomic pipeline, "" for self-contained

	Status StepStatus
	Err    error
}

// validateSteps asserts the invariants the planner relies on:
// every key non-empty and unique. Programmer error, not
// environment, so it is checked once at engine start and
// returned as an error rather than silently misplanned.
func validateSteps(steps []InstallStep) error {
	seen := make(map[string]bool, len(steps))
	for i, s := range steps {
		if s.Key == "" {
			return fmt.Errorf(
				"install step %d (%s) has no key", i+1, s.Name)
		}
		if seen[s.Key] {
			return fmt.Errorf(
				"install step key %q duplicated", s.Key)
		}
		seen[s.Key] = true
	}
	return nil
}

// stepPlan is the planner's decision for one step.
type stepPlan struct {
	Run    bool
	Reason string // human-readable cause, logged verbatim
}

// planRun decides, for every step, run or skip. It is the
// whole resume ruling as one pure function, unit-tested per
// scenario, with no I/O and no clock.
func planRun(steps []InstallStep, led *installLedger) []stepPlan {
	// Terminal member of each group = last in list order.
	terminal := map[string]string{} // group → terminal key
	for _, s := range steps {
		if s.Group != "" {
			terminal[s.Group] = s.Key
		}
	}

	plan := make([]stepPlan, len(steps))
	for i, s := range steps {
		switch {
		case s.Kind == StepGate:
			plan[i] = stepPlan{Run: true,
				Reason: "gate: re-verified every pass"}
		case s.Group != "":
			tk := terminal[s.Group]
			if led.done(tk) {
				e := led.Steps[tk]
				plan[i] = stepPlan{Run: false, Reason: fmt.Sprintf(
					"group %q complete (terminal %s done by v%s at %s)",
					s.Group, tk, e.Version, e.CompletedAt)}
			} else {
				plan[i] = stepPlan{Run: true, Reason: fmt.Sprintf(
					"group %q incomplete: re-runs whole "+
						"(ephemeral workdir)", s.Group)}
			}
		case led.done(s.Key):
			e := led.Steps[s.Key]
			plan[i] = stepPlan{Run: false, Reason: fmt.Sprintf(
				"completed by v%s at %s", e.Version, e.CompletedAt)}
		default:
			plan[i] = stepPlan{Run: true, Reason: "not yet completed"}
		}
	}
	return plan
}

// ── Runner ───────────────────────────────────────────────

type stepRunner struct {
	steps      []InstallStep
	plan       []stepPlan
	ledger     *installLedger
	ledgerPath string
	version    string
}

func newStepRunner(
	steps []InstallStep, version string, led *installLedger,
	ledgerPath string,
) (*stepRunner, error) {
	if err := validateSteps(steps); err != nil {
		return nil, err
	}
	if led == nil {
		return nil, fmt.Errorf("install ledger is required")
	}
	plan := planRun(steps, led)
	toRun := 0
	for _, p := range plan {
		if p.Run {
			toRun++
		}
	}
	if toRun < len(steps) {
		logger.Install(
			"resume: %d/%d steps already complete, running %d",
			len(steps)-toRun, len(steps), toRun)
	}
	return &stepRunner{
		steps:      steps,
		plan:       plan,
		ledger:     led,
		ledgerPath: ledgerPath,
		version:    version,
	}, nil
}

// runIndex executes one step per the plan. Every path leaves a
// log line (start, skip, complete, FAILED), so a run's trail
// in vpn.log never just stops. The ledger entry is written only after Fn returns nil.
// A publication failure stops the run immediately: later steps must not
// advance past a mutation whose completion is not durable for the next pass.
func (r *stepRunner) runIndex(i int) (skipped bool, err error) {
	s := &r.steps[i]
	n, total := i+1, len(r.steps)
	if !r.plan[i].Run {
		logger.Install("step %d/%d skipped: %s (%s)",
			n, total, s.Name, r.plan[i].Reason)
		return true, nil
	}
	logger.Install("step %d/%d starting: %s", n, total, s.Name)
	if err := s.Fn(); err != nil {
		logger.Install("step %d/%d FAILED: %s: %v",
			n, total, s.Name, err)
		return false, err
	}
	if err := r.ledger.markDone(s.Key, r.version); err != nil {
		return false, err
	}
	if err := r.ledger.save(r.ledgerPath); err != nil {
		return false, fmt.Errorf(
			"step completed but install ledger publication failed: %w", err)
	}
	logger.Install("step %d/%d complete: %s", n, total, s.Name)
	return false, nil
}

// ── Outcomes ─────────────────────────────────────────────

// RunOutcome distinguishes the three ways a runner ends. Terminal lifecycle
// status is derived only from RunComplete plus successful finalization.
type RunOutcome int

const (
	RunComplete RunOutcome = iota
	RunFailed
	RunInterrupted
)

type RunResult struct {
	Outcome  RunOutcome
	StepName string // failed / interrupted-at step ("" if complete)
	StepNum  int    // 1-based
	Total    int
	Err      error // the failed step's error
}

// rootLoginOffStep is the step after which root can no longer log in over SSH.
const rootLoginOffStep = "ssh.harden"

// RootLoginOffNotice tells the owner how to get back in once root's SSH login
// is off.
const RootLoginOffNotice = "Root can no longer log in over SSH, so if you close this terminal, log in as " +
	paths.AdminUser + " first."

// RootLoginMayBeOffNotice is for an SSH step that failed after it may have
// written the setting that turns root's SSH login off.
const RootLoginMayBeOffNotice = "Root's SSH login may already be off, so keep this terminal open and check in a new terminal that you can log in as " +
	paths.AdminUser + "."

// StopError reports an install that did not finish. NotStarted means no step
// was ever recorded. Paused means it stopped between steps with no step
// failed, and the step named is the one the next run does. Otherwise the
// step named failed with Err, or, with no step named, finishing the install
// failed. StartErr is why the last start was refused when the owner then
// left. RootLoginOff is set once root's SSH login is off, RootLoginMayBeOff
// when the SSH step failed after it may have turned it off.
type StopError struct {
	NotStarted        bool
	Paused            bool
	StepName          string
	StepNum, Total    int
	Err               error
	StartErr          error
	RootLoginOff      bool
	RootLoginMayBeOff bool
}

func (e *StopError) Error() string {
	switch {
	case e.NotStarted && e.StartErr != nil:
		return fmt.Sprintf("install did not start: %v", e.StartErr)
	case e.NotStarted:
		return "install did not start"
	case e.Paused:
		return fmt.Sprintf("install paused at step %d of %d (%s)", e.StepNum, e.Total, e.StepName)
	case e.StepName == "":
		return e.Err.Error()
	}
	return fmt.Sprintf("%s: %v", e.StepName, e.Err)
}

func (e *StopError) Unwrap() []error {
	var out []error
	for _, err := range []error{e.Err, e.StartErr} {
		if err != nil {
			out = append(out, err)
		}
	}
	return out
}

// RefusedStartText says that the install did not start and why, in the
// words of the error that refused it. The screen and the shell add what to
// do next.
func RefusedStartText(err error) string {
	why := err.Error()
	r, n := utf8.DecodeRuneInString(why)
	return "The install did not start. " + string(unicode.ToUpper(r)) + why[n:] + "."
}

// Lines are what the shell shows after the install screen closes.
func (e *StopError) Lines() []string {
	var out []string
	if e.StartErr != nil {
		out = append(out, RefusedStartText(e.StartErr))
	}
	switch {
	case e.NotStarted && e.StartErr != nil:
		out = append(out, "Run sudo vpn install to try again.")
	case e.NotStarted:
		out = append(out, "The install did not start.", "Run sudo vpn install to start it.")
	case e.Paused:
		out = append(out, fmt.Sprintf("Install paused at step %d of %d (%s).", e.StepNum, e.Total, e.StepName),
			"Run sudo vpn install to continue.")
	default:
		out = append(out, "Failed: "+e.Error())
	}
	if e.RootLoginOff {
		out = append(out, RootLoginOffNotice)
	} else if e.RootLoginMayBeOff {
		out = append(out, RootLoginMayBeOffNotice)
	}
	return out
}

// nextStep returns the step the next run of steps against led starts with:
// the first step the planner would run, leaving out gates, which run on
// every pass anyway. A group whose last member is not recorded runs again
// from its first member. ok is false when every step is recorded.
func nextStep(steps []InstallStep, led *installLedger) (int, bool) {
	for i, p := range planRun(steps, led) {
		if p.Run && steps[i].Kind != StepGate {
			return i, true
		}
	}
	return 0, false
}

// classifyRun describes execution progress. RunComplete means every step
// finished. Terminal lifecycle publication remains a separate requirement.
func classifyRun(
	steps []InstallStep, done, failed bool, current int,
) RunResult {
	total := len(steps)
	if failed {
		for i, s := range steps {
			if s.Status == StepFailed {
				return RunResult{
					Outcome:  RunFailed,
					StepName: s.Name,
					StepNum:  i + 1,
					Total:    total,
					Err:      s.Err,
				}
			}
		}
		// Marked failed with no failed step recorded. Impossible
		// by construction, so refuse to call it complete
		// (fail-safe direction).
		return RunResult{Outcome: RunFailed, Total: total,
			Err: fmt.Errorf("install failed (step unknown)")}
	}
	if done {
		return RunResult{Outcome: RunComplete, Total: total}
	}
	name := ""
	if current >= 0 && current < total {
		name = steps[current].Name
	}
	return RunResult{
		Outcome:  RunInterrupted,
		StepName: name,
		StepNum:  current + 1,
		Total:    total,
	}
}

// ── Unattended front-end ─────────────────────────────────

// RunInstallUnattended executes steps with no TUI: one plain
// line per step to stdout, same runner, same ledger, same log
// trail. Its consumer is `vpn install --unattended`.
// Interruption needs no handling here: a killed process leaves
// the ledger truthful by write ordering.
func RunInstallUnattended(
	steps []InstallStep, version string, ledger *installLedger,
	ledgerPath string,
) (RunResult, error) {
	r, err := newStepRunner(steps, version, ledger, ledgerPath)
	if err != nil {
		return RunResult{}, err
	}
	total := len(steps)
	for i := range steps {
		skipped, err := r.runIndex(i)
		switch {
		case err != nil:
			steps[i].Status = StepFailed
			steps[i].Err = err
			fmt.Printf("  [FAIL] [%d/%d] %s: %v\n",
				i+1, total, steps[i].Name, err)
			return classifyRun(steps, false, true, i), nil
		case skipped:
			steps[i].Status = StepSkipped
			fmt.Printf("  [skip] [%d/%d] %s\n",
				i+1, total, steps[i].Name)
		default:
			steps[i].Status = StepDone
			fmt.Printf("  [done] [%d/%d] %s\n",
				i+1, total, steps[i].Name)
		}
	}
	return classifyRun(steps, true, false, total), nil
}

// willRun suppresses a decision screen only when the planner proves its
// step will be skipped. An absent key conservatively keeps the screen.
func (r *stepRunner) willRun(key string) bool {
	for i, s := range r.steps {
		if s.Key == key {
			return r.plan[i].Run
		}
	}
	return true
}
