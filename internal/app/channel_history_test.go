package app

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/virtpriv/node/internal/lndrpc"
)

type closedChannelSource func(context.Context) ([]lndrpc.ClosedChannel, error)

func (f closedChannelSource) ListClosedChannelsContext(ctx context.Context) ([]lndrpc.ClosedChannel, error) {
	return f(ctx)
}

func TestChannelHistoryOwnsRowsAndRetainsFailedRead(t *testing.T) {
	r := NewChannelHistoryReader()
	defer r.Close()
	rows := []lndrpc.ClosedChannel{{ChannelPoint: "closed:0", PeerAlias: "same peer"}}
	good := r.Collect(closedChannelSource(func(context.Context) ([]lndrpc.ClosedChannel, error) { return rows, nil }))
	rows[0].ChannelPoint = "changed:0"
	failed := errors.New("closed channels failed")
	partial := r.Collect(closedChannelSource(func(context.Context) ([]lndrpc.ClosedChannel, error) {
		return rows, failed
	})).Retain(good)
	if len(partial.Closed.Value) != 1 || partial.Closed.Value[0].ChannelPoint != "closed:0" ||
		partial.Closed.Fresh() || !errors.Is(partial.Closed.Err, failed) || partial.Closed.ObservedAt != good.Closed.ObservedAt {
		t.Fatal("source mutation or failed partial read replaced last-good history")
	}
	empty := r.Collect(closedChannelSource(func(context.Context) ([]lndrpc.ClosedChannel, error) { return nil, nil })).Retain(partial)
	if !empty.Closed.Fresh() || len(empty.Closed.Value) != 0 {
		t.Fatal("successful empty read did not replace retained rows")
	}
	if absent := r.Collect(nil); absent.Closed.Known() || absent.Closed.Err == nil {
		t.Fatal("absent source became known empty history")
	}
}

func TestChannelHistoryDeadlineAndCloseJoin(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(map[bool]string{false: "deadline", true: "shutdown"}[shutdown], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := NewChannelHistoryReader()
				defer r.Close()
				entered := make(chan context.Context)
				release := make(chan struct{})
				defer func() {
					select {
					case <-release:
					default:
						close(release)
					}
				}()
				result := make(chan ChannelHistorySnapshot, 1)
				go func() {
					result <- r.Collect(closedChannelSource(func(ctx context.Context) ([]lndrpc.ClosedChannel, error) {
						entered <- ctx
						<-ctx.Done()
						<-release
						return nil, ctx.Err()
					}))
				}()
				ctx := <-entered
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 30*time.Second {
					t.Fatal("read lacks a deadline within the 30-second bound")
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
				if !errors.Is(ctx.Err(), want) {
					t.Fatal("read was not canceled on time")
				}
				if shutdown {
					select {
					case <-closed:
						t.Fatal("Close returned before admitted work joined")
					default:
					}
				}
				close(release)
				if got := <-result; !errors.Is(got.Closed.Err, want) || got.Closed.Known() {
					t.Fatalf("cancellation published successful history: %+v", got)
				}
				r.Close()
				if late := r.Collect(nil); !errors.Is(late.Closed.Err, context.Canceled) {
					t.Fatal("closed reader admitted late work")
				}
			})
		})
	}
}
