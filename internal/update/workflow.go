// Package update coordinates durable, user-approved node updates.
package update

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/virtpriv/node/internal/host"
	"github.com/virtpriv/node/internal/update/protocol"
)

// workflowOps marks the actual trust, storage and service boundaries. Tests
// interrupt those boundaries and restart with the last durable record.
type workflowOps struct {
	save func(*job) error
	// identify reads what the node is before any change: whether each affected
	// service runs its installed executable, and the wallet and device identity.
	identify   func(*job) error
	stage      func(*job) error
	capacity   func(*job) error
	discard    func(*job) error
	guard      func([]protocol.Component) error
	stop       func(protocol.Component) error
	install    func(*job) error
	start      func(protocol.Component) error
	running    func(protocol.Component) error
	health     func(*job, protocol.Component) error
	commit     func(*job) error
	backup     func() error
	quarantine func([]protocol.Component) error
}

var now = time.Now

// The steps Node Updates draws. Each run writes the list again from its own
// affected services, so a Retry or a repair release shows its own steps.
const (
	stepCheckNode = "Check the node"
	stepDownload  = "Download and verify release files"
	stepSpace     = "Check free space"
	stepPrepare   = "Prepare services"
	stepInstall   = "Install new files"
	stepFinish    = "Finish VPN update"
)

func stopStep(c protocol.Component) string  { return "Stop " + string(c) }
func startStep(c protocol.Component) string { return "Start and check " + string(c) }

func stepList(affected []protocol.Component) []string {
	steps := []string{stepCheckNode, stepDownload, stepSpace, stepPrepare}
	for _, c := range slices.Backward(affected) {
		steps = append(steps, stopStep(c))
	}
	steps = append(steps, stepInstall)
	for _, c := range affected {
		steps = append(steps, startStep(c))
	}
	return append(steps, stepFinish)
}

// codedError marks an error with the code the worker saves for it. The error
// text stays as it was.
type codedError struct {
	failure protocol.Failure
	err     error
}

func (e *codedError) Error() string { return e.err.Error() }
func (e *codedError) Unwrap() error { return e.err }

func known(code protocol.FailureCode, c protocol.Component, err error) error {
	return &codedError{failure: protocol.Failure{Code: code, Service: c}, err: err}
}

// failureOf returns the code saved with err, or nil for a failure that the
// screen explains with its general advice.
func failureOf(err error) *protocol.Failure {
	if e, ok := errors.AsType[*codedError](err); ok {
		f := e.failure
		return &f
	}
	return nil
}

func runJob(j *job, ops workflowOps) error {
	if !j.active() || j.Phase == "failed" {
		return nil
	}
	affected := j.Affected
	// mark makes step the one in progress. A step that begins gets its time
	// and a journal line, and the steps after it have not begun. Marking the
	// step in progress again keeps its time.
	mark := func(step string) {
		n := slices.Index(j.Steps, step) + 1
		if n < 1 {
			j.StepNumber = 0
			return
		}
		if n == j.StepNumber && n <= len(j.StepTimes) && !j.StepTimes[n-1].IsZero() {
			return
		}
		times := make([]time.Time, len(j.Steps))
		copy(times, j.StepTimes[:min(len(j.StepTimes), n-1)])
		times[n-1] = now().UTC()
		j.StepNumber, j.StepTimes = n, times
		fmt.Fprintf(os.Stderr, "step %d of %d: %s\n", n, len(j.Steps), step)
	}
	// publish records the phase, the step text older screens show, and the
	// step of the list that is now in progress.
	publish := func(phase, step, listed string) error {
		j.Phase, j.Step = phase, step
		mark(listed)
		return ops.save(j)
	}
	if steps := stepList(affected); !slices.Equal(steps, j.Steps) {
		j.Steps, j.StepNumber, j.StepTimes = steps, 0, nil
		mark(steps[0])
	}
	if j.Phase == "committing" {
		mark(stepFinish)
	}
	if err := ops.save(j); err != nil {
		return err
	}
	// saveFailure saves a failure with its code. The code comes from the
	// cause alone, never from the cleanup joined to it.
	saveFailure := func(cause error, f *protocol.Failure) error {
		j.Phase = "failed"
		j.Error, j.Failure = cause.Error(), f
		j.Next, j.Advice = protocol.Advise(f, !j.HostChanges)
		return errors.Join(cause, ops.save(j))
	}
	// refuse records a failure without touching services.
	refuse := func(cause error) error { return saveFailure(cause, failureOf(cause)) }
	fail := func(cause error) error {
		f := failureOf(cause)
		if j.HostChanges && ops.quarantine != nil {
			cause = errors.Join(cause, ops.quarantine(affected))
		}
		return saveFailure(cause, f)
	}
	// Last space check before any service is touched. Services stay exactly
	// as they were found. A refusal returns the downloaded files' space unless
	// Retry still needs them: that is only after a finished download whose
	// host changes have begun. The record is saved first, so a crash cannot
	// leave it promising files that are gone.
	checkSpace := func() error {
		if err := publish(j.Phase, "Check free space", stepSpace); err != nil {
			return err
		}
		err := ops.capacity(j)
		if err == nil {
			return nil
		}
		if j.Completed["staged"] && j.HostChanges {
			return refuse(err)
		}
		delete(j.Completed, "staged")
		return errors.Join(refuse(err), ops.discard(j))
	}
	if j.Phase == "retry" {
		// The installed helper only records the request. Starts are attempted
		// afresh. Downloads and the history of what may have run are kept.
		// The step that failed begins again with its own time when marked.
		j.Started = map[protocol.Component]bool{}
		if n := j.StepNumber; n >= 1 && n <= len(j.StepTimes) {
			j.StepTimes[n-1] = time.Time{}
		}
		j.Phase = "accepted"
		if j.Completed["staged"] {
			j.Phase = "installing"
		}
	}
	if !j.HostChanges && j.Phase == "accepted" {
		// A new job, or a Retry before any host change: the node is still as
		// it was found, so its services can say what they are. The reading is
		// saved with the next phase. A later phase therefore always has one,
		// and a download resumed at boot does not ask services that are still
		// starting. Once host changes have begun the saved identity stands,
		// also when an earlier helper or a failed update recorded it.
		mark(stepCheckNode)
		if err := ops.save(j); err != nil {
			return err
		}
		if err := ops.identify(j); err != nil {
			return refuse(err)
		}
	}
	if !j.Completed["staged"] {
		if err := publish("staging", "Download and verify release files", stepDownload); err != nil {
			return err
		}
		// Staging touches no service, so its failure never quarantines, even
		// in a repair job that starts with host changes already begun.
		if err := ops.stage(j); err != nil {
			return refuse(err)
		}
		if err := checkSpace(); err != nil {
			return err
		}
		j.Completed["staged"] = true
		if err := publish("installing", "Prepare service changes", stepPrepare); err != nil {
			return err
		}
	} else if j.Phase == "installing" {
		// A resumed or retried installation checks again before it stops
		// anything.
		if err := checkSpace(); err != nil {
			return err
		}
	}
	if j.Phase == "installing" {
		// Saved before the first guard. From here Cancel is refused and a
		// failure quarantines the affected services.
		j.HostChanges = true
		if err := publish("installing", "Guard services", stepPrepare); err != nil {
			return err
		}
		if err := ops.guard(affected); err != nil {
			return fail(err)
		}
		// Reverse dependency order: LND stops before its Core backend.
		stopOrder := slices.Clone(affected)
		slices.Reverse(stopOrder)
		for _, c := range stopOrder {
			if err := publish("installing", "Stop "+string(c), stopStep(c)); err != nil {
				return err
			}
			if err := ops.stop(c); err != nil {
				if errors.Is(err, host.ErrUpdateServiceStop) {
					err = known(protocol.FailureStopFailed, c, err)
				}
				return fail(err)
			}
		}
		if err := publish("installing", "Install new files", stepInstall); err != nil {
			return err
		}
		if err := ops.install(j); err != nil {
			return fail(err)
		}
		if err := publish("starting", "Start updated services", stepInstall); err != nil {
			return err
		}
	}
	if j.Phase != "committing" {
		for _, c := range affected {
			// Marked before the check below, so an interrupted start fails on
			// its own step.
			mark(startStep(c))
			if err := ops.save(j); err != nil {
				return err
			}
			if j.Started[c] {
				// An uncertain start is never repeated automatically. If the
				// process survived, verify it. Otherwise require explicit Retry.
				if err := ops.running(c); err != nil {
					return fail(known(protocol.FailureStartInterrupted, c,
						fmt.Errorf("%s startup was interrupted, review the failure and retry: %w", c, err)))
				}
			} else {
				j.Started[c] = true
				j.MayHaveRun[c] = true
				if err := publish("starting", "Start "+string(c), startStep(c)); err != nil {
					return err
				}
				if err := ops.start(c); err != nil {
					return fail(fmt.Errorf("start %s: %w", c, err))
				}
			}
			if err := publish("checking", "Check "+string(c), startStep(c)); err != nil {
				return err
			}
			if err := ops.health(j, c); err != nil {
				return fail(err)
			}
		}
		if err := publish("committing", "Finish VPN update", stepFinish); err != nil {
			return err
		}
	}
	if err := ops.commit(j); err != nil {
		return fail(err)
	}
	// The commit ends with a systemd reload, during which the watcher of
	// LND's channel backup sees no change. LND writes that backup when its
	// wallet unlocks, which is the moment the update waited for. A failed
	// export cannot fail a finished update.
	if err := ops.backup(); err != nil {
		fmt.Fprintln(os.Stderr, "LND backup copy failed after the update:", err)
	}
	j.Error, j.Failure, j.Next, j.Advice = "", nil, "", ""
	if err := publish("complete", "Update complete", stepFinish); err != nil {
		return err
	}
	// Leftover downloads only cost space. They cannot fail a finished update.
	if err := ops.discard(j); err != nil {
		fmt.Fprintln(os.Stderr, "remove staged update files:", err)
	}
	return nil
}
