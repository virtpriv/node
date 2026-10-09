package app

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/virtpriv/node/internal/bitcoin"
	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/helper"
	"github.com/virtpriv/node/internal/lndrpc"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/servicecontrol"
	"github.com/virtpriv/node/internal/system"
)

// Observation retains the last successful value and the latest read error.
// A zero observation is unavailable, not a successful zero or empty result.
type Observation[T any] struct {
	Value      T
	ObservedAt time.Time
	Err        error
}

func (o Observation[T]) Known() bool { return !o.ObservedAt.IsZero() }
func (o Observation[T]) Fresh() bool { return o.Known() && o.Err == nil }

func observe[T any](value T, err error) Observation[T] {
	o := Observation[T]{Err: err}
	if err == nil {
		o.Value, o.ObservedAt = value, time.Now()
	}
	return o
}

func retain[T any](next, previous Observation[T]) Observation[T] {
	if !next.Known() && next.Err != nil {
		next.Value, next.ObservedAt = previous.Value, previous.ObservedAt
	}
	return next
}

type StatusChannel struct {
	lndrpc.Channel
	Pending bool
}

type ChannelStatus struct {
	Channels []StatusChannel
	Pending  lndrpc.PendingChannelInfo
}

type StatusSnapshot struct {
	Services    map[string]Observation[bool]
	Disk        Observation[system.DiskInfo]
	Memory      Observation[system.MemInfo]
	Bitcoin     Observation[bitcoin.BlockchainInfo]
	LNDSize     Observation[string]
	Reboot      Observation[bool]
	Updates     Observation[system.SecurityUpdates]
	PublicIP    Observation[string]
	WalletState Observation[lndrpc.WalletState]
	Node        Observation[lndrpc.NodeInfo]
	Balance     Observation[lndrpc.WalletBalance]
	Channels    Observation[ChannelStatus]
}

// WalletUnavailable preserves observations for display without treating a
// failed presence read as a change of wallet or proof of current balances.
func (s StatusSnapshot) WalletUnavailable(err error) StatusSnapshot {
	s.WalletState.Err, s.Node.Err, s.Balance.Err, s.Channels.Err = err, err, err, err
	return s
}

// Retain combines failures with last-good values from the same observation
// scope. Callers must discard the previous snapshot when that scope changes.
func (s StatusSnapshot) Retain(previous StatusSnapshot) StatusSnapshot {
	for name, value := range s.Services {
		s.Services[name] = retain(value, previous.Services[name])
	}
	s.Disk = retain(s.Disk, previous.Disk)
	s.Memory = retain(s.Memory, previous.Memory)
	s.Bitcoin = retain(s.Bitcoin, previous.Bitcoin)
	s.LNDSize = retain(s.LNDSize, previous.LNDSize)
	s.Reboot = retain(s.Reboot, previous.Reboot)
	s.Updates = retain(s.Updates, previous.Updates)
	s.PublicIP = retain(s.PublicIP, previous.PublicIP)
	s.WalletState = retain(s.WalletState, previous.WalletState)
	s.Node = retain(s.Node, previous.Node)
	s.Balance = retain(s.Balance, previous.Balance)
	s.Channels = retain(s.Channels, previous.Channels)
	return s
}

type StatusLND interface {
	GetStateContext(context.Context) (lndrpc.WalletState, error)
	GetInfoContext(context.Context) (*lndrpc.NodeInfo, error)
	GetWalletBalanceContext(context.Context) (*lndrpc.WalletBalance, error)
	ListChannelsContext(context.Context) ([]lndrpc.Channel, error)
	GetPendingChannelsContext(context.Context) (*lndrpc.PendingChannelInfo, error)
}

type statusSources struct {
	service  func(context.Context, string) (bool, error)
	disk     func(context.Context, string) (system.DiskInfo, error)
	memory   func() (system.MemInfo, error)
	bitcoin  func(context.Context, int) (bitcoin.BlockchainInfo, error)
	size     func(context.Context) (string, error)
	reboot   func() (bool, error)
	updates  func() (system.SecurityUpdates, error)
	publicIP func(context.Context) (string, error)
}

// StatusCollector owns observation I/O and its cache for one terminal session.
// Scheduling and publication stay with the caller. RPCs and helper reads share
// a deadline. Separate daemon RPCs are not an atomic snapshot.
type StatusCollector struct {
	sources     statusSources
	ctx         context.Context
	cancel      context.CancelFunc
	mu          sync.Mutex
	closed      bool
	calls       sync.WaitGroup
	sizeMu      sync.Mutex
	size        Observation[string]
	sizeAttempt time.Time
}

func readNodeService(ctx context.Context, name string) (bool, error) {
	unit := servicecontrol.Unit(name)
	if unit == "" {
		return false, errors.New("unknown node service")
	}
	return system.ReadServiceActive(ctx, unit)
}

func NewStatusCollector() *StatusCollector {
	ctx, cancel := context.WithCancel(context.Background())
	return &StatusCollector{ctx: ctx, cancel: cancel, sources: statusSources{
		service: readNodeService, disk: system.ReadDisk,
		memory: system.ReadMemory, bitcoin: bitcoin.GetBlockchainInfo,
		reboot: system.ReadRebootRequired, publicIP: system.ReadPublicIPv4,
		updates: func() (system.SecurityUpdates, error) {
			return system.ReadSecurityUpdates(paths.AutoUpgrades)
		},
		size: func(ctx context.Context) (string, error) {
			s, err := helper.StartContext(ctx, helper.VerbDirSize, helper.DirSizeParams{Which: "lnd"})
			if err != nil {
				return "", err
			}
			var result helper.DirSizeResult
			if err := s.Wait(&result); err != nil {
				return "", err
			}
			if result.Size == "" || result.Size == "N/A" {
				return "", errors.New("LND directory size unavailable")
			}
			return result.Size, nil
		},
	}}
}

// Close cancels and joins local readers. Existing shared LND client mutex waits
// can delay the join. Automatic reconnection is not owned by this collector.
func (c *StatusCollector) Close() {
	c.mu.Lock()
	c.closed = true
	c.cancel()
	c.mu.Unlock()
	c.calls.Wait()
}

func StatusServices(cfg config.AppConfig) []string {
	names := []string{"tor", "bitcoind"}
	if cfg.HasLND() {
		names = append(names, "lnd")
	}
	if cfg.SyncthingEnabled {
		names = append(names, "syncthing")
	}
	return names
}

func (c *StatusCollector) lndSize(ctx context.Context) Observation[string] {
	c.sizeMu.Lock()
	defer c.sizeMu.Unlock()
	if !c.sizeAttempt.IsZero() && time.Since(c.sizeAttempt) < 5*time.Minute {
		return c.size
	}
	c.size = retain(observe(c.sources.size(ctx)), c.size)
	c.sizeAttempt = time.Now()
	return c.size
}

func (c *StatusCollector) Collect(cfg config.AppConfig, walletExists bool, client StatusLND) StatusSnapshot {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return StatusSnapshot{}
	}
	c.calls.Add(1)
	c.mu.Unlock()
	defer c.calls.Done()
	ctx, cancel := context.WithTimeout(c.ctx, 30*time.Second)
	defer cancel()
	s := StatusSnapshot{Services: make(map[string]Observation[bool])}
	var wg sync.WaitGroup
	var servicesMu sync.Mutex
	for _, name := range StatusServices(cfg) {
		wg.Go(func() {
			value := observe(c.sources.service(ctx, name))
			servicesMu.Lock()
			s.Services[name] = value
			servicesMu.Unlock()
		})
	}
	wg.Go(func() { s.Disk = observe(c.sources.disk(ctx, "/")) })
	wg.Go(func() { s.Memory = observe(c.sources.memory()) })
	wg.Go(func() { s.Reboot = observe(c.sources.reboot()) })
	wg.Go(func() { s.Updates = observe(c.sources.updates()) })
	wg.Go(func() {
		profile, err := cfg.NetworkConfig()
		if err != nil {
			s.Bitcoin.Err = err
			return
		}
		s.Bitcoin = observe(c.sources.bitcoin(ctx, profile.RPCPort))
	})
	if cfg.P2PMode == "hybrid" {
		wg.Go(func() {
			s.PublicIP = observe(c.sources.publicIP(ctx))
		})
	}
	if cfg.HasLND() {
		wg.Go(func() { s.LNDSize = c.lndSize(ctx) })
	}
	if cfg.HasLND() && walletExists {
		unavailable := errors.New("LND client unavailable")
		s.WalletState.Err, s.Node.Err, s.Balance.Err, s.Channels.Err = unavailable, unavailable, unavailable, unavailable
		if client != nil {
			wg.Go(func() { s.WalletState = observe(client.GetStateContext(ctx)) })
			wg.Go(func() {
				value, err := client.GetInfoContext(ctx)
				if err != nil {
					s.Node.Err = err
					return
				}
				s.Node = observe(*value, nil)
			})
			wg.Go(func() {
				value, err := client.GetWalletBalanceContext(ctx)
				if err != nil {
					s.Balance.Err = err
					return
				}
				s.Balance = observe(*value, nil)
			})
			wg.Go(func() {
				channels, err := client.ListChannelsContext(ctx)
				pending, pendingErr := client.GetPendingChannelsContext(ctx)
				if err = errors.Join(err, pendingErr); err != nil {
					s.Channels.Err = err
					return
				}
				// Publish the channel view only when both constituent reads succeed.
				value := ChannelStatus{Pending: *pending}
				for _, ch := range channels {
					value.Channels = append(value.Channels, StatusChannel{Channel: ch})
				}
				for _, ch := range pending.PendingOpenChannels {
					value.Channels = append(value.Channels, StatusChannel{Pending: true, Channel: lndrpc.Channel{
						ChannelPoint: ch.ChannelPoint, RemotePubkey: ch.RemotePubkey, PeerAlias: ch.PeerAlias, Capacity: ch.Capacity, LocalBalance: ch.LocalBalance,
					}})
				}
				s.Channels = observe(value, nil)
			})
		}
	}
	wg.Wait()
	return s
}

// SecurityUpdatesState is how the System screen reports Debian's automatic
// security updates.
type SecurityUpdatesState int

const (
	SecurityUpdatesCurrent SecurityUpdatesState = iota
	SecurityUpdatesNotRunYet
	SecurityUpdatesOverdue
)

// securityUpdatesWarnAfter allows for Debian's timing: apt-daily.timer fires
// at 6:00 and 18:00 with up to 12 hours of random delay, and a run counts once
// per calendar day, so two successful runs can be about 42 hours apart on a
// healthy node.
const securityUpdatesWarnAfter = 48 * time.Hour

// SecurityUpdatesStatus judges the recorded runs at now. The time returned is
// the last successful run, or the install of the settings when none has run
// since.
func SecurityUpdatesStatus(u system.SecurityUpdates, now time.Time) (SecurityUpdatesState, time.Time) {
	// A provider image can hold stamps from before vpn's install. Only a run
	// after vpn wrote its settings counts.
	if u.LastRun.IsZero() || u.LastRun.Before(u.Configured) {
		if now.Sub(u.Configured) > securityUpdatesWarnAfter {
			return SecurityUpdatesOverdue, u.Configured
		}
		return SecurityUpdatesNotRunYet, u.Configured
	}
	if now.Sub(u.LastRun) > securityUpdatesWarnAfter {
		return SecurityUpdatesOverdue, u.LastRun
	}
	return SecurityUpdatesCurrent, u.LastRun
}
