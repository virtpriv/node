package installer

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/virtpriv/node/internal/host"
	"github.com/virtpriv/node/internal/logger"
	"github.com/virtpriv/node/internal/loginpassword"
)

// stopFixture prepares a lifecycle with the first done steps recorded and
// returns a runner of RunInstall whose host steps are replaced by fn. With
// start false the frontend leaves without starting, as Exit does on the
// password screen. Otherwise it starts with password.
func stopFixture(t *testing.T, done int) (run func(start bool, password string, fn func(s *InstallSession, i int) error) error) {
	t.Helper()
	f := newLifecycleFixture(t)
	ledger := f.initialize(t)
	for _, key := range baseInstallStepKeys[:done] {
		if err := ledger.markDone(key, "test"); err != nil {
			t.Fatal(err)
		}
	}
	if err := ledger.save(f.fs.ledger); err != nil {
		t.Fatal(err)
	}
	installStartupFixture(t, f, func() error {
		return nil
	})
	return func(start bool, password string, fn func(s *InstallSession, i int) error) error {
		return RunInstall(InstallOptions{}, func(_ InstallView, s *InstallSession) error {
			s.runner.ledgerPath = f.fs.ledger
			s.complete = func() error { return errors.New("unexpected finalization") }
			for i := range s.runner.steps {
				s.runner.steps[i].Run = plain(func() error { return fn(s, i) })
			}
			if !start {
				return nil
			}
			// A password the policy refuses is passed on as typed, so the
			// session is the one that refuses it.
			typed, _ := loginpassword.New(password)
			if err := s.Start(InteractiveInput{Password: typed}); err != nil {
				return nil // the owner reads the refusal, then chooses Exit
			}
			for range s.Events() {
			}
			return nil
		})
	}
}

const goodPassword = "exact-test-password"

func keyIndex(t *testing.T, key string) int {
	t.Helper()
	for i, k := range baseInstallStepKeys {
		if k == key {
			return i
		}
	}
	t.Fatalf("no step %q", key)
	return -1
}

func noStep(t *testing.T) func(*InstallSession, int) error {
	return func(_ *InstallSession, i int) error {
		t.Errorf("step %d ran", i+1)
		return nil
	}
}

// A pause names the last step finished and the step the next run actually
// does: the first step not recorded, and the Tor check only before it first
// ran, since it runs again on every pass. With nothing recorded the install
// did not start.
func TestRootPauseNamesTheStepTheNextRunDoes(t *testing.T) {
	requireRootTestEnvironment(t)
	stopped := func(t *testing.T, err error) *StopError {
		t.Helper()
		var stop *StopError
		if !errors.As(err, &stop) {
			t.Fatalf("got %v, want a stop", err)
		}
		return stop
	}

	t.Run("exit before any step", func(t *testing.T) {
		run := stopFixture(t, 0)
		stop := stopped(t, run(false, "", noStep(t)))
		if !stop.NotStarted || stop.Paused {
			t.Fatalf("got %+v, want not started", stop)
		}
	})

	t.Run("exit on a rerun", func(t *testing.T) {
		run := stopFixture(t, 5)
		stop := stopped(t, run(false, "", noStep(t)))
		if stop.NotStarted || !stop.Paused || stop.DoneNum != 5 || stop.StepNum != 6 {
			t.Fatalf("got %+v, want paused after step 5, next step 6", stop)
		}
	})

	t.Run("stop after a step", func(t *testing.T) {
		at := keyIndex(t, "base.upgrade")
		run := stopFixture(t, at)
		stop := stopped(t, run(true, goodPassword, func(s *InstallSession, i int) error {
			if i == at {
				s.Stop()
			}
			return nil
		}))
		if !stop.Paused || stop.DoneNum != at+1 || stop.StepNum != at+2 {
			t.Fatalf("got %+v, want paused after step %d, next step %d", stop, at+1, at+2)
		}
	})

	t.Run("the Tor check is named before its first run", func(t *testing.T) {
		gate := keyIndex(t, "tor.gate")
		run := stopFixture(t, gate)
		stop := stopped(t, run(false, "", noStep(t)))
		if !stop.Paused || stop.DoneNum != gate || stop.StepNum != gate+1 {
			t.Fatalf("got %+v, want paused after step %d, the Tor check next", stop, gate)
		}
	})

	t.Run("the Tor check is not named", func(t *testing.T) {
		gate := keyIndex(t, "tor.gate")
		run := stopFixture(t, gate+1)
		stop := stopped(t, run(false, "", noStep(t)))
		if !stop.Paused || stop.DoneNum != gate+1 || stop.StepNum != gate+2 {
			t.Fatalf("got %+v, want paused after the Tor check, step %d, next step %d", stop, gate+1, gate+2)
		}
	})
}

// After a refused start the owner can only leave. The shell then prints why,
// and the log keeps it, because the screen points there.
func TestRootExitAfterARefusedStartKeepsTheReason(t *testing.T) {
	requireRootTestEnvironment(t)
	var log bytes.Buffer
	prev := logger.SetOutput(&log)
	t.Cleanup(func() { logger.SetOutput(prev) })
	run := stopFixture(t, 0)
	err := run(true, "short", noStep(t))
	var stop *StopError
	if !errors.As(err, &stop) || !stop.NotStarted || stop.StartErr == nil {
		t.Fatalf("got %#v, want not started with the refusal", err)
	}
	why := stop.StartErr.Error()
	if lines := strings.Join(stop.Lines(), "\n"); !strings.Contains(lines, "The install did not start.") ||
		!strings.Contains(lines, why[1:]) {
		t.Errorf("shell lines %q leave out the reason %q", stop.Lines(), why)
	}
	if !strings.Contains(log.String(), why) {
		t.Errorf("log %q leaves out the reason", log.String())
	}
}

// The SSH step writes the setting before it restarts sshd. A failure after
// the write may leave root's SSH login already off.
func TestRootSSHChangeNotConfirmedWarnsAboutRootLogin(t *testing.T) {
	requireRootTestEnvironment(t)
	off := keyIndex(t, rootLoginOffStep)
	for _, tc := range []struct {
		name string
		err  error
		may  bool
	}{
		{"restart failed after the write", fmt.Errorf("restart sshd: %w", host.ErrSSHSettingNotConfirmed), true},
		{"setting refused and restored", errors.New("sshd did not confirm vpn's SSH settings, so the earlier file is back and sshd was not restarted"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := stopFixture(t, off)
			err := run(true, goodPassword, func(_ *InstallSession, i int) error {
				if i == off {
					return tc.err
				}
				return nil
			})
			var stop *StopError
			if !errors.As(err, &stop) || stop.RootLoginMayBeOff != tc.may || stop.RootLoginOff {
				t.Fatalf("got %#v, want may be off %v", err, tc.may)
			}
		})
	}
}

// After a failed step the shell names the command that continues the install
// the way this run was started. It names none while vpn may not be installed,
// and no sudo while root runs on a machine that may not have it yet.
func TestRootAFailedStepNamesTheCommandThatContinues(t *testing.T) {
	requireRootTestEnvironment(t)
	for _, tc := range []struct {
		name    string
		done    int
		sudoUID string
		want    string
	}{
		{"step 1 failed, so vpn may not be installed", 0, "1000", ""},
		{"step 2 failed for root, so sudo may not be installed", 1, "", "vpn install"},
		{"step 2 failed in a run that came through sudo", 1, "1000", "sudo vpn install"},
		{"a later step failed, after step 2 installed sudo", 2, "", "sudo vpn install"},
		{"a step failed after root's login went off", keyIndex(t, rootLoginOffStep) + 1, "", "sudo vpn install"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SUDO_UID", tc.sudoUID)
			run := stopFixture(t, tc.done)
			err := run(true, goodPassword, func(_ *InstallSession, i int) error {
				if i == tc.done {
					return errors.New("boom")
				}
				return nil
			})
			stop, ok := errors.AsType[*StopError](err)
			if !ok || stop.StepNum != tc.done+1 || stop.Again != tc.want {
				t.Fatalf("got %#v, want step %d failed and the command %q", err, tc.done+1, tc.want)
			}
			// The command stands alone on the last line, under the notice
			// about root's login. With no command the lines end in words.
			lines := stop.Lines()
			last := lines[len(lines)-1]
			if (tc.want != "" && last != tc.want) || (tc.want == "" && last == "") {
				t.Fatalf("the shell ends with %q in %q", last, lines)
			}
		})
	}
}
