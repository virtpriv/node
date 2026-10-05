package app

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/virtpriv/node/internal/lndrpc"
)

type observationSource struct {
	coins func(context.Context, int32, int32) ([]lndrpc.UTXO, error)
	txs   func(context.Context) ([]lndrpc.OnChainTx, error)
}

func (s observationSource) ListUnspentContext(ctx context.Context, min, max int32) ([]lndrpc.UTXO, error) {
	return s.coins(ctx, min, max)
}
func (s observationSource) GetTransactionsContext(ctx context.Context) ([]lndrpc.OnChainTx, error) {
	return s.txs(ctx)
}

func TestOnChainReaderPartialObservationAndRetention(t *testing.T) {
	r := NewOnChainReader()
	t.Cleanup(r.Close)
	failure := errors.New("history read failed")
	result := r.Collect(observationSource{
		coins: func(ctx context.Context, min, max int32) ([]lndrpc.UTXO, error) {
			if min != 0 || max != 999999 {
				t.Error("confirmation range changed")
			}
			return nil, nil
		},
		txs: func(context.Context) ([]lndrpc.OnChainTx, error) {
			return []lndrpc.OnChainTx{{Txid: "partial"}}, failure
		},
	})
	previous := OnChainSnapshot{Utxos: observe([]lndrpc.UTXO{{Txid: "spent"}}, nil), OnChainTxs: observe([]lndrpc.OnChainTx{{Txid: "retained"}}, nil)}
	result = result.Retain(previous)
	if !result.Utxos.Fresh() || len(result.Utxos.Value) != 0 || result.OnChainTxs.Fresh() || !errors.Is(result.OnChainTxs.Err, failure) || result.OnChainTxs.Value[0].Txid != "retained" || result.OnChainTxs.ObservedAt != previous.OnChainTxs.ObservedAt {
		t.Fatalf("empty success or failed partial read handled incorrectly: %+v", result)
	}
	missing := r.Collect(nil)
	if missing.Utxos.Known() || missing.OnChainTxs.Known() || missing.Utxos.Err == nil || missing.OnChainTxs.Err == nil {
		t.Fatal("missing source became a successful empty snapshot")
	}
}

func TestOnChainReaderBoundsAndJoinsBothReads(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(map[bool]string{false: "deadline", true: "close"}[shutdown], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := NewOnChainReader()
				defer r.Close()
				entered := make(chan context.Context, 2)
				block := func(ctx context.Context) error { entered <- ctx; <-ctx.Done(); return ctx.Err() }
				done := make(chan OnChainSnapshot, 1)
				go func() {
					done <- r.Collect(observationSource{
						coins: func(ctx context.Context, _, _ int32) ([]lndrpc.UTXO, error) { return nil, block(ctx) },
						txs:   func(ctx context.Context) ([]lndrpc.OnChainTx, error) { return nil, block(ctx) },
					})
				}()
				first, second := <-entered, <-entered
				for _, ctx := range []context.Context{first, second} {
					deadline, ok := ctx.Deadline()
					if !ok || time.Until(deadline) != 30*time.Second {
						t.Fatal("unbounded list read")
					}
				}
				want := context.DeadlineExceeded
				if shutdown {
					r.Close()
					want = context.Canceled
				}
				result := <-done
				if !errors.Is(result.Utxos.Err, want) || !errors.Is(result.OnChainTxs.Err, want) || first.Err() == nil || second.Err() == nil {
					t.Fatal("reader did not cancel and join both reads")
				}
				r.Close()
				late := r.Collect(observationSource{})
				if !errors.Is(late.Utxos.Err, context.Canceled) || !errors.Is(late.OnChainTxs.Err, context.Canceled) {
					t.Fatal("late command entered closed reader")
				}
			})
		})
	}
}

func TestOnChainReaderCallerCancellationIsIsolatedAndJoined(t *testing.T) {
	for _, last := range []string{"coins", "history"} {
		t.Run(last+" exits last", func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := NewOnChainReader()
				defer r.Close()
				form, cancel := context.WithCancel(context.Background())
				defer cancel()
				formEntered, homeEntered := make(chan context.Context, 2), make(chan context.Context, 2)
				coinsRelease, txsRelease, homeRelease := make(chan struct{}), make(chan struct{}), make(chan struct{})
				releaseCoins := sync.OnceFunc(func() { close(coinsRelease) })
				releaseTxs := sync.OnceFunc(func() { close(txsRelease) })
				releaseHome := sync.OnceFunc(func() { close(homeRelease) })
				defer releaseCoins()
				defer releaseTxs()
				defer releaseHome()
				source := func(entered chan context.Context, coinsRelease, txsRelease chan struct{}, maxConfs int32, waitForCancel bool) observationSource {
					block := func(ctx context.Context, release chan struct{}) {
						entered <- ctx
						if waitForCancel {
							<-ctx.Done()
						}
						<-release
					}
					return observationSource{
						coins: func(ctx context.Context, min, max int32) ([]lndrpc.UTXO, error) {
							if min != 0 || max != maxConfs {
								t.Errorf("confirmation range = %d..%d, want 0..%d", min, max, maxConfs)
							}
							block(ctx, coinsRelease)
							return []lndrpc.UTXO{{Txid: "late"}}, nil
						},
						txs: func(ctx context.Context) ([]lndrpc.OnChainTx, error) {
							block(ctx, txsRelease)
							return []lndrpc.OnChainTx{{Txid: "late"}}, nil
						},
					}
				}
				coinsDone := make(chan Observation[[]lndrpc.UTXO], 1)
				txsDone := make(chan Observation[[]lndrpc.OnChainTx], 1)
				homeDone := make(chan OnChainSnapshot, 1)
				formSource := source(formEntered, coinsRelease, txsRelease, math.MaxInt32, true)
				go func() { coinsDone <- r.ReadUnspent(form, formSource, 0, math.MaxInt32) }()
				go func() { txsDone <- r.ReadTransactions(form, formSource) }()
				go func() { homeDone <- r.Collect(source(homeEntered, homeRelease, homeRelease, 999999, false)) }()
				formReads := []context.Context{<-formEntered, <-formEntered}
				homeReads := []context.Context{<-homeEntered, <-homeEntered}
				cancel()
				synctest.Wait()
				for _, ctx := range formReads {
					if !errors.Is(ctx.Err(), context.Canceled) {
						t.Fatal("form cancellation did not reach both reads")
					}
				}
				for _, ctx := range homeReads {
					if ctx.Err() != nil {
						t.Fatal("canceling the form canceled unrelated wallet observations")
					}
				}
				// Finish unrelated work so it cannot conceal missing form-read ownership.
				releaseHome()
				<-homeDone
				closed := make(chan struct{})
				go func() { r.Close(); close(closed) }()
				order := []string{"coins", "history"}
				if last == "coins" {
					order = []string{"history", "coins"}
				}
				var result OnChainSnapshot
				for _, read := range order {
					synctest.Wait()
					select {
					case <-closed:
						t.Fatalf("shutdown returned while %s read was still blocked", read)
					default:
					}
					if read == "coins" {
						releaseCoins()
						result.Utxos = <-coinsDone
					} else {
						releaseTxs()
						result.OnChainTxs = <-txsDone
					}
				}
				<-closed
				if !errors.Is(result.Utxos.Err, context.Canceled) || !errors.Is(result.OnChainTxs.Err, context.Canceled) || result.Utxos.Known() || result.OnChainTxs.Known() {
					t.Fatal("canceled reads published late successful source responses")
				}
			})
		})
	}
}

func TestOnChainReaderRefusesCanceledCallerBeforeRPC(t *testing.T) {
	r := NewOnChainReader()
	defer r.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Nil functions panic if either source operation is reached.
	result := OnChainSnapshot{Utxos: r.ReadUnspent(ctx, observationSource{}, 0, math.MaxInt32), OnChainTxs: r.ReadTransactions(ctx, observationSource{})}
	if !errors.Is(result.Utxos.Err, context.Canceled) || !errors.Is(result.OnChainTxs.Err, context.Canceled) {
		t.Fatal("canceled caller was admitted")
	}
}
