package app

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/virtpriv/node/internal/autounlock"
	"github.com/virtpriv/node/internal/helper"
)

// AutoUnlockObservation describes what the local caller knows about the request.
type AutoUnlockObservation int

const (
	AutoUnlockUnknown AutoUnlockObservation = iota
	AutoUnlockNotStarted
	AutoUnlockObserved
)

// AutoUnlockResult separates local observation from the host's proven outcome.
// A lost response is never evidence of rollback or a stopped root operation.
type AutoUnlockResult struct {
	Observation AutoUnlockObservation
	Transition  autounlock.Result
	Err         error
}

// AutoUnlockChanges owns the local readers for one TUI. Accepted root operations
// continue independently when the TUI exits or stops observing.
type AutoUnlockChanges struct {
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	workers sync.WaitGroup
	change  func(context.Context, bool, autounlock.Password) (autounlock.Result, error)
}

// NewAutoUnlockChanges creates a lifetime owner for helper-backed transitions.
func NewAutoUnlockChanges() *AutoUnlockChanges {
	ctx, cancel := context.WithCancel(context.Background())
	return &AutoUnlockChanges{ctx: ctx, cancel: cancel, change: callAutoUnlock}
}

func callAutoUnlock(ctx context.Context, enable bool, password autounlock.Password) (autounlock.Result, error) {
	verb := helper.VerbRemoveWalletPassword
	var params any
	if enable {
		verb = helper.VerbStageWalletPassword
		params = helper.StageWalletPasswordParams{Password: password.Text()}
	}
	session, err := helper.StartContext(ctx, verb, params)
	if err != nil {
		return autounlock.Result{}, err
	}
	var result autounlock.Result
	err = session.Wait(&result)
	return result, err
}

// Enable immediately starts observation of one exact password verification.
func (w *AutoUnlockChanges) Enable(password autounlock.Password) <-chan AutoUnlockResult {
	return w.start(true, password)
}

// Disable immediately starts observation of the host's locked-state transition.
func (w *AutoUnlockChanges) Disable() <-chan AutoUnlockResult {
	return w.start(false, autounlock.Password{})
}

func (w *AutoUnlockChanges) start(enable bool, password autounlock.Password) <-chan AutoUnlockResult {
	results := make(chan AutoUnlockResult, 1)
	w.mu.Lock()
	defer w.mu.Unlock()
	err := w.ctx.Err()
	if enable && password.Text() == "" {
		err = errors.New("wallet password was not validated")
	}
	if err != nil {
		results <- AutoUnlockResult{Observation: AutoUnlockNotStarted, Err: err}
		close(results)
		return results
	}
	// Include helper queue time. Allow the existing 20-minute helper window plus
	// queue allowance; this is not LND's 120-second candidate-start timeout.
	// Expiry ends only local observation, even if the helper is still queued.
	ctx, cancel := context.WithTimeout(w.ctx, 25*time.Minute)
	w.workers.Go(func() {
		defer cancel()
		defer close(results)
		if err := ctx.Err(); err != nil {
			results <- AutoUnlockResult{Observation: AutoUnlockNotStarted, Err: err}
			return
		}
		transition, err := w.change(ctx, enable, password)
		if err == nil && !validAutoUnlockOutcome(enable, transition.Outcome) {
			err = errors.New("helper returned an unexpected auto-unlock outcome")
		}
		if err != nil {
			results <- AutoUnlockResult{Observation: AutoUnlockUnknown, Err: err}
			return
		}
		results <- AutoUnlockResult{Observation: AutoUnlockObserved, Transition: transition}
	})
	return results
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

// Close cancels and joins readers, including those with no remaining consumer.
func (w *AutoUnlockChanges) Close() {
	w.mu.Lock()
	w.cancel()
	w.mu.Unlock()
	w.workers.Wait()
}
