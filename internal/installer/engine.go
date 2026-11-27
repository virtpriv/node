// internal/installer/engine.go

package installer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/virtpriv/node/internal/logger"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/steps"
	"github.com/virtpriv/node/internal/theme"
)

// ── Install engine core ──────────────────────────────────
//
// The interactive install and the unattended install run their steps
// through one step runner, which decides what a rerun repeats, records each
// finished step in the ledger and logs it. The steps themselves run through
// internal/steps. The install screen only shows progress.
//
// What a rerun repeats:
//
//   - A step that changes the machine runs once. Its recorded completion is
//     trusted. The ledger records that a step finished, not that its result
//     is still in place, see the ledger.go header.
//   - A check (a gate) runs on every pass, because it checks the machine as
//     it is now. So no package can come through Tor in a pass that did not
//     check Tor's routing itself.

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
	// StepSkipped renders a resume skip. The ledger records a
	// prior completion, and the step did not execute this pass.
	StepSkipped
)

type InstallStep struct {
	// Key is the stable, versionless ledger key ("tor.configure",
	// "apt.base"). Names carry copy edits. Keys identify the step across
	// binary versions.
	Key string
	// Name is display-only.
	Name string
	// Run does the step. ctx ends when the owner asks for a pause. A step
	// looks at it only where it waits and never hands it to a command, so a
	// command that started runs to its end. say tells the owner what the
	// step is doing now, in one short line.
	Run  func(ctx context.Context, say func(string)) error
	Kind StepKind

	Status StepStatus
	Err    error
}

// plain makes a step of work that neither waits nor says what it is doing.
func plain(fn func() error) func(context.Context, func(string)) error {
	return func(context.Context, func(string)) error { return fn() }
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

// planRun decides, for every step, run or skip. It holds every resume
// decision as one function with no I/O and no clock, tested per scenario.
func planRun(steps []InstallStep, led *installLedger) []stepPlan {
	plan := make([]stepPlan, len(steps))
	for i, s := range steps {
		switch {
		case s.Kind == StepGate:
			plan[i] = stepPlan{Run: true,
				Reason: "gate: re-verified every pass"}
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

// stepRunner is the install's side of internal/steps, its record and its
// log. It hands the shared runner the steps this run does and reports each
// step, the skipped ones in their place, to the front end.
type stepRunner struct {
	steps      []InstallStep
	plan       []stepPlan
	ledger     *installLedger
	ledgerPath string
	version    string
	// show tells the front end that step i changed. It must not block.
	show func(i int, status StepStatus, err error)
	// todo holds the steps this run does, by their place in the whole list.
	todo []int
	// told is the first step the front end has not heard about.
	told int

	// doingStep and doing are the step that last said a line and that line,
	// empty once the step has ended. The install screen reads them.
	mu        sync.Mutex
	doingStep int
	doing     string
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
		show:       func(int, StepStatus, error) {},
	}, nil
}

// run does the steps the plan asks for, in order, until one fails, a pause
// asked through ctx is honored or all are done. Every step leaves a log
// line when it starts and when it is skipped, completes, fails or is
// stopped. A step is recorded only after it returned without error, and a
// record that cannot be written ends the run, because a later step must not
// build on a step the next run would not know about. A process that is
// killed or loses its terminal leaves no such line, so the log then ends at
// "starting" or at the last line the step said.
func (r *stepRunner) run(ctx context.Context) RunResult {
	r.todo, r.told = nil, 0
	var list []steps.Step
	for i := range r.steps {
		if !r.plan[i].Run {
			continue
		}
		s := &r.steps[i]
		r.todo = append(r.todo, i)
		list = append(list, steps.Step{Name: s.Name, Run: func(ctx context.Context) error {
			return s.Run(ctx, func(text string) { r.say(i, text) })
		}})
	}
	total := len(r.steps)
	res := steps.Run(ctx, list, r)
	switch res.Outcome {
	case steps.Failed:
		return classifyRun(r.steps, false, true, r.todo[res.Index])
	case steps.Stopped:
		return classifyRun(r.steps, false, false, r.todo[res.Index])
	}
	r.skipTo(total)
	return classifyRun(r.steps, true, false, total)
}

// skipTo reports the steps before step i that this run skips.
func (r *stepRunner) skipTo(i int) {
	for ; r.told < i; r.told++ {
		s := &r.steps[r.told]
		logger.Install("step %d/%d skipped: %s (%s)",
			r.told+1, len(r.steps), s.Name, r.plan[r.told].Reason)
		s.Status = StepSkipped
		r.show(r.told, StepSkipped, nil)
	}
}

// Begin, End and Log make the stepRunner the shared runner's sink. Their k
// counts the steps this run does.

func (r *stepRunner) Begin(k int) error {
	i := r.todo[k]
	r.skipTo(i)
	r.told = i + 1
	r.steps[i].Status = StepRunning
	r.show(i, StepRunning, nil)
	return nil
}

// End records a step that is done. A stopped step is not recorded and goes
// back to not begun, so the next run does it. A record that cannot be
// written is handed back, which fails the step.
func (r *stepRunner) End(k int, o steps.Outcome, err error) error {
	i := r.todo[k]
	s := &r.steps[i]
	r.setDoing(i, "")
	var recordErr error
	status := StepFailed
	switch o {
	case steps.Stopped:
		status = StepPending
	case steps.Done:
		if recordErr = r.record(s.Key); recordErr == nil {
			status = StepDone
		}
		err = recordErr
	}
	s.Status, s.Err = status, err
	r.show(i, status, err)
	return recordErr
}

func (r *stepRunner) record(key string) error {
	if err := r.ledger.markDone(key, r.version); err != nil {
		return err
	}
	if err := r.ledger.save(r.ledgerPath); err != nil {
		return fmt.Errorf(
			"step completed but install ledger publication failed: %w", err)
	}
	return nil
}

// Log numbers a line by the whole list, so a rerun that skips steps still
// logs "step 9/17".
func (r *stepRunner) Log(k int, text string) {
	logger.Install("step %d/%d %s", r.todo[k]+1, len(r.steps), text)
}

// say is what step i tells the owner it is doing now. The line goes to the
// log, and the install screen reads the newest one.
func (r *stepRunner) say(i int, text string) {
	r.setDoing(i, text)
	logger.Install("step %d/%d: %s", i+1, len(r.steps), text)
}

func (r *stepRunner) setDoing(i int, text string) {
	r.mu.Lock()
	r.doingStep, r.doing = i, text
	r.mu.Unlock()
}

// Doing returns the step that last said a line, counted from 0, and that
// line, empty once the step has ended.
func (r *stepRunner) Doing() (step int, text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.doingStep, r.doing
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
	StepName string // the step that failed or was not reached, "" if complete
	StepNum  int    // counted from 1
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

// StopError reports an install that did not finish. NotStarted means this
// run began no step, because nothing was ever recorded or the start was
// refused. Paused means it stopped with no step failed, between
// two steps or in a step's wait, and a step stopped in its wait is not
// recorded. DoneNum and DoneName name the last step recorded, and the step
// named is the one the next run does.
// Otherwise the step named failed with Err, or, with no step named,
// finishing the install failed. StartErr is why the last start was refused
// when the owner then left. RootLoginOff is set once root's SSH login is
// off, RootLoginMayBeOff when the SSH step failed after it may have turned
// it off. Again is the command that runs the install again, see
// againCommand.
type StopError struct {
	Again             string
	NotStarted        bool
	Paused            bool
	StepName          string
	StepNum, Total    int
	DoneNum           int
	DoneName          string
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
		return fmt.Sprintf("install paused after step %d of %d (%s)", e.DoneNum, e.Total, e.DoneName)
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

// runsInstalledVPN reports whether the running program is the vpn that step
// 1 installs. Only then is it certain that the vpn command exists.
func runsInstalledVPN() bool {
	self, err := os.Executable()
	if err != nil {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	return self == paths.BinaryPath
}

// againCommand is the command that runs the install again, named the way
// this run was started, or "" while it is not certain that the vpn command
// exists. That is certain once step 1 is recorded or when the running
// program is the installed vpn. sudo is named when this run came through
// sudo, when step 2, which installs it, is recorded, or when the caller is
// not root. Otherwise the caller is root on a machine that may have no sudo
// yet, and types the command as it is. An unattended run is continued
// unattended, since a plain run is refused while a generated password is
// still owed.
func againCommand(unattended bool, led *installLedger) string {
	if !runsInstalledVPN() && (led == nil || !led.done(vpnProgramStep)) {
		return ""
	}
	cmd := "vpn install"
	if os.Geteuid() != 0 || os.Getenv("SUDO_UID") != "" || (led != nil && led.done(basePackagesStep)) {
		cmd = "sudo " + cmd
	}
	if unattended {
		cmd += " --unattended"
	}
	return cmd
}

// Lines are what the shell shows after the install screen closes, and what
// an unattended install ends with. A command to type stands alone on the
// last line, after an empty one. It is named only when vpn is installed for
// certain. Otherwise the owner runs the downloaded program again. A failed
// step says where the details are and how to continue.
func (e *StopError) Lines() []string {
	var out []string
	if e.StartErr != nil {
		out = append(out, RefusedStartText(e.StartErr))
	}
	var run string
	facts, isFacts := errors.AsType[factsFailed](e.Err)
	switch {
	case e.NotStarted:
		if e.StartErr == nil {
			out = append(out, "The install did not start.")
		}
		if e.Again != "" {
			run = "To try again, run:"
		} else {
			out = append(out, "To start it, run the installer again.")
		}
	case e.Paused:
		out = append(out, fmt.Sprintf("Install paused after step %d of %d (%s).", e.DoneNum, e.Total, e.DoneName))
		run = fmt.Sprintf("To continue with step %d (%s), run:", e.StepNum, e.StepName)
	case e.StepName != "":
		// A command's output ends in a line break, which would print an
		// empty line here.
		out = append(out, "Failed: "+strings.TrimSpace(e.Error()), "Details are in "+paths.LogFile+".")
		if e.Again != "" {
			run = "To continue the install, run:"
		} else {
			out = append(out, "To continue the install, run the installer again.")
		}
	case isFacts:
		// The shell indents every line.
		report, _ := formatChecks("", "Checking this machine before finishing", facts.results)
		out = append(out, strings.TrimRight(report, "\n"), "", "The install did not finish.")
		if e.Again != "" {
			run = "Once it is fixed, run:"
		}
	default:
		out = append(out, "Failed: "+e.Error())
		if e.Again != "" {
			run = "To try again, run:"
		}
	}
	if e.RootLoginOff {
		out = append(out, RootLoginOffNotice)
	} else if e.RootLoginMayBeOff {
		out = append(out, RootLoginMayBeOffNotice)
	}
	if run != "" {
		out = append(out, run, "", e.Again)
	}
	return out
}

// PrintStop shows in the shell why an install ended without finishing, once
// the install screen has closed. The reason is a pause, a refusal or a
// failure.
func PrintStop(err error) {
	lines := []string{"Failed: " + err.Error()}
	var stop *StopError
	if errors.As(err, &stop) {
		lines = stop.Lines()
	}
	fmt.Fprintln(os.Stderr)
	theme.PrintShell(os.Stderr, lines)
}

// nextStep returns the step the next run of steps against led starts with:
// the first step the planner would run, leaving out a gate that already
// passed, since gates run again on every pass anyway. ok is false when every
// step is recorded.
func nextStep(steps []InstallStep, led *installLedger) (int, bool) {
	for i, p := range planRun(steps, led) {
		if p.Run && (steps[i].Kind != StepGate || !led.done(steps[i].Key)) {
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
		// Marked failed with no failed step recorded. That cannot happen,
		// and the safe answer is to never call it complete.
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

// RunInstallUnattended runs the steps without the install screen for
// `vpn install --unattended`, with the same runner, ledger and log, and one
// line per step on stdout. It has no pause. A process that is killed leaves
// its running step unrecorded, so the next run does that step again.
func RunInstallUnattended(
	steps []InstallStep, version string, ledger *installLedger,
	ledgerPath string,
) (RunResult, error) {
	r, err := newStepRunner(steps, version, ledger, ledgerPath)
	if err != nil {
		return RunResult{}, err
	}
	total := len(steps)
	// On a terminal the running step shows [....] until it ends, then its
	// line is written again in place. Saved to a file, each line is written
	// once, when its step ends, so the file holds no rewrites.
	live := term.IsTerminal(int(os.Stdout.Fd()))
	r.show = func(i int, status StepStatus, err error) {
		n, name := i+1, steps[i].Name
		if live && (status == StepDone || status == StepFailed) {
			fmt.Print("\r")
		}
		switch status {
		case StepRunning:
			if live {
				fmt.Printf("  [....] [%d/%d] %s", n, total, name)
			}
		case StepFailed:
			// Why it failed follows once, in the lines the run ends with.
			fmt.Printf("  [FAIL] [%d/%d] %s\n", n, total, name)
		case StepSkipped:
			fmt.Printf("  [skip] [%d/%d] %s\n", n, total, name)
		case StepDone:
			fmt.Printf("  [done] [%d/%d] %s\n", n, total, name)
		}
	}
	return r.run(context.Background()), nil
}

// willRun suppresses the password screen only when the planner proves its
// step will be skipped. An absent key conservatively keeps the screen.
func (r *stepRunner) willRun(key string) bool {
	for i, s := range r.steps {
		if s.Key == key {
			return r.plan[i].Run
		}
	}
	return true
}
