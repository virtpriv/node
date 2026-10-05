package app

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/virtpriv/node/internal/lndrpc"
)

type PaymentHistorySource interface {
	ListInvoicesContext(context.Context, uint64) ([]lndrpc.PaymentEntry, error)
	ListPaymentsContext(context.Context, uint64) ([]lndrpc.PaymentEntry, error)
}

type PaymentHistorySnapshot struct {
	Invoices Observation[[]lndrpc.PaymentEntry]
	Payments Observation[[]lndrpc.PaymentEntry]
}

func (s PaymentHistorySnapshot) Retain(previous PaymentHistorySnapshot) PaymentHistorySnapshot {
	s.Invoices = retain(s.Invoices, previous.Invoices)
	s.Payments = retain(s.Payments, previous.Payments)
	return s
}

func (s PaymentHistorySnapshot) Unavailable(err error) PaymentHistorySnapshot {
	s.Invoices.Err, s.Payments.Err = err, err
	return s
}

func (s PaymentHistorySnapshot) Fresh() bool { return s.Invoices.Fresh() && s.Payments.Fresh() }

// Entries merges the two recent windows without changing either observation.
// Stable ties use direction and the daemon's index, not reply order.
func (s PaymentHistorySnapshot) Entries() []lndrpc.PaymentEntry {
	entries := make([]lndrpc.PaymentEntry, 0, len(s.Invoices.Value)+len(s.Payments.Value))
	entries = append(entries, s.Invoices.Value...)
	entries = append(entries, s.Payments.Value...)
	slices.SortFunc(entries, func(a, b lndrpc.PaymentEntry) int {
		if order := cmp.Compare(b.CreationDate, a.CreationDate); order != 0 {
			return order
		}
		if a.IsIncoming != b.IsIncoming {
			if a.IsIncoming {
				return -1
			}
			return 1
		}
		return cmp.Compare(b.Index, a.Index)
	})
	return entries
}

// PaymentHistoryReader owns collection and local cancellation. The independent
// RPC windows are not a complete ledger or an atomic balance snapshot.
type PaymentHistoryReader struct {
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	closed bool
	calls  sync.WaitGroup
}

func NewPaymentHistoryReader() *PaymentHistoryReader {
	ctx, cancel := context.WithCancel(context.Background())
	return &PaymentHistoryReader{ctx: ctx, cancel: cancel}
}

// Close joins admitted reads. Existing shared reconnect mutex waits can delay
// cancellation; this owner does not change connection or root-worker recovery.
func (r *PaymentHistoryReader) Close() {
	r.mu.Lock()
	r.closed = true
	r.cancel()
	r.mu.Unlock()
	r.calls.Wait()
}

func (r *PaymentHistoryReader) Collect(source PaymentHistorySource) PaymentHistorySnapshot {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return PaymentHistorySnapshot{}.Unavailable(context.Canceled)
	}
	r.calls.Add(1)
	r.mu.Unlock()
	defer r.calls.Done()
	if source == nil {
		return PaymentHistorySnapshot{}.Unavailable(errors.New("lnd not connected"))
	}
	ctx, cancel := context.WithTimeout(r.ctx, 30*time.Second)
	defer cancel()
	var result PaymentHistorySnapshot
	var reads sync.WaitGroup
	reads.Go(func() { result.Invoices = observe(source.ListInvoicesContext(ctx, 50)) })
	reads.Go(func() { result.Payments = observe(source.ListPaymentsContext(ctx, 50)) })
	reads.Wait()
	return result
}
