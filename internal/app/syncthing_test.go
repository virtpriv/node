package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/virtpriv/node/internal/syncthing"
)

type syncFake struct {
	fail        string
	existing    bool
	shared      bool
	self        bool
	folderReads int
	writes      []string
	block       chan struct{}
	release     chan struct{}
}

func (f *syncFake) err(at string) error {
	if f.fail == at {
		return errors.New("injected " + at)
	}
	return nil
}
func (f *syncFake) CanonicalID(context.Context, string) (string, error) {
	if f.self {
		return "LOCAL", nil
	}
	return "REMOTE", f.err("id")
}
func (f *syncFake) LocalID(context.Context) (string, error) { return "LOCAL", f.err("local") }
func (f *syncFake) ListDevices(ctx context.Context) ([]syncthing.Device, error) {
	if f.block != nil {
		close(f.block)
		<-ctx.Done()
		<-f.release
		return nil, ctx.Err()
	}
	if f.fail == "verification read" && len(f.writes) > 0 {
		return nil, errors.New("verification read failed")
	}
	if f.existing {
		return []syncthing.Device{{DeviceID: "REMOTE", BackupKnown: true, BackupShared: f.shared}}, f.err("list")
	}
	return nil, f.err("list")
}
func (f *syncFake) ConfirmPrivacy(context.Context) error { return f.err("privacy") }
func (f *syncFake) BackupFolder(context.Context, string) (syncthing.BackupFolder, error) {
	f.folderReads++
	if f.folderReads == 2 && f.fail == "reread" {
		return syncthing.BackupFolder{}, errors.New("injected reread")
	}
	folder := syncthing.BackupFolder{}
	if f.shared {
		folder.Devices = []json.RawMessage{json.RawMessage(`{"deviceID":"REMOTE"}`)}
	}
	return folder, f.err("folder")
}
func (f *syncFake) AddDevice(context.Context, string) error {
	f.writes = append(f.writes, "add")
	f.existing = true
	return f.err("add")
}
func (f *syncFake) ShareBackup(context.Context, syncthing.BackupFolder, string) error {
	f.writes = append(f.writes, "share")
	if f.fail != "share absent" {
		f.shared = true
	}
	return f.err("share")
}
func (f *syncFake) RemoveDevice(context.Context, string) error {
	f.writes = append(f.writes, "remove")
	f.existing = f.fail == "device remains"
	f.shared = f.fail == "share remains"
	return f.err("remove")
}
func syncService(t *testing.T, f *syncFake) *Syncthing {
	t.Helper()
	s := NewSyncthing()
	t.Cleanup(s.Close)
	s.client = func() (SyncthingClient, error) { return f, nil }
	s.lock = func(action func() error) error { return action() }
	return s
}
func TestSyncthingPairOutcomesAndPreservation(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		fail                   string
		existing, shared, self bool
		want                   SyncthingOutcome
		writes                 string
	}{
		{name: "verification read", fail: "verification read", want: SyncthingUnknown, writes: "add,share"},
		{name: "share absent", fail: "share absent", want: SyncthingUnknown, writes: "add,share"},
		{name: "new pair", want: SyncthingComplete, writes: "add,share"},
		{name: "finish existing device", existing: true, want: SyncthingComplete, writes: "share"},
		{name: "duplicate", existing: true, shared: true, want: SyncthingNotChanged},
		{name: "self", self: true, want: SyncthingNotChanged},
		{name: "invalid ID", fail: "id", want: SyncthingNotChanged},
		{name: "privacy drift", fail: "privacy", want: SyncthingNotChanged},
		{name: "folder drift", fail: "folder", want: SyncthingNotChanged},
		{name: "read failure", fail: "list", want: SyncthingNotChanged},
		{name: "lost addition response", fail: "add", want: SyncthingUnknown, writes: "add"},
		{name: "folder read after addition", fail: "reread", want: SyncthingPartial, writes: "add"},
		{name: "lost share response", fail: "share", want: SyncthingUnknown, writes: "add,share"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &syncFake{fail: tc.fail, existing: tc.existing, shared: tc.shared, self: tc.self}
			result := syncService(t, f).Pair("input")
			if result.Outcome != tc.want || strings.Join(f.writes, ",") != tc.writes {
				t.Fatalf("result=%+v writes=%v", result, f.writes)
			}
			if (result.Err == nil) != (tc.want == SyncthingComplete) {
				t.Fatalf("wrong completion/error: %+v", result)
			}
		})
	}
}
func TestSyncthingRemovalAndContention(t *testing.T) {
	for _, tc := range []struct {
		name           string
		fail           string
		existing, self bool
		want           SyncthingOutcome
		writes         string
	}{
		{name: "verification read", existing: true, fail: "verification read", want: SyncthingUnknown, writes: "remove"},
		{name: "device remains", existing: true, fail: "device remains", want: SyncthingUnknown, writes: "remove"},
		{name: "share remains", existing: true, fail: "share remains", want: SyncthingUnknown, writes: "remove"},
		{name: "remove", existing: true, want: SyncthingComplete, writes: "remove"},
		{name: "missing", want: SyncthingNotChanged},
		{name: "self", self: true, want: SyncthingNotChanged},
		{name: "lost response", existing: true, fail: "remove", want: SyncthingUnknown, writes: "remove"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &syncFake{existing: tc.existing, self: tc.self, fail: tc.fail}
			result := syncService(t, f).Remove("input")
			if result.Outcome != tc.want || strings.Join(f.writes, ",") != tc.writes {
				t.Fatalf("result=%+v writes=%v", result, f.writes)
			}
		})
	}
	f := &syncFake{}
	s := syncService(t, f)
	s.lock = func(func() error) error { return errors.New("busy") }
	if result := s.Pair("input"); result.Outcome != SyncthingNotChanged || result.Err == nil || len(f.writes) != 0 {
		t.Fatalf("contending pair mutated: %+v", result)
	}
}
func TestSyncthingShutdownJoinsAndRefusesNewCalls(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &syncFake{block: make(chan struct{}), release: make(chan struct{})}
		s := syncService(t, f)
		done := make(chan error, 1)
		go func() { _, err := s.ListDevices(); done <- err }()
		<-f.block
		closed := false
		go func() { s.Close(); closed = true }()
		synctest.Wait()
		// Cancellation alone must not let Close return before the call finishes.
		returnedEarly := closed
		close(f.release)
		synctest.Wait()
		if returnedEarly || !closed {
			t.Fatal("shutdown did not wait for the active call")
		}
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if result := s.Pair("input"); result.Err == nil || len(f.writes) != 0 {
			t.Fatal("shutdown allowed mutation")
		}
	})
}
