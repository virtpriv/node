package app

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/virtpriv/node/internal/bitcoin"
	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/lndrpc"
	"github.com/virtpriv/node/internal/system"
)

type statusLNDStub struct {
	balance                             string
	channels                            []lndrpc.Channel
	pending                             lndrpc.PendingChannelInfo
	balanceErr, channelsErr, pendingErr error
}

func (s *statusLNDStub) GetStateContext(context.Context) (lndrpc.WalletState, error) {
	return lndrpc.WalletStateActive, nil
}
func (s *statusLNDStub) GetInfoContext(context.Context) (*lndrpc.NodeInfo, error) {
	return &lndrpc.NodeInfo{Pubkey: "node"}, nil
}
func (s *statusLNDStub) GetWalletBalanceContext(context.Context) (*lndrpc.WalletBalance, error) {
	return &lndrpc.WalletBalance{TotalBalance: s.balance}, s.balanceErr
}
func (s *statusLNDStub) ListChannelsContext(context.Context) ([]lndrpc.Channel, error) {
	return s.channels, s.channelsErr
}
func (s *statusLNDStub) GetPendingChannelsContext(context.Context) (*lndrpc.PendingChannelInfo, error) {
	return &s.pending, s.pendingErr
}

func statusCollectorFixture(t *testing.T) *StatusCollector {
	t.Helper()
	c := NewStatusCollector()
	t.Cleanup(c.Close)
	c.sources = statusSources{
		service: func(context.Context, string) (bool, error) { return false, nil },
		disk:    func(context.Context, string) (system.DiskInfo, error) { return system.DiskInfo{Used: "1G"}, nil },
		memory:  func() (system.MemInfo, error) { return system.MemInfo{Used: "1G"}, nil },
		bitcoin: func(context.Context, int) (bitcoin.BlockchainInfo, error) {
			return bitcoin.BlockchainInfo{Synced: true}, nil
		},
		size:   func(context.Context) (string, error) { return "1G", nil },
		reboot: func() (bool, error) { return false, nil }, publicIP: func(context.Context) (string, error) { return "", errors.New("unavailable") },
	}
	return c
}

func TestStatusPartialFailureAndRecovery(t *testing.T) {
	c := statusCollectorFixture(t)
	failed := errors.New("injected RPC failure")
	rpc := &statusLNDStub{balanceErr: failed, channelsErr: failed}
	first := c.Collect(*config.Default(), true, rpc)
	if !first.Node.Fresh() || first.Balance.Known() || first.Channels.Known() || !errors.Is(first.Channels.Err, failed) {
		t.Fatal("GetInfo success disguised failed balance/channel reads")
	}
	rpc.balanceErr, rpc.channelsErr = nil, nil
	rpc.balance = "20000"
	rpc.channels = []lndrpc.Channel{{ChannelPoint: "funding:0", LocalBalance: 1000}}
	rpc.pending.PendingOpenChannels = []lndrpc.PendingChannel{{ChannelPoint: "pending:1"}}
	good := c.Collect(*config.Default(), true, rpc).Retain(first)
	if !good.Balance.Fresh() || !good.Channels.Fresh() || len(good.Channels.Value.Channels) != 2 {
		t.Fatal("successful observation did not recover")
	}
	if !slices.ContainsFunc(good.Channels.Value.Channels, func(ch StatusChannel) bool { return ch.Pending && ch.ChannelPoint == "pending:1" }) {
		t.Fatal("pending channel lost its funding identity during status composition")
	}
	// One half of the channel read succeeds with an empty list. This must not
	// delete the previous channel while the pending-channel read is unavailable.
	rpc.channels = nil
	rpc.pendingErr, rpc.balanceErr = failed, failed
	c.sources.disk = func(context.Context, string) (system.DiskInfo, error) { return system.DiskInfo{}, failed }
	partial := c.Collect(*config.Default(), true, rpc).Retain(good)
	if partial.Balance.Fresh() || partial.Balance.Value.TotalBalance != "20000" || partial.Balance.ObservedAt != good.Balance.ObservedAt ||
		partial.Channels.Fresh() || len(partial.Channels.Value.Channels) != 2 || !partial.Node.Fresh() || partial.Disk.Fresh() || partial.Disk.Value.Used != "1G" {
		t.Fatal("partial failure lost last-good data or mislabeled its freshness")
	}
	if !good.Balance.Fresh() || len(good.Channels.Value.Channels) != 2 {
		t.Fatal("publication mutated the previous snapshot")
	}
	rpc.pendingErr, rpc.balanceErr = nil, nil
	rpc.pending.PendingOpenChannels = nil
	rpc.balance = "0"
	recovered := c.Collect(*config.Default(), true, rpc).Retain(partial)
	if !recovered.Balance.Fresh() || recovered.Balance.Value.TotalBalance != "0" || !recovered.Channels.Fresh() || len(recovered.Channels.Value.Channels) != 0 {
		t.Fatal("genuine zero/empty success did not replace stale data")
	}
}

func TestStatusDisabledSourcesAndSizeCache(t *testing.T) {
	c := statusCollectorFixture(t)
	calls := 0
	c.sources.size = func(context.Context) (string, error) { calls++; return "", errors.New("size unavailable") }
	cfg := config.Default()
	withoutWallet := c.Collect(*cfg, false, nil)
	if withoutWallet.Balance.Known() || withoutWallet.Node.Known() || withoutWallet.Node.Err != nil {
		t.Fatal("absent wallet was queried")
	}
	c.Collect(*cfg, false, nil)
	if calls != 1 || withoutWallet.LNDSize.Known() || withoutWallet.LNDSize.Err == nil {
		t.Fatal("size cache lost failure identity or repeated I/O")
	}
	// A new successful probe replaces a cached failure; a cancelled/failed probe
	// must never become a successful N/A value.
	c.sizeAttempt = time.Now().Add(-6 * time.Minute)
	c.sources.size = func(context.Context) (string, error) { return "2G", nil }
	recovered := c.Collect(*cfg, false, nil)
	if !recovered.LNDSize.Fresh() || recovered.LNDSize.Value != "2G" {
		t.Fatal("size cache failed to recover")
	}
	cfg.SyncthingEnabled = true
	enabled := c.Collect(*cfg, false, nil)
	if o, ok := enabled.Services["syncthing"]; !ok || !o.Fresh() || o.Value {
		t.Fatal("successful inactive service observation lost")
	}
}

func TestStatusCloseCancelsAndJoinsRead(t *testing.T) {
	c := statusCollectorFixture(t)
	entered := make(chan struct{})
	exited := make(chan struct{})
	c.sources.bitcoin = func(ctx context.Context, _ int) (bitcoin.BlockchainInfo, error) {
		close(entered)
		<-ctx.Done()
		close(exited)
		return bitcoin.BlockchainInfo{}, ctx.Err()
	}
	result := make(chan StatusSnapshot, 1)
	go func() { result <- c.Collect(*config.Default(), false, nil) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("read did not start")
	}
	c.Close()
	select {
	case <-exited:
	default:
		t.Fatal("Close returned before its reader")
	}
	select {
	case s := <-result:
		if !errors.Is(s.Bitcoin.Err, context.Canceled) {
			t.Fatal("lost read cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("collector did not finish")
	}
	if s := c.Collect(*config.Default(), false, nil); s.Services != nil {
		t.Fatal("closed collector admitted I/O")
	}
}

func TestStatusCollectWaitsForSlowSourceWithoutLosingFastResult(t *testing.T) {
	c := statusCollectorFixture(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	c.sources.bitcoin = func(ctx context.Context, _ int) (bitcoin.BlockchainInfo, error) {
		close(entered)
		select {
		case <-release:
			return bitcoin.BlockchainInfo{}, errors.New("slow failed read")
		case <-ctx.Done():
			return bitcoin.BlockchainInfo{}, ctx.Err()
		}
	}
	done := make(chan StatusSnapshot, 1)
	go func() { done <- c.Collect(*config.Default(), true, &statusLNDStub{balance: "20000"}) }()
	<-entered
	select {
	case <-done:
		t.Fatal("collector returned before its outstanding source")
	default:
	}
	close(release)
	select {
	case s := <-done:
		if !s.Balance.Fresh() || s.Balance.Value.TotalBalance != "20000" || s.Bitcoin.Known() || s.Bitcoin.Err == nil {
			t.Fatal("slow failure replaced an independent successful balance")
		}
	case <-time.After(time.Second):
		t.Fatal("collector did not finish")
	}
}
