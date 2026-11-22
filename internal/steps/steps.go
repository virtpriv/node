// Package steps runs a list of named steps of root work, one after another.
// It holds what every place that does such work while an owner watches can
// share, which is when a step begins and ends, how a stop is honored, and
// what the log says. A place keeps its own record, its own log and its own
// rules about what runs again. The install is the first place that uses it.
package steps

import (
	"context"
	"errors"
)

// A Step is one named piece of work.
//
// Run gets the run's context, which ends when a stop is asked. A step never
// hands it to a command that changes the machine, such as apt or dpkg, so a
// command that started runs to its end. A step looks at the context only
// where it waits, when it enters the wait and while it sleeps, and hands
// back the context's own error when the wait gives way. It never joins that
// error with a failure's.
type Step struct {
	Name string
	Run  func(ctx context.Context) error
}

// Outcome is how a step or a run ended.
type Outcome int

const (
	// Done: the step finished and its record was written.
	Done Outcome = iota
	// Failed: the step, or the write of its record, returned an error.
	Failed
	// Stopped: a stop was asked and the step gave way in a wait, or the
	// stop came before the step began.
	Stopped
)

// The words the log gets for a step, before its name.
const (
	logStarting = "starting: "
	logComplete = "complete: "
	logStopped  = "stopped: "
	logFailed   = "FAILED: "
)

// A Sink is the place's own record and log. The runner calls it from one
// goroutine, in order.
type Sink interface {
	// Begin marks step i as begun.
	Begin(i int)
	// End tells how step i ended, with its error when it failed. A sink
	// records only a step that is Done. When End fails, the step failed.
	End(i int, o Outcome, err error) error
	// Log writes one line about step i. The sink puts the step's number
	// before it, since only the place knows the numbers an owner sees.
	Log(i int, text string)
}

// Result is how a run ended. Index is the step that failed or was stopped,
// the step a stop came before, or len(list) when every step is done.
type Result struct {
	Outcome Outcome
	Index   int
	Err     error
}

// Run runs the steps in order until one fails, a stop is honored or all are
// done.
func Run(ctx context.Context, list []Step, sink Sink) Result {
	for i, s := range list {
		if ctx.Err() != nil {
			return Result{Outcome: Stopped, Index: i}
		}
		sink.Begin(i)
		sink.Log(i, logStarting+s.Name)
		err := s.Run(ctx)
		o := outcome(ctx, err)
		if o == Stopped {
			err = nil
		}
		// The record comes before the log, so the log never says more than
		// the record holds.
		if recErr := sink.End(i, o, err); recErr != nil {
			o, err = Failed, errors.Join(err, recErr)
		}
		switch o {
		case Failed:
			sink.Log(i, logFailed+s.Name+": "+err.Error())
			return Result{Outcome: Failed, Index: i, Err: err}
		case Stopped:
			sink.Log(i, logStopped+s.Name)
			return Result{Outcome: Stopped, Index: i}
		}
		sink.Log(i, logComplete+s.Name)
	}
	return Result{Outcome: Done, Index: len(list)}
}

// outcome reads a step's error. Only the context's own error, and only once
// a stop was asked, means that a wait gave way. So a step cannot call itself
// stopped, and a failure after a stop stays a failure.
func outcome(ctx context.Context, err error) Outcome {
	switch {
	case err == nil:
		return Done
	case ctx.Err() != nil && errors.Is(err, ctx.Err()):
		return Stopped
	}
	return Failed
}
