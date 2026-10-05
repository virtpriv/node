package app

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/virtpriv/node/internal/lndrpc"
)

type ChannelHistorySource interface {
	ListClosedChannelsContext(context.Context) ([]lndrpc.ClosedChannel, error)
}

type ChannelHistorySnapshot struct {
	Closed Observation[[]lndrpc.ClosedChannel]
}

func (s ChannelHistorySnapshot) Retain(previous ChannelHistorySnapshot) ChannelHistorySnapshot {
	s.Closed = retain(s.Closed, previous.Closed)
	return s
}

// ChannelHistoryEntry preserves source identity without claiming that independent
// current and closed observations form an atomic channel lifecycle snapshot.
type ChannelHistoryEntry struct {
	ChannelPoint    string
	PeerAlias       string
	RemotePubkey    string
	Capacity        int64
	Status          string
	CloseType       string
	BlocksRemaining int32
}

func (s ChannelHistorySnapshot) Entries(current Observation[ChannelStatus]) []ChannelHistoryEntry {
	var entries []ChannelHistoryEntry
	if current.Known() {
		for _, ch := range current.Value.Channels {
			status := "inactive"
			if ch.Active {
				status = "active"
			}
			if ch.Pending {
				status = "pending open"
			}
			entries = append(entries, ChannelHistoryEntry{ChannelPoint: ch.ChannelPoint, PeerAlias: ch.PeerAlias, RemotePubkey: ch.RemotePubkey, Capacity: ch.Capacity, Status: status})
		}
		for _, ch := range current.Value.Pending.WaitingCloseChannels {
			entries = append(entries, ChannelHistoryEntry{ChannelPoint: ch.ChannelPoint, PeerAlias: ch.PeerAlias, RemotePubkey: ch.RemotePubkey, Capacity: ch.Capacity, Status: "waiting close", CloseType: "closing"})
		}
		for _, ch := range current.Value.Pending.PendingForceCloseChannels {
			entries = append(entries, ChannelHistoryEntry{ChannelPoint: ch.ChannelPoint, PeerAlias: ch.PeerAlias, RemotePubkey: ch.RemotePubkey, Capacity: ch.Capacity, Status: "force close", CloseType: "force", BlocksRemaining: ch.BlocksRemaining})
		}
	}
	if s.Closed.Known() {
		for _, ch := range s.Closed.Value {
			entries = append(entries, ChannelHistoryEntry{ChannelPoint: ch.ChannelPoint, PeerAlias: ch.PeerAlias, RemotePubkey: ch.RemotePubkey, Capacity: ch.Capacity, Status: "closed", CloseType: ch.CloseType})
		}
	}
	return entries
}

// ChannelHistoryReader owns the additional closed-channel read. Open and pending
// channels are reused from status, with independent freshness retained by callers.
type ChannelHistoryReader struct {
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	closed bool
	calls  sync.WaitGroup
}

func NewChannelHistoryReader() *ChannelHistoryReader {
	ctx, cancel := context.WithCancel(context.Background())
	return &ChannelHistoryReader{ctx: ctx, cancel: cancel}
}

// Close cancels and joins admitted work. Shared client reconnect mutex waits
// can delay cancellation; this does not own the reconnect or root worker.
func (r *ChannelHistoryReader) Close() {
	r.mu.Lock()
	r.closed = true
	r.cancel()
	r.mu.Unlock()
	r.calls.Wait()
}

func (r *ChannelHistoryReader) Collect(source ChannelHistorySource) ChannelHistorySnapshot {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return ChannelHistorySnapshot{Closed: Observation[[]lndrpc.ClosedChannel]{Err: context.Canceled}}
	}
	r.calls.Add(1)
	r.mu.Unlock()
	defer r.calls.Done()
	if source == nil {
		return ChannelHistorySnapshot{Closed: Observation[[]lndrpc.ClosedChannel]{Err: errors.New("lnd not connected")}}
	}
	ctx, cancel := context.WithTimeout(r.ctx, 30*time.Second)
	defer cancel()
	rows, err := source.ListClosedChannelsContext(ctx)
	return ChannelHistorySnapshot{Closed: observe(slices.Clone(rows), err)}
}
