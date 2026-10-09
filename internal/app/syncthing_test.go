package app

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

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
func (f *syncFake) BackupDelivery(context.Context, []syncthing.Device) (map[string]syncthing.Delivery, error) {
	return nil, f.err("delivery")
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

// The copy line comes from systemd, not from Syncthing. Syncthing being down
// must not hide whether the node keeps its own copy current.
func TestSyncthingObservationKeepsTheCopyLineWhenSyncthingIsDown(t *testing.T) {
	s := NewSyncthing()
	t.Cleanup(s.Close)
	s.client = func() (SyncthingClient, error) { return nil, errors.New("daemon down") }
	at := time.Date(2026, 10, 8, 14, 3, 12, 0, time.UTC)
	s.backupCopy = func(context.Context) (BackupCopy, error) {
		return BackupCopy{State: BackupCopyCurrent, At: at}, nil
	}
	o := s.Observe()
	if o.CopyErr != nil || o.Copy.State != BackupCopyCurrent || !o.Copy.At.Equal(at) {
		t.Fatalf("copy line lost with Syncthing down: %+v %v", o.Copy, o.CopyErr)
	}
	if o.DevicesErr == nil || o.DeliveryErr == nil {
		t.Fatal("Syncthing down was not reported for the devices")
	}
}

// Each state comes from systemd's own record of the timer and the copy job,
// judged at the moment the screen draws. The records below have the shape
// systemctl show prints on Debian 13.
func TestBackupCopyStateFromSystemdRecord(t *testing.T) {
	now := time.Date(2026, 10, 8, 14, 4, 0, 0, time.UTC)
	stamp := func(ago time.Duration) string { return "@" + strconv.FormatInt(now.Add(-ago).Unix(), 10) }
	record := func(set ...string) string {
		timer := map[string]string{"Id": "lnd-backup-check.timer", "LoadState": "loaded", "ActiveState": "active", "ActiveEnterTimestamp": ""}
		export := map[string]string{"Id": "lnd-backup-export.service", "LoadState": "loaded", "ActiveState": "inactive",
			"Result": "success", "ExecMainStartTimestamp": "", "ExecMainExitTimestamp": "", "InactiveEnterTimestamp": ""}
		for i := 0; i+1 < len(set); i += 2 {
			unit, key, _ := strings.Cut(set[i], ".")
			if unit == "timer" {
				timer[key] = set[i+1]
			} else {
				export[key] = set[i+1]
			}
		}
		block := func(m map[string]string) string {
			var b strings.Builder
			for k, v := range m {
				b.WriteString(k + "=" + v + "\n")
			}
			return b.String()
		}
		return block(timer) + "\n" + block(export)
	}
	const none = -1
	for _, tc := range []struct {
		name  string
		out   string
		state BackupCopyState
		at    time.Duration
	}{
		{"checked and equal", record("export.ExecMainExitTimestamp", stamp(40*time.Second)), BackupCopyCurrent, 40 * time.Second},
		{"timer stopped", record("timer.ActiveState", "inactive", "export.ExecMainExitTimestamp", stamp(40*time.Second)), BackupCopyOff, none},
		{"timer failed", record("timer.ActiveState", "failed"), BackupCopyOff, none},
		{"no timer, old units", "Result=success\nId=lnd-backup-check.timer\nLoadState=not-found\nActiveState=inactive\n\nResult=success\nExecMainExitTimestamp=@1791334331\nId=lnd-backup-export.service\nLoadState=loaded\nActiveState=inactive\n", BackupCopyNoTimer, none},
		{"last check failed", record("export.ActiveState", "failed", "export.Result", "exit-code",
			"export.ExecMainExitTimestamp", stamp(31*time.Second), "export.InactiveEnterTimestamp", stamp(30*time.Second)), BackupCopyFailed, 30 * time.Second},
		{"failed before running", record("export.ActiveState", "failed", "export.Result", "resources",
			"export.ExecMainExitTimestamp", stamp(10*time.Minute), "export.InactiveEnterTimestamp", stamp(20*time.Second)), BackupCopyFailed, 20 * time.Second},
		{"failed with no time", record("export.ActiveState", "failed", "export.Result", "resources"), BackupCopyFailed, none},
		{"checks late", record("export.ExecMainExitTimestamp", stamp(4*time.Minute)), BackupCopyLate, 4 * time.Minute},
		{"checking now", record("export.ActiveState", "activating", "export.ExecMainStartTimestamp", stamp(10*time.Second)), BackupCopyChecking, none},
		{"copy stuck", record("export.ActiveState", "activating", "export.ExecMainStartTimestamp", stamp(5*time.Minute)), BackupCopyStuck, 5 * time.Minute},
		{"none since boot", record("timer.ActiveEnterTimestamp", stamp(30*time.Second)), BackupCopyNotYet, none},
		{"timer on, copy never ran", record("timer.ActiveEnterTimestamp", stamp(5*time.Minute)), BackupCopyLate, none},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := parseBackupCopy(tc.out)
			got := parsed.Judged(now)
			if err != nil || got.State != tc.state {
				t.Fatalf("got %+v, %v, want state %v", got, err, tc.state)
			}
			if tc.at != none && !got.At.Equal(now.Add(-tc.at)) {
				t.Fatalf("time %v, want %v", got.At, now.Add(-tc.at))
			}
			if tc.at == none && (tc.state == BackupCopyFailed || tc.state == BackupCopyLate) && !got.At.IsZero() {
				t.Fatalf("a time no run finished at is shown: %v", got.At)
			}
		})
	}
	for name, out := range map[string]string{
		"empty":         "",
		"no copy job":   "Id=lnd-backup-check.timer\nLoadState=loaded\nActiveState=active\n",
		"bad time":      record("export.ExecMainExitTimestamp", "@soon"),
		"copy job gone": record("export.LoadState", "not-found", "export.ExecMainExitTimestamp", stamp(time.Second)),
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := parseBackupCopy(out); err == nil {
				t.Fatalf("garbled record gave %+v", got)
			}
		})
	}
}
