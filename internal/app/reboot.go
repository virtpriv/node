package app

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/virtpriv/node/internal/helper"
)

type RebootOutcome int

const (
	RebootUnconfirmed RebootOutcome = iota
	RebootNotStarted
	RebootAccepted
)

type RebootResult struct {
	Outcome RebootOutcome
	Err     error
}

// Reboots owns local observation for one terminal session. Close cancels and
// joins readers without canceling an accepted host request or retrying it.
type Reboots struct {
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	workers sync.WaitGroup
	call    func(context.Context, string, any, any) error
}

func NewReboots() *Reboots {
	ctx, cancel := context.WithCancel(context.Background())
	return &Reboots{ctx: ctx, cancel: cancel,
		call: func(ctx context.Context, verb string, params, result any) error {
			session, err := helper.StartContext(ctx, verb, params)
			if err != nil {
				return err
			}
			return session.Wait(result)
		},
	}
}

func (r *Reboots) Close() {
	r.mu.Lock()
	r.cancel()
	r.mu.Unlock()
	r.workers.Wait()
}

func (r *Reboots) Request() <-chan RebootResult {
	results := make(chan RebootResult, 1)
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ctx.Err(); err != nil {
		results <- RebootResult{Outcome: RebootNotStarted, Err: err}
		close(results)
		return results
	}
	// Include time queued behind a long helper transaction. This bounds local
	// observation, not shutdown, and expiry does not authorize an automatic retry.
	ctx, cancel := context.WithTimeout(r.ctx, 35*time.Minute)
	r.workers.Go(func() {
		defer cancel()
		defer close(results)
		if err := ctx.Err(); err != nil {
			results <- RebootResult{Outcome: RebootNotStarted, Err: err}
			return
		}
		var reply helper.RebootResult
		err := r.call(ctx, helper.VerbReboot, nil, &reply)
		if err == nil && !reply.Accepted {
			err = errors.New("helper did not confirm reboot acceptance")
		}
		outcome := RebootAccepted
		if err != nil {
			outcome = RebootUnconfirmed
		}
		results <- RebootResult{Outcome: outcome, Err: err}
	})
	return results
}
