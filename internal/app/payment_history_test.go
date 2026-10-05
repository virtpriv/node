package app

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"github.com/virtpriv/node/internal/lndrpc"
)

type historySource struct {
	invoices func(context.Context, uint64) ([]lndrpc.PaymentEntry, error)
	payments func(context.Context, uint64) ([]lndrpc.PaymentEntry, error)
}

func (s historySource) ListInvoicesContext(ctx context.Context, limit uint64) ([]lndrpc.PaymentEntry, error) {
	return s.invoices(ctx, limit)
}
func (s historySource) ListPaymentsContext(ctx context.Context, limit uint64) ([]lndrpc.PaymentEntry, error) {
	return s.payments(ctx, limit)
}

func TestPaymentHistoryRetainsIndependentWindowsAndStableOrder(t *testing.T) {
	r := NewPaymentHistoryReader()
	defer r.Close()
	failed := errors.New("payments failed")
	wantInv := []lndrpc.PaymentEntry{{Index: 1, IsIncoming: true, CreationDate: 8}, {Index: 2, IsIncoming: true, CreationDate: 8}}
	// Spare capacity exposes a merge that reuses and sorts the source backing array.
	inv := make([]lndrpc.PaymentEntry, len(wantInv), 4)
	copy(inv, wantInv)
	pay := []lndrpc.PaymentEntry{{Index: 1, CreationDate: 9}, {Index: 2, CreationDate: 8}}
	source := historySource{
		invoices: func(_ context.Context, limit uint64) ([]lndrpc.PaymentEntry, error) {
			if limit != 50 {
				t.Error("wrong invoice window")
			}
			return inv, nil
		},
		payments: func(_ context.Context, limit uint64) ([]lndrpc.PaymentEntry, error) {
			if limit != 50 {
				t.Error("wrong payment window")
			}
			return pay, nil
		},
	}
	good := r.Collect(source)
	entries := good.Entries()
	if !good.Fresh() || len(entries) != 4 || entries[0].CreationDate != 9 || !reflect.DeepEqual(inv, wantInv) {
		t.Fatal("merge lost records, newest-first order or source ownership")
	}
	// Equal timestamps must not shuffle when the daemon changes reply order.
	// The particular direction/index tie-break is an implementation choice.
	inv = []lndrpc.PaymentEntry{inv[1], inv[0]}
	pay = []lndrpc.PaymentEntry{pay[1], pay[0]}
	if !reflect.DeepEqual(r.Collect(source).Entries(), entries) {
		t.Fatal("equal timestamps followed daemon reply order")
	}
	source.invoices = func(context.Context, uint64) ([]lndrpc.PaymentEntry, error) { return nil, nil }
	source.payments = func(context.Context, uint64) ([]lndrpc.PaymentEntry, error) {
		return []lndrpc.PaymentEntry{{Index: 99}}, failed
	}
	partial := r.Collect(source).Retain(good)
	if partial.Fresh() || !partial.Invoices.Fresh() || len(partial.Invoices.Value) != 0 || !errors.Is(partial.Payments.Err, failed) || partial.Payments.ObservedAt != good.Payments.ObservedAt || !reflect.DeepEqual(partial.Payments.Value, good.Payments.Value) {
		t.Fatal("failed partial read replaced successful history or empty invoice window")
	}
	absent := r.Collect(nil)
	if absent.Invoices.Known() || absent.Payments.Known() || absent.Invoices.Err == nil || absent.Payments.Err == nil {
		t.Fatal("missing source became known empty history")
	}
	source.payments = func(context.Context, uint64) ([]lndrpc.PaymentEntry, error) { return nil, nil }
	recovered := r.Collect(source).Retain(partial)
	if !recovered.Fresh() || len(recovered.Entries()) != 0 {
		t.Fatal("successful empty windows did not replace old records")
	}
}

func TestPaymentHistoryDeadlineAndCloseJoinBothReads(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(map[bool]string{false: "deadline", true: "close"}[shutdown], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := NewPaymentHistoryReader()
				defer r.Close()
				entered := make(chan context.Context, 2)
				release := make(chan struct{})
				defer func() {
					select {
					case <-release:
					default:
						close(release)
					}
				}()
				block := func(ctx context.Context, _ uint64) ([]lndrpc.PaymentEntry, error) {
					entered <- ctx
					<-ctx.Done()
					<-release
					return nil, ctx.Err()
				}
				result := make(chan PaymentHistorySnapshot, 1)
				go func() { result <- r.Collect(historySource{block, block}) }()
				first, second := <-entered, <-entered
				for _, ctx := range []context.Context{first, second} {
					deadline, ok := ctx.Deadline()
					if !ok || time.Until(deadline) != 30*time.Second {
						t.Fatal("missing shared 30-second bound")
					}
				}
				closed := make(chan struct{})
				want := context.DeadlineExceeded
				if shutdown {
					want = context.Canceled
					go func() { r.Close(); close(closed) }()
				} else {
					time.Sleep(30 * time.Second)
				}
				synctest.Wait()
				select {
				case <-result:
					t.Fatal("collection returned before readers joined")
				default:
				}
				if shutdown {
					select {
					case <-closed:
						t.Fatal("Close returned before canceled readers joined")
					default:
					}
				}
				close(release)
				got := <-result
				if !errors.Is(got.Invoices.Err, want) || !errors.Is(got.Payments.Err, want) {
					t.Fatal("lost cancellation outcomes")
				}
				if shutdown {
					<-closed
				} else {
					r.Close()
				}
				late := r.Collect(historySource{})
				if !errors.Is(late.Invoices.Err, context.Canceled) || !errors.Is(late.Payments.Err, context.Canceled) {
					t.Fatal("late command entered closed reader")
				}
			})
		})
	}
}
