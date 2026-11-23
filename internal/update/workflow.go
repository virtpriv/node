// Package update coordinates durable, user-approved node updates.
package update

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/virtpriv/node/internal/steps"
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

// runJob runs an update from its saved record. Every run does the same three
// steps: it checks the node, checks the free space and replaces the program.
// The two checks change nothing, so a run after a power cut or a Retry does
// them again, and replacing the program again with the same bytes is safe.
func runJob(j *job, ops workflowOps) error {
	if !j.active() || j.Phase == "failed" {
		return nil
	}
	// This run's own list and its own times. A list another worker wrote, or
	// the times of a run that was cut or failed, would mark the wrong step.
	list := []steps.Step{
		{Name: stepCheckNode, Run: func(context.Context) error { return ops.check(j) }},
		{Name: stepSpace, Run: func(context.Context) error { return ops.capacity(j) }},
		{Name: stepFinish, Run: func(context.Context) error { return ops.commit(j) }},
	}
	j.Steps, j.StepNumber = nil, 0
	for _, s := range list {
		j.Steps = append(j.Steps, s.Name)
	}
	j.StepStarts, j.StepEnds = make([]time.Time, len(list)), make([]time.Time, len(list))
	// The installed side only records a Retry. Once the program may have been
	// replaced, the record keeps saying so.
	j.Phase = "accepted"
	if j.HostChanges {
		j.Phase = "committing"
	}
	// Nothing stops an update between its steps, so its context never ends.
	res := steps.Run(context.Background(), list, &workerSink{j: j, save: ops.save})
	if res.Outcome != steps.Done {
		return res.Err
	}
	j.Error, j.Next, j.Advice = "", "", ""
	j.Ended = time.Now().UTC()
	j.Phase, j.Step = "complete", "Update complete"
	return ops.save(j)
}

// workerSink is the update's side of internal/steps: its record and its
// journal lines.
type workerSink struct {
	j    *job
	save func(*job) error
}

// Begin saves the step that is now in progress, with the step text a screen
// without the list shows. The last step replaces the program. It is saved
// together with the word that the machine may now change, so the program is
// never replaced without that on record. From there Cancel is refused.
func (s *workerSink) Begin(i int) error {
	j := s.j
	if i == len(j.Steps)-1 {
		j.Phase, j.HostChanges = "committing", true
	}
	j.Step, j.StepNumber, j.StepStarts[i] = j.Steps[i], i+1, now().UTC()
	return s.save(j)
}

// End notes when the step finished or failed. A finished step's end is
// saved with the next step's start, or with the update's end. A failure is
// saved at once, with the screen's next step and words for it, which say
// whether the program may have been replaced.
func (s *workerSink) End(i int, o steps.Outcome, err error) error {
	j := s.j
	j.StepEnds[i] = now().UTC()
	if o != steps.Failed {
		return nil
	}
	j.Phase = "failed"
	j.Error = err.Error()
	j.Next, j.Advice = protocol.Advise(!j.HostChanges)
	return s.save(j)
}

// Log writes a line to the update unit's journal, which the l key on Node
// Updates opens.
func (s *workerSink) Log(i int, text string) {
	fmt.Fprintf(os.Stderr, "step %d/%d %s\n", i+1, len(s.j.Steps), text)
}
