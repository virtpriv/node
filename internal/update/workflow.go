// Package update coordinates durable, user-approved node updates.
package update

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/virtpriv/node/internal/update/protocol"
)

// workflowOps marks the actual trust and storage boundaries. Tests interrupt
// those boundaries and restart with the last durable record.
type workflowOps struct {
	save func(*job) error
	// check reads whether the node is still the one the owner reviewed the
	// update for. It changes nothing.
	check func(*job) error
	// capacity reads whether the new program fits. It changes nothing.
	capacity func(*job) error
	// commit replaces the installed program with the approved one.
	commit func(*job) error
}

var now = time.Now

// The steps Node Updates draws. Each run writes the list again, so a Retry
// or a repair release shows its own steps.
const (
	stepCheckNode = "Check the node"
	stepSpace     = "Check free space"
	stepFinish    = "Finish VPN update"
)

func stepList() []string { return []string{stepCheckNode, stepSpace, stepFinish} }

// runJob runs an update from its saved record. Every run does the same three
// steps: it checks the node, checks the free space and replaces the program.
// The two checks change nothing, so a run after a power cut or a Retry does
// them again, and replacing the program again with the same bytes is safe.
func runJob(j *job, ops workflowOps) error {
	if !j.active() || j.Phase == "failed" {
		return nil
	}
	// mark makes step the one in progress. A step that begins gets its start
	// and a journal line, the step in progress before it ends then, and the
	// steps after it have not begun. Steps before it keep their times.
	mark := func(step string) {
		n := slices.Index(j.Steps, step) + 1
		at := now().UTC()
		starts, ends := make([]time.Time, len(j.Steps)), make([]time.Time, len(j.Steps))
		copy(starts, j.StepStarts[:min(len(j.StepStarts), n-1)])
		copy(ends, j.StepEnds[:min(len(j.StepEnds), n-1)])
		if was := j.StepNumber; was >= 1 && was < n && !starts[was-1].IsZero() && ends[was-1].IsZero() {
			ends[was-1] = at
		}
		starts[n-1] = at
		j.StepNumber, j.StepStarts, j.StepEnds = n, starts, ends
		fmt.Fprintf(os.Stderr, "step %d of %d: %s\n", n, len(j.Steps), step)
	}
	// end notes that the step in progress finished or failed.
	end := func() {
		if n := j.StepNumber; n >= 1 && n <= len(j.StepEnds) {
			j.StepEnds[n-1] = now().UTC()
		}
	}
	// begin saves the step that is now in progress, with the step text a
	// screen without the list shows.
	begin := func(step string) error {
		j.Step = step
		mark(step)
		return ops.save(j)
	}
	// fail saves a failure with the screen's next step and words for it,
	// which say whether the program may have been replaced.
	fail := func(cause error) error {
		end()
		j.Phase = "failed"
		j.Error = cause.Error()
		j.Next, j.Advice = protocol.Advise(!j.HostChanges)
		return errors.Join(cause, ops.save(j))
	}

	// This run's own list and its own times. A list another worker wrote, or
	// the times of a run that was cut or failed, would mark the wrong step.
	j.Steps, j.StepNumber, j.StepStarts, j.StepEnds = stepList(), 0, nil, nil
	// The installed side only records a Retry. Once the program may have been
	// replaced, the record keeps saying so.
	j.Phase = "accepted"
	if j.HostChanges {
		j.Phase = "committing"
	}

	if err := begin(stepCheckNode); err != nil {
		return err
	}
	if err := ops.check(j); err != nil {
		return fail(err)
	}
	if err := begin(stepSpace); err != nil {
		return err
	}
	if err := ops.capacity(j); err != nil {
		return fail(err)
	}
	// Saved before the program is replaced. From here Cancel is refused.
	j.Phase, j.HostChanges = "committing", true
	if err := begin(stepFinish); err != nil {
		return err
	}
	if err := ops.commit(j); err != nil {
		return fail(err)
	}
	j.Error, j.Next, j.Advice = "", "", ""
	j.Ended = time.Now().UTC()
	end()
	j.Phase, j.Step = "complete", "Update complete"
	return ops.save(j)
}
