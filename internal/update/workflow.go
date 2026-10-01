// Package update coordinates durable, user-approved node updates.
package update

import (
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/virtualprivatenode/vpn/internal/update/protocol"
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
		if j.Completed["staged"] && ops.quarantine != nil {
			cause = errors.Join(cause, ops.quarantine(affected))
		}
		return refuse(cause)
	}
	if !j.Completed["staged"] {
		if err := publish("staging", "Download and verify release files"); err != nil {
			return err
		}
		if err := ops.stage(j); err != nil {
			return fail(err)
		}
		// Last space check before any service is touched. A refusal here
		// returns the downloaded files' space and can still be cancelled.
		if err := ops.capacity(j); err != nil {
			return refuse(errors.Join(err, ops.discard(j)))
		}
		j.Completed["staged"] = true
		if err := publish("installing", "Prepare service changes"); err != nil {
			return err
		}
	} else if j.Phase == "installing" {
		// A resumed or retried installation checks space again before it
		// stops anything. Services stay exactly as they were found.
		if err := ops.capacity(j); err != nil {
			return refuse(err)
		}
	}
	if j.Phase == "installing" {
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
