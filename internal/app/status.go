package app

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

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

type StatusSnapshot struct {
	Services map[string]Observation[bool]
	Disk     Observation[system.DiskInfo]
	Memory   Observation[system.MemInfo]
	Reboot   Observation[system.RebootFlag]
	Updates  Observation[system.SecurityUpdates]
	Timers   Observation[[]system.AptTimer]
}

// Retain combines failures with the last good values of the previous snapshot.
func (s StatusSnapshot) Retain(previous StatusSnapshot) StatusSnapshot {
	for name, value := range s.Services {
		s.Services[name] = retain(value, previous.Services[name])
	}
	s.Disk = retain(s.Disk, previous.Disk)
	s.Memory = retain(s.Memory, previous.Memory)
	s.Reboot = retain(s.Reboot, previous.Reboot)
	s.Updates = retain(s.Updates, previous.Updates)
	s.Timers = retain(s.Timers, previous.Timers)
	return s
}

type statusSources struct {
	service func(context.Context, string) (bool, error)
	disk    func(context.Context, string) (system.DiskInfo, error)
	memory  func() (system.MemInfo, error)
	reboot  func() (system.RebootFlag, error)
	updates func() (system.SecurityUpdates, error)
	timers  func(context.Context) ([]system.AptTimer, error)
}

// StatusCollector owns observation I/O for one terminal session. Scheduling
// and publication stay with the caller. The reads share a deadline and are
// not an atomic snapshot.
type StatusCollector struct {
	sources statusSources
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	closed  bool
	calls   sync.WaitGroup
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
		memory: system.ReadMemory,
		reboot: system.ReadRebootFlag,
		timers: system.ReadAptTimers,
		updates: func() (system.SecurityUpdates, error) {
			return system.ReadSecurityUpdates(paths.AptNightly)
		},
	}}
}

// Close cancels and joins local readers.
func (c *StatusCollector) Close() {
	c.mu.Lock()
	c.closed = true
	c.cancel()
	c.mu.Unlock()
	c.calls.Wait()
}

// StatusServices are the services System lists.
func StatusServices() []string { return []string{"tor"} }

func (c *StatusCollector) Collect() StatusSnapshot {
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
	for _, name := range StatusServices() {
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
	wg.Go(func() { s.Timers = observe(c.sources.timers(ctx)) })
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

// rebootLateAfter is when a standing restart flag is late. The nightly run
// restarts the machine right after it, and Debian's timer starts the run once
// a day at 6:00 with up to an hour's random delay. So the longest normal wait
// is about 25 hours, plus the run itself through Tor.
const rebootLateAfter = 30 * time.Hour

// RebootLate reports whether the flag has stood longer than a nightly run
// should take to act on it.
func RebootLate(f system.RebootFlag, now time.Time) bool {
	return f.Required && now.Sub(f.Since) > rebootLateAfter
}

// SystemWarning reports whether the owner should look at System. That is
// when no nightly run succeeded for 48 hours, a nightly timer is off or
// stopped, a service stopped, or a restart is late. A restart
// that waits for the next night leaves the owner nothing to do. Each reading
// counts by its last known value, so a failed read keeps a warning it
// showed, and one never read sets none.
func (s StatusSnapshot) SystemWarning(now time.Time) bool {
	if s.Updates.Known() {
		if state, _ := SecurityUpdatesStatus(s.Updates.Value, now); state == SecurityUpdatesOverdue {
			return true
		}
	}
	if s.Timers.Known() && slices.ContainsFunc(s.Timers.Value, func(t system.AptTimer) bool { return t.State != system.AptTimerRunning }) {
		return true
	}
	for _, o := range s.Services {
		if o.Known() && !o.Value {
			return true
		}
	}
	return s.Reboot.Known() && RebootLate(s.Reboot.Value, now)
}
