package jobs

import (
	"errors"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/virtpriv/node/internal/api"
)

func testStore(t *testing.T, alive func(id string) (bool, error)) *Store {
	t.Helper()
	// The lock refuses a symlinked folder, and macOS's temporary folder
	// starts with one (/var).
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &Store{
		Dir: dir + "/jobs", LockPath: dir + "/operations.lock",
		unitAlive:  alive,
		bootID:     func() (string, error) { return "boot-a", nil },
		nodeUpdate: func() (bool, string, error) { return false, "", nil },
		now:        time.Now,
	}
}

// J1: two clients starting a change at the same moment. Both may pass the
// check made without the lock. Only one is accepted, and the other's refusal
// names it.
func TestTwoChangesAtOnceAcceptOneAndRefuseTheOther(t *testing.T) {
	var mu sync.Mutex
	running := map[string]bool{}
	s := testStore(t, func(id string) (bool, error) {
		mu.Lock()
		defer mu.Unlock()
		return running[id], nil
	})
	start := func(id string) error {
		// systemd-run takes a moment, and the lock is held throughout.
		time.Sleep(50 * time.Millisecond)
		mu.Lock()
		running[id] = true
		mu.Unlock()
		return nil
	}
	var wg sync.WaitGroup
	gate := make(chan struct{})
	jobs := make([]api.Job, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Go(func() {
			<-gate
			jobs[i], errs[i] = s.Accept(api.KindPackageUpdate, "Update Packages", nil, 1000, RuleAll, start)
		})
	}
	close(gate)
	wg.Wait()

	accepted, refused := -1, -1
	for i, err := range errs {
		if err == nil {
			accepted = i
		} else {
			refused = i
		}
	}
	if accepted < 0 || refused < 0 {
		t.Fatalf("want one accepted and one refused, got errors %v", errs)
	}
	busy, ok := errors.AsType[*BusyError](errs[refused])
	if !ok || busy.Job.ID != jobs[accepted].ID {
		t.Fatalf("refusal %v does not name the accepted job %s", errs[refused], jobs[accepted].ID)
	}
	ids, err := s.ids()
	if err != nil || len(ids) != 1 || ids[0] != jobs[accepted].ID {
		t.Fatalf("job folders = %v, %v, want only %s", ids, err, jobs[accepted].ID)
	}
}

// J3, J4, J5: what an unfinished record means once its service is gone, and
// after a restart.
func TestResolveUnfinishedRecords(t *testing.T) {
	now := time.Date(2026, 11, 9, 20, 0, 0, 0, time.UTC)
	rec := func(kind string, state api.JobState, boot string) Record {
		return Record{Schema: schema, BootID: boot, Job: api.Job{ID: "20261109-195900-aaaaaa", Kind: kind, State: state}}
	}
	type want struct {
		state          api.JobState
		nothingChanged bool
		restarted      bool
		changed        bool
	}
	for _, c := range []struct {
		name        string
		r           Record
		alive, held bool
		want        want
	}{
		{"running", rec(api.KindPackageUpdate, api.JobStarted, "boot-a"), true, false, want{api.JobStarted, false, false, false}},
		{"starting while vpnd still holds the lock", rec(api.KindPackageUpdate, api.JobAccepted, "boot-a"), false, true, want{api.JobAccepted, false, false, false}},
		{"accepted and never started", rec(api.KindPackageUpdate, api.JobAccepted, "boot-a"), false, false, want{api.JobFailed, true, false, true}},
		{"started then killed", rec(api.KindPackageUpdate, api.JobStarted, "boot-a"), false, false, want{api.JobUnknown, false, false, true}},
		{"cut by a power cut", rec(api.KindPackageUpdate, api.JobStarted, "boot-b"), false, false, want{api.JobUnknown, false, false, true}},
		{"accepted before a power cut", rec(api.KindPackageUpdate, api.JobAccepted, "boot-b"), false, false, want{api.JobFailed, true, false, true}},
		{"a reboot after its restart", rec(api.KindReboot, api.JobStarted, "boot-b"), false, false, want{api.JobDone, false, true, true}},
		{"a reboot settled unknown during its shutdown", rec(api.KindReboot, api.JobUnknown, "boot-b"), false, false, want{api.JobDone, false, true, true}},
		{"a reboot that wrote done before its restart", rec(api.KindReboot, api.JobDone, "boot-b"), false, false, want{api.JobDone, false, true, true}},
		{"finished stays", rec(api.KindPackageUpdate, api.JobDone, "boot-b"), false, false, want{api.JobDone, false, false, false}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, changed := resolve(c.r, "boot-a", c.alive, c.held, now)
			if g := (want{got.State, got.NothingChanged, got.Restarted, changed}); g != c.want {
				t.Fatalf("resolve = %+v, want %+v", g, c.want)
			}
			if changed && c.r.State != api.JobDone && !got.Ended.Equal(now) {
				t.Fatalf("ended = %v, want %v", got.Ended, now)
			}
		})
	}
}

// J7: pruning removes only finished records, oldest first, and keeps the
// newest ones.
func TestPruneRemovesOnlyTheOldestFinished(t *testing.T) {
	r := func(id string, state api.JobState) Record {
		return Record{Job: api.Job{ID: id, State: state}}
	}
	records := []Record{
		r("20261109-100500-000005", api.JobDone),
		r("20261109-100000-000000", api.JobStarted), // oldest, unfinished
		r("20261109-100100-000001", api.JobFailed),
		r("20261109-100400-000004", api.JobUnknown),
		r("20261109-100200-000002", api.JobDone),
		r("20261109-100600-000006", api.JobAccepted),
		r("20261109-100300-000003", api.JobDone),
	}
	got := prune(records, 3)
	slices.Sort(got)
	want := []string{"20261109-100100-000001", "20261109-100200-000002"}
	if !slices.Equal(got, want) {
		t.Fatalf("prune = %v, want %v", got, want)
	}
	if got := prune(records, 10); len(got) != 0 {
		t.Fatalf("prune under the limit = %v, want none", got)
	}
}
