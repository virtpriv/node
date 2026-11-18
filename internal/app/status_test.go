package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/virtpriv/node/internal/system"
)

func statusCollectorFixture(t *testing.T) *StatusCollector {
	t.Helper()
	c := NewStatusCollector()
	t.Cleanup(c.Close)
	c.sources = statusSources{
		service: func(context.Context, string) (bool, error) { return false, nil },
		disk: func(context.Context, string) (system.DiskInfo, error) {
			return system.DiskInfo{UsedBytes: 1_000_000_000}, nil
		},
		memory:  func() (system.MemInfo, error) { return system.MemInfo{UsedBytes: 1_000_000_000}, nil },
		reboot:  func() (system.RebootFlag, error) { return system.RebootFlag{}, nil },
		updates: func() (system.SecurityUpdates, error) { return system.SecurityUpdates{}, nil },
		timers:  func(context.Context) ([]system.AptTimer, error) { return nil, nil },
	}
	return c
}

func TestStatusPartialFailureAndRecovery(t *testing.T) {
	c := statusCollectorFixture(t)
	good := c.Collect()
	if o, ok := good.Services["tor"]; !ok || !o.Fresh() || o.Value {
		t.Fatal("successful inactive service observation lost")
	}
	failed := errors.New("injected read failure")
	c.sources.disk = func(context.Context, string) (system.DiskInfo, error) { return system.DiskInfo{}, failed }
	partial := c.Collect().Retain(good)
	if partial.Disk.Fresh() || partial.Disk.Value.UsedBytes != 1_000_000_000 || partial.Disk.ObservedAt != good.Disk.ObservedAt ||
		!errors.Is(partial.Disk.Err, failed) || !partial.Memory.Fresh() {
		t.Fatal("partial failure lost last-good data or mislabeled its freshness")
	}
	if !good.Disk.Fresh() {
		t.Fatal("publication mutated the previous snapshot")
	}
	c.sources.disk = func(context.Context, string) (system.DiskInfo, error) { return system.DiskInfo{}, nil }
	recovered := c.Collect().Retain(partial)
	if !recovered.Disk.Fresh() || recovered.Disk.Value.UsedBytes != 0 {
		t.Fatal("genuine zero success did not replace stale data")
	}
}

func TestStatusCloseCancelsAndJoinsRead(t *testing.T) {
	c := statusCollectorFixture(t)
	entered := make(chan struct{})
	exited := make(chan struct{})
	c.sources.disk = func(ctx context.Context, _ string) (system.DiskInfo, error) {
		close(entered)
		<-ctx.Done()
		close(exited)
		return system.DiskInfo{}, ctx.Err()
	}
	result := make(chan StatusSnapshot, 1)
	go func() { result <- c.Collect() }()
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
		if !errors.Is(s.Disk.Err, context.Canceled) {
			t.Fatal("lost read cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("collector did not finish")
	}
	if s := c.Collect(); s.Services != nil {
		t.Fatal("closed collector admitted I/O")
	}
}

func TestStatusCollectWaitsForSlowSourceWithoutLosingFastResult(t *testing.T) {
	c := statusCollectorFixture(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	c.sources.disk = func(ctx context.Context, _ string) (system.DiskInfo, error) {
		close(entered)
		select {
		case <-release:
			return system.DiskInfo{}, errors.New("slow failed read")
		case <-ctx.Done():
			return system.DiskInfo{}, ctx.Err()
		}
	}
	done := make(chan StatusSnapshot, 1)
	go func() { done <- c.Collect() }()
	<-entered
	select {
	case <-done:
		t.Fatal("collector returned before its outstanding source")
	default:
	}
	close(release)
	select {
	case s := <-done:
		if !s.Memory.Fresh() || s.Memory.Value.UsedBytes != 1_000_000_000 || s.Disk.Known() || s.Disk.Err == nil {
			t.Fatal("slow failure replaced an independent successful read")
		}
	case <-time.After(time.Second):
		t.Fatal("collector did not finish")
	}
}

// Debian checks once a day at a random time, so two good runs can be about 42
// hours apart. Only a gap past 48 hours, or no run 48 hours after install,
// is a warning.
func TestSecurityUpdatesWarnOnlyAfterTwoDays(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	installed := now.Add(-30 * 24 * time.Hour)
	for _, tc := range []struct {
		name  string
		in    system.SecurityUpdates
		state SecurityUpdatesState
		since time.Time
	}{
		{"healthy gap", system.SecurityUpdates{LastRun: now.Add(-42 * time.Hour), Configured: installed}, SecurityUpdatesCurrent, now.Add(-42 * time.Hour)},
		{"run overdue", system.SecurityUpdates{LastRun: now.Add(-49 * time.Hour), Configured: installed}, SecurityUpdatesOverdue, now.Add(-49 * time.Hour)},
		{"fresh install", system.SecurityUpdates{Configured: now.Add(-20 * time.Hour)}, SecurityUpdatesNotRunYet, now.Add(-20 * time.Hour)},
		{"never ran", system.SecurityUpdates{Configured: now.Add(-49 * time.Hour)}, SecurityUpdatesOverdue, now.Add(-49 * time.Hour)},
		{"stamps older than the install", system.SecurityUpdates{LastRun: now.Add(-90 * 24 * time.Hour), Configured: now.Add(-20 * time.Hour)}, SecurityUpdatesNotRunYet, now.Add(-20 * time.Hour)},
		{"no run since an old install", system.SecurityUpdates{LastRun: now.Add(-90 * 24 * time.Hour), Configured: now.Add(-49 * time.Hour)}, SecurityUpdatesOverdue, now.Add(-49 * time.Hour)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, since := SecurityUpdatesStatus(tc.in, now)
			if state != tc.state || !since.Equal(tc.since) {
				t.Fatalf("got %v since %v, want %v since %v", state, since, tc.state, tc.since)
			}
		})
	}
}

func TestTorObservationUsesWorkerWhileWrapperIsActive(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\ncase \"$*\" in\n 'show --property=ActiveState --value tor@default.service') echo inactive;;\n 'show --property=ActiveState --value tor.service'|'show --property=ActiveState --value tor') echo active;;\n *) exit 2;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	collector := NewStatusCollector()
	defer collector.Close()
	active, err := collector.sources.service(t.Context(), "tor")
	if err != nil || active {
		t.Fatalf("worker observation: active %v, err %v", active, err)
	}
}
