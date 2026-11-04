package host

import (
	"errors"
	"testing"
	"time"
)

// ── Waiting for Debian's own apt job ─────────────────────

// fakeClock moves only when the code under test sleeps.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time                      { return c.now }
func (c *fakeClock) Sleep(d time.Duration)               { c.now = c.now.Add(d) }
func (c *fakeClock) since(start time.Time) time.Duration { return c.now.Sub(start) }

// The messages apt 3.0.3 and unattended-upgrades 2.12 print, in the C
// locale, when another apt holds a lock.
var aptLockMessages = []string{
	"apt-get update --error-on=any -qq: exit status 100: E: Could not get lock /var/lib/apt/lists/lock. It is held by process 1234 (apt-get)",
	"apt-get install -y -qq fail2ban: exit status 100: E: Could not get lock /var/lib/dpkg/lock-frontend. It is held by process 912 (unattended-upgr)",
	"unattended-upgrade: exit status 1: Cache lock can not be acquired, exiting",
	// When another unattended-upgrade runs, unattended-upgrade logs why only
	// to its own log file, so its failure carries no message.
	"unattended-upgrade : exit status 1: ",
}

func TestDebianJobStillRunningAfterAnHour(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0)}
	start := clock.now
	job := debianAptJob{running: func() (bool, error) { return true, nil }, sleep: clock.Sleep, now: clock.Now}
	err := job.run(func() error {
		t.Fatal("ran while Debian's job was running")
		return nil
	})
	if !errors.Is(err, errDebianJobStillRunning) {
		t.Fatalf("error %v, want %v", err, errDebianJobStillRunning)
	}
	if waited := clock.since(start); waited < time.Hour || waited > time.Hour+time.Minute {
		t.Fatalf("gave up after %v, want one hour", waited)
	}
}

// Debian's job can start between the wait and the command. A lock error, or a
// failure while Debian's job is running, then waits once more and tries once
// more. A second failure is the command's own.
func TestDebianJobLockErrorWaitsOnceMore(t *testing.T) {
	for _, msg := range aptLockMessages {
		for _, tc := range []struct {
			name      string
			failures  int
			wantRuns  int
			wantError bool
		}{
			{name: "the second try runs", failures: 1, wantRuns: 2},
			{name: "a second lock error stops", failures: 2, wantRuns: 2, wantError: true},
		} {
			t.Run(tc.name+": "+msg, func(t *testing.T) {
				clock := &fakeClock{now: time.Unix(1_800_000_000, 0)}
				var busyUntil time.Time
				runs := 0
				job := debianAptJob{
					running: func() (bool, error) { return clock.now.Before(busyUntil), nil },
					sleep:   clock.Sleep, now: clock.Now,
				}
				err := job.run(func() error {
					runs++
					if runs <= tc.failures {
						busyUntil = clock.now.Add(5 * time.Minute)
						return errors.New(msg)
					}
					if clock.now.Before(busyUntil) {
						t.Fatal("tried again while Debian's job was running")
					}
					return nil
				})
				if runs != tc.wantRuns || (err != nil) != tc.wantError {
					t.Fatalf("ran %d times, error %v", runs, err)
				}
				if tc.wantError && err.Error() != msg {
					t.Fatalf("error %q, want apt's own %q", err, msg)
				}
			})
		}
	}
}

func TestDebianJobOtherErrorsAreNotRetried(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0)}
	failure := errors.New("apt-get update --error-on=any -qq: exit status 100: E: Failed to fetch tor+https://deb.debian.org/debian/dists/trixie/InRelease")
	runs := 0
	job := debianAptJob{running: func() (bool, error) { return false, nil }, sleep: clock.Sleep, now: clock.Now}
	err := job.run(func() error { runs++; return failure })
	if runs != 1 || !errors.Is(err, failure) {
		t.Fatalf("ran %d times, error %v", runs, err)
	}
}

// dpkg --audit prints nothing on a healthy machine. Anything it prints, or a
// failure to run it, fails Update Packages.
func TestPackageAuditResult(t *testing.T) {
	failure := errors.New("dpkg --audit: signal: killed")
	for _, tc := range []struct {
		out  string
		err  error
		want bool
	}{
		{out: "", want: false},
		{out: " package is only half configured\n", want: true},
		{err: failure, want: true},
	} {
		if err := packageAuditResult(tc.out, tc.err); (err != nil) != tc.want || (tc.err != nil && !errors.Is(err, failure)) {
			t.Fatalf("audit %q, %v: %v", tc.out, tc.err, err)
		}
	}
}
