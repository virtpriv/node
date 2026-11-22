package host

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/virtpriv/node/internal/failure"
)

// ── Waiting for Debian's own apt job ─────────────────────

// fakeClock moves only when the code under test sleeps.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }
func (c *fakeClock) Sleep(_ context.Context, d time.Duration) error {
	c.now = c.now.Add(d)
	return nil
}
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
	job := debianAptJob{say: func(string) {}, running: func() (bool, error) { return true, nil }, sleep: clock.Sleep, now: clock.Now}
	err := job.run(context.Background(), "", func() error {
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
					say:     func(string) {},
					running: func() (bool, error) { return clock.now.Before(busyUntil), nil },
					sleep:   clock.Sleep, now: clock.Now,
				}
				err := job.run(context.Background(), "", func() error {
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
	job := debianAptJob{say: func(string) {}, running: func() (bool, error) { return false, nil }, sleep: clock.Sleep, now: clock.Now}
	err := job.run(context.Background(), "", func() error { runs++; return failure })
	if runs != 1 || !errors.Is(err, failure) {
		t.Fatalf("ran %d times, error %v", runs, err)
	}
}

// A stop asked while a command ran is seen before the next command starts,
// also when Debian's job does not run and the wait would return at once.
func TestDebianJobSeesAStopBeforeTheNextCommand(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0)}
	ctx, stop := context.WithCancel(context.Background())
	job := debianAptJob{say: func(string) {}, running: func() (bool, error) { return false, nil }, sleep: clock.Sleep, now: clock.Now}
	if err := job.run(ctx, "", func() error { stop(); return nil }); err != nil {
		t.Fatalf("the command that ran when the stop came: %v", err)
	}
	err := job.run(ctx, "", func() error {
		t.Fatal("a command started after the stop")
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error %v, want the stop", err)
	}
}

// While Debian's job runs the owner is told once what vpn waits for, however
// long the wait. Then the command's own line replaces it and the command
// runs.
func TestDebianJobSaysWhyItWaitsThenWhatItDoes(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0)}
	busyUntil := clock.now.Add(time.Minute)
	var said []string
	job := debianAptJob{
		say:     func(text string) { said = append(said, text) },
		running: func() (bool, error) { return clock.now.Before(busyUntil), nil },
		sleep:   clock.Sleep, now: clock.Now,
	}
	ran := false
	if err := job.run(context.Background(), "what the command does", func() error { ran = true; return nil }); err != nil || !ran {
		t.Fatalf("ran %v, error %v", ran, err)
	}
	if len(said) != 2 || said[0] != debianAptJobWaiting || said[1] != "what the command does" {
		t.Fatalf("said %q, want the waiting line once, then the command's", said)
	}
}

// A stop ends the wait for Debian's job at once and no command runs.
func TestDebianJobWaitGivesWayToAStop(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	now := time.Unix(1_800_000_000, 0)
	sleeps := 0
	job := debianAptJob{
		say:     func(string) {},
		running: func() (bool, error) { return true, nil },
		sleep: func(ctx context.Context, _ time.Duration) error {
			if sleeps++; sleeps == 3 {
				stop()
			}
			return ctx.Err()
		},
		now: func() time.Time { return now },
	}
	err := job.run(ctx, "", func() error {
		t.Fatal("ran while Debian's job was running")
		return nil
	})
	if !errors.Is(err, context.Canceled) || sleeps != 3 {
		t.Fatalf("error %v after %d sleeps, want the stop at the third", err, sleeps)
	}
}

// A command that failed stays a failure when the stop comes in the wait
// before its second try. Its error comes back alone, so the install's runner
// cannot take it for a stop.
func TestDebianJobFailureIsNotHiddenByAStop(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	busy := false
	failure := errors.New(aptLockMessages[0])
	now := time.Unix(1_800_000_000, 0)
	job := debianAptJob{
		say:     func(string) {},
		running: func() (bool, error) { return busy, nil },
		sleep: func(ctx context.Context, _ time.Duration) error {
			stop()
			return ctx.Err()
		},
		now: func() time.Time { return now },
	}
	err := job.run(ctx, "", func() error { busy = true; return failure })
	if err != failure {
		t.Fatalf("error %v, want the command's own and nothing else", err)
	}
}

// The install puts a plain sentence on a command that fails. Only the
// command's failure gets it, with the command's own error under it. A lock
// error is still recognized by its own words, and a wait that fails or gives
// way to a stop comes back as it is.
func TestDebianJobGivesOnlyAFailedCommandTheSentence(t *testing.T) {
	const text = "The package tool could not do it."
	now := time.Unix(1_800_000_000, 0)
	idle := func() *debianAptJob {
		return &debianAptJob{say: func(string) {}, running: func() (bool, error) { return false, nil },
			sleep: func(context.Context, time.Duration) error { return nil }, now: func() time.Time { return now }}
	}

	broken := errors.New("apt-get install -y -qq fail2ban: exit status 100: E: Unable to locate package fail2ban")
	runs := 0
	err := idle().run(context.Background(), "", sentence(text, func() error { runs++; return broken }))
	if f, ok := errors.AsType[*failure.Failure](err); !ok || f.Text != text || f.Cause != broken || runs != 1 {
		t.Fatalf("a failed command gave %#v after %d runs, want the sentence over the command's error after one", err, runs)
	}

	runs = 0
	err = idle().run(context.Background(), "", sentence(text, func() error {
		if runs++; runs == 1 {
			return errors.New(aptLockMessages[0])
		}
		return nil
	}))
	if err != nil || runs != 2 {
		t.Fatalf("a lock error gave %v after %d runs, want a second try that passes", err, runs)
	}

	clock := &fakeClock{now: now}
	hour := debianAptJob{say: func(string) {}, running: func() (bool, error) { return true, nil }, sleep: clock.Sleep, now: clock.Now}
	if err := hour.run(context.Background(), "", sentence(text, func() error { return nil })); err != errDebianJobStillRunning {
		t.Fatalf("an hour of waiting gave %v, want its own failure and not the command's sentence", err)
	}

	ctx, stop := context.WithCancel(context.Background())
	stop()
	if err := idle().run(ctx, "", sentence(text, func() error { return broken })); err != context.Canceled {
		t.Fatalf("a stop gave %v, want the stop's own error", err)
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
