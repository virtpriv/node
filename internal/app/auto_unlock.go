package app

import (
	"encoding/json"
	"errors"

	"github.com/virtpriv/node/internal/api"
	"github.com/virtpriv/node/internal/autounlock"
)

// AutoUnlockObservation describes what the TUI knows about the change.
type AutoUnlockObservation int

const (
	AutoUnlockUnknown AutoUnlockObservation = iota
	AutoUnlockNotStarted
	AutoUnlockObserved
	// AutoUnlockRunning: the job has not ended.
	AutoUnlockRunning
)

// AutoUnlockResult separates what the TUI observed from the host's proven
// outcome. A lost answer is never evidence of rollback or of a stopped job.
type AutoUnlockResult struct {
	JobID       string
	Observation AutoUnlockObservation
	Enable      bool
	Transition  autounlock.Result
	Err         error
}

// AutoUnlockResultOf reads an auto-unlock job's update. Only a completed job
// with an outcome its kind can have is observed.
func AutoUnlockResultOf(u JobUpdate) AutoUnlockResult {
	r := AutoUnlockResult{JobID: u.Job.ID, Enable: u.Job.Kind == api.KindAutoUnlockOn, Err: u.Err}
	switch u.Outcome {
	case JobRunning:
		r.Observation = AutoUnlockRunning
		return r
	case JobNotStarted:
		r.Observation, r.Err = AutoUnlockNotStarted, u.Error()
		return r
	case JobCompleted:
	default:
		r.Observation, r.Err = AutoUnlockUnknown, u.Error()
		return r
	}
	if err := json.Unmarshal(u.Job.Result, &r.Transition); err != nil || !validAutoUnlockOutcome(r.Enable, r.Transition.Outcome) {
		r.Observation = AutoUnlockUnknown
		r.Err = errors.New("vpnd returned an unexpected auto-unlock outcome")
		return r
	}
	r.Observation = AutoUnlockObserved
	return r
}

func validAutoUnlockOutcome(enable bool, outcome autounlock.Outcome) bool {
	if outcome == autounlock.RepairRequired {
		return true
	}
	if enable {
		return outcome == autounlock.Enabled || outcome == autounlock.VerificationFailed || outcome == autounlock.VerificationTimedOut
	}
	return outcome == autounlock.Disabled || outcome == autounlock.StillEnabled
}
