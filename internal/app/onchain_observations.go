package app

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/virtpriv/node/internal/lndrpc"
)

type OnChainSource interface {
	ListUnspentContext(context.Context, int32, int32) ([]lndrpc.UTXO, error)
	GetTransactionsContext(context.Context) ([]lndrpc.OnChainTx, error)
}

type OnChainSnapshot struct {
	Utxos      Observation[[]lndrpc.UTXO]
	OnChainTxs Observation[[]lndrpc.OnChainTx]
}

func (s OnChainSnapshot) Retain(previous OnChainSnapshot) OnChainSnapshot {
	s.Utxos = retain(s.Utxos, previous.Utxos)
	s.OnChainTxs = retain(s.OnChainTxs, previous.OnChainTxs)
	return s
}

func (s OnChainSnapshot) Unavailable(err error) OnChainSnapshot {
	s.Utxos.Err, s.OnChainTxs.Err = err, err
	return s
}

// OnChainReader owns local read lifetimes. Scheduling and same-wallet retention
// belong to its caller; the two reads do not form an atomic wallet snapshot.
type OnChainReader struct {
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	closed bool
	calls  sync.WaitGroup
}

func NewOnChainReader() *OnChainReader {
	ctx, cancel := context.WithCancel(context.Background())
	return &OnChainReader{ctx: ctx, cancel: cancel}
}

// Close cancels and joins local reads. Shared LND reconnect mutex waits can
// still delay the join; this owner does not change connection recovery.
func (r *OnChainReader) Close() {
	r.mu.Lock()
	r.closed = true
	r.cancel()
	r.mu.Unlock()
	r.calls.Wait()
}

func (r *OnChainReader) begin(caller context.Context) (context.Context, func(), error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, nil, context.Canceled
	}
	r.calls.Add(1)
	r.mu.Unlock()
	ctx, cancel := context.WithTimeout(r.ctx, 30*time.Second)
	stop := context.AfterFunc(caller, cancel)
	done := func() {
		stop()
		cancel()
		r.calls.Done()
	}
	if err := caller.Err(); err != nil {
		done()
		return nil, nil, err
	}
	return ctx, done, nil
}

// ReadUnspent and ReadTransactions share the terminal owner while allowing forms
// to publish each observation as soon as it completes. Each caller can cancel
// its own reads without canceling another form or the On-Chain view.
func (r *OnChainReader) ReadUnspent(caller context.Context, source OnChainSource, minConfs, maxConfs int32) Observation[[]lndrpc.UTXO] {
	ctx, done, err := r.begin(caller)
	if err != nil {
		return Observation[[]lndrpc.UTXO]{Err: err}
	}
	defer done()
	if source == nil {
		return Observation[[]lndrpc.UTXO]{Err: errors.New("lnd not connected")}
	}
	coins, err := source.ListUnspentContext(ctx, minConfs, maxConfs)
	if caller.Err() != nil {
		err = caller.Err()
	} else if ctx.Err() != nil {
		err = ctx.Err()
	}
	return observe(coins, err)
}

func (r *OnChainReader) ReadTransactions(caller context.Context, source OnChainSource) Observation[[]lndrpc.OnChainTx] {
	ctx, done, err := r.begin(caller)
	if err != nil {
		return Observation[[]lndrpc.OnChainTx]{Err: err}
	}
	defer done()
	if source == nil {
		return Observation[[]lndrpc.OnChainTx]{Err: errors.New("lnd not connected")}
	}
	txs, err := source.GetTransactionsContext(ctx)
	if caller.Err() != nil {
		err = caller.Err()
	} else if ctx.Err() != nil {
		err = ctx.Err()
	}
	return observe(txs, err)
}

func (r *OnChainReader) Collect(source OnChainSource) OnChainSnapshot {
	ctx, done, err := r.begin(context.Background())
	if err != nil {
		return OnChainSnapshot{}.Unavailable(err)
	}
	defer done()
	if source == nil {
		return OnChainSnapshot{}.Unavailable(errors.New("lnd not connected"))
	}
	var result OnChainSnapshot
	var reads sync.WaitGroup
	reads.Go(func() {
		result.Utxos = observe(source.ListUnspentContext(ctx, 0, 999999))
	})
	reads.Go(func() {
		result.OnChainTxs = observe(source.GetTransactionsContext(ctx))
	})
	reads.Wait()
	return result
}
