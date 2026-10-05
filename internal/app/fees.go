package app

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/virtpriv/node/internal/bitcoin"
	"github.com/virtpriv/node/internal/config"
)

// FeeSuggestions contains independent estimates for the fast, medium and slow
// requested targets. A zero entry is unavailable; successful entries retain the
// actual target returned by Core. These are hints, not exact transaction quotes.
type FeeSuggestions struct {
	Tiers [3]bitcoin.FeeEstimate
	Err   error
}

// FeeReader owns local read lifetimes for one terminal session. Callers own
// scheduling, request identity and publication; no cross-form cache is kept.
type FeeReader struct {
	estimate func(context.Context, int, int) (bitcoin.FeeEstimate, error)
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	closed   bool
	calls    sync.WaitGroup
}

func NewFeeReader() *FeeReader {
	ctx, cancel := context.WithCancel(context.Background())
	return &FeeReader{estimate: bitcoin.EstimateSmartFee, ctx: ctx, cancel: cancel}
}

func (r *FeeReader) Close() {
	r.mu.Lock()
	r.closed = true
	r.cancel()
	r.mu.Unlock()
	r.calls.Wait()
}

// Collect bounds the whole collection and cancels it when either its form or
// terminal session closes. An individual unavailable estimate does not discard
// the other targets; cancellation discards the interrupted collection.
func (r *FeeReader) Collect(caller context.Context, network string) FeeSuggestions {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return FeeSuggestions{Err: context.Canceled}
	}
	r.calls.Add(1)
	r.mu.Unlock()
	defer r.calls.Done()

	ctx, cancel := context.WithTimeout(r.ctx, 30*time.Second)
	defer cancel()
	stop := context.AfterFunc(caller, cancel)
	defer stop()
	if err := caller.Err(); err != nil {
		return FeeSuggestions{Err: err}
	}
	profile, err := config.NetworkConfigFromName(network)
	if err != nil {
		return FeeSuggestions{Err: err}
	}
	var result FeeSuggestions
	available := false
	for i, target := range [...]int{1, 6, 25} {
		if err := ctx.Err(); err != nil {
			return FeeSuggestions{Err: err}
		}
		estimate, err := r.estimate(ctx, profile.RPCPort, target)
		if err == nil {
			result.Tiers[i] = estimate
			available = true
		}
	}
	if err := ctx.Err(); err != nil {
		return FeeSuggestions{Err: err}
	}
	if !available {
		result.Err = errors.New("no fee estimates available")
	}
	return result
}
