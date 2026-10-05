// Package update coordinates durable, user-approved node updates.
package update

import (
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/virtpriv/node/internal/update/protocol"
)

// workflowOps marks the actual trust, storage and service boundaries. Tests
// interrupt those boundaries and restart with the last durable record.
type workflowOps struct {
	save       func(*job) error
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
	quarantine func([]protocol.Component) error
}

func runJob(j *job, ops workflowOps) error {
	if !j.active() || j.Phase == "failed" {
		return nil
	}
	affected := j.Affected
	publish := func(phase, step string) error { j.Phase, j.Step = phase, step; return ops.save(j) }
	// refuse records a failure without touching services.
	refuse := func(cause error) error {
		j.Phase = "failed"
		j.Error = cause.Error()
		return errors.Join(cause, ops.save(j))
	}
	fail := func(cause error) error {
		if j.HostChanges && ops.quarantine != nil {
			cause = errors.Join(cause, ops.quarantine(affected))
		}
		return refuse(cause)
	}
	// Last space check before any service is touched. Services stay exactly
	// as they were found. A refusal returns the downloaded files' space unless
	// Retry still needs them: that is only after a finished download whose
	// host changes have begun. The record is saved first, so a crash cannot
	// leave it promising files that are gone.
	checkSpace := func() error {
		if err := publish(j.Phase, "Check free space"); err != nil {
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
		// afresh; downloads and the history of what may have run are kept.
		j.Started = map[protocol.Component]bool{}
		j.Phase = "accepted"
		if j.Completed["staged"] {
			j.Phase = "installing"
		}
	}
	if !j.Completed["staged"] {
		if err := publish("staging", "Download and verify release files"); err != nil {
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
		if err := publish("installing", "Prepare service changes"); err != nil {
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
		if !j.HostChanges {
			// Saved before the first guard. From here Cancel is refused and a
			// failure quarantines the affected services.
			j.HostChanges = true
			if err := publish("installing", "Guard services"); err != nil {
				return err
			}
		}
		if err := ops.guard(affected); err != nil {
			return fail(err)
		}
		// Reverse dependency order: LND stops before its Core backend.
		stopOrder := slices.Clone(affected)
		slices.Reverse(stopOrder)
		for _, c := range stopOrder {
			if err := publish("installing", "Stop "+string(c)); err != nil {
				return err
			}
			if err := ops.stop(c); err != nil {
				return fail(err)
			}
		}
		if err := ops.install(j); err != nil {
			return fail(err)
		}
		if err := publish("starting", "Start updated services"); err != nil {
			return err
		}
	}
	if j.Phase != "committing" {
		for _, c := range affected {
			if j.Started[c] {
				// An uncertain start is never repeated automatically. If the
				// process survived, verify it; otherwise require explicit Retry.
				if err := ops.running(c); err != nil {
					return fail(fmt.Errorf("%s startup was interrupted; inspect the failure and retry: %w", c, err))
				}
			} else {
				j.Started[c] = true
				j.MayHaveRun[c] = true
				if err := publish("starting", "Start "+string(c)); err != nil {
					return err
				}
				if err := ops.start(c); err != nil {
					return fail(fmt.Errorf("start %s: %w", c, err))
				}
			}
			if err := publish("checking", "Check "+string(c)); err != nil {
				return err
			}
			if err := ops.health(j, c); err != nil {
				return fail(err)
			}
		}
		if err := publish("committing", "Finish VPN update"); err != nil {
			return err
		}
	}
	if err := ops.commit(j); err != nil {
		return fail(err)
	}
	j.Error = ""
	if err := publish("complete", "Update complete; reopen the TUI"); err != nil {
		return err
	}
	// Leftover downloads only cost space. They cannot fail a finished update.
	if err := ops.discard(j); err != nil {
		fmt.Fprintln(os.Stderr, "remove staged update files:", err)
	}
	return nil
}
