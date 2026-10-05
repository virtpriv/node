package app

import (
	"context"
	"sync"
	"time"

	"github.com/virtpriv/node/internal/helper"
)

type PackageUpdateOutcome int

const (
	PackageUpdateUnconfirmed PackageUpdateOutcome = iota
	PackageUpdateNotStarted
	PackageUpdateCompleted
)

type PackageUpdateResult struct {
	Outcome PackageUpdateOutcome
	Err     error
}

// PackageUpdates owns local observation, not the accepted root transaction.
// Closing it releases and joins readers without killing apt or retrying work.
type PackageUpdates struct {
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	workers sync.WaitGroup
	start   func(context.Context, string, any) (helperSession, error)
}

func NewPackageUpdates() *PackageUpdates {
	ctx, cancel := context.WithCancel(context.Background())
	return &PackageUpdates{ctx: ctx, cancel: cancel,
		start: func(ctx context.Context, verb string, params any) (helperSession, error) {
			return helper.StartContext(ctx, verb, params)
		},
	}
}

func (u *PackageUpdates) Close() {
	u.mu.Lock()
	u.cancel()
	u.mu.Unlock()
	u.workers.Wait()
}

func (u *PackageUpdates) Update() <-chan PackageUpdateResult {
	results := make(chan PackageUpdateResult, 1)
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.ctx.Err(); err != nil {
		results <- PackageUpdateResult{Outcome: PackageUpdateNotStarted, Err: err}
		close(results)
		return results
	}
	// Allow helper queue time as well as its 30-minute socket budget. Expiry
	// ends observation only; root work may still finish after this deadline.
	ctx, cancel := context.WithTimeout(u.ctx, 35*time.Minute)
	u.workers.Go(func() {
		defer cancel()
		defer close(results)
		if err := ctx.Err(); err != nil {
			results <- PackageUpdateResult{Outcome: PackageUpdateNotStarted, Err: err}
			return
		}
		err := u.observe(ctx)
		outcome := PackageUpdateCompleted
		if err != nil {
			outcome = PackageUpdateUnconfirmed
		}
		results <- PackageUpdateResult{Outcome: outcome, Err: err}
	})
	return results
}

func (u *PackageUpdates) observe(ctx context.Context) error {
	session, err := u.start(ctx, helper.VerbPackageUpdate, nil)
	if err != nil {
		return err
	}
	defer session.Close()
	for i := range helper.PackageUpdateStepNames() {
		if err := session.WaitStep(i); err != nil {
			return err
		}
	}
	// Upgrade progress precedes the root consistency audit. Only the final
	// successful terminator confirms that the complete workflow finished.
	return session.Wait(nil)
}
