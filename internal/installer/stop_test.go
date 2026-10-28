package installer

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/virtpriv/node/internal/host"
	"github.com/virtpriv/node/internal/logger"
	"github.com/virtpriv/node/internal/loginpassword"
)

// stopFixture prepares a lifecycle with the first done steps recorded and
// returns a runner of RunInstall whose host steps are replaced by fn. With
// start false the frontend leaves without starting, as Exit does on the
// password screen.
func stopFixture(t *testing.T, done int) (run func(start bool, fn func(s *InstallSession, i int) error) error, workBase string) {
	return stopFixtureWith(t, done, nil)
}

// stopFixtureWith lets setup change the session before the frontend acts.
func stopFixtureWith(t *testing.T, done int, setup func(*InstallSession)) (run func(start bool, fn func(s *InstallSession, i int) error) error, workBase string) {
	t.Helper()
	f := newLifecycleFixture(t)
	ledger := f.initialize(t)
	if err := ledger.setDbCache(512); err != nil {
		t.Fatal(err)
	}
	for _, key := range baseInstallStepKeys[:done] {
		if err := ledger.markDone(key, "test"); err != nil {
			t.Fatal(err)
		}
	}
	if err := ledger.save(f.fs.ledger); err != nil {
		t.Fatal(err)
	}
	installStartupFixture(t, f, func() (host.SSHObservation, error) {
		return host.SSHObservation{PasswordAuth: true}, nil
	})
	workBase = t.TempDir()
	saved := newInstallStartupDependencies
	newInstallStartupDependencies = func() installStartupDependencies {
		deps := saved()
		deps.workBase = workBase
		return deps
	}
	t.Cleanup(func() { newInstallStartupDependencies = saved })
	run = func(start bool, fn func(s *InstallSession, i int) error) error {
		return RunInstall(InstallOptions{}, func(_ InstallView, s *InstallSession) error {
			s.runner.ledgerPath = f.fs.ledger
			s.complete = func() error { return errors.New("unexpected finalization") }
			if setup != nil {
				setup(s)
			}
			for i := range s.runner.steps {
				s.runner.steps[i].Fn = func() error { return fn(s, i) }
			}
			if !start {
				return nil
			}
			password, err := loginpassword.New("exact-test-password")
			if err != nil {
				return err
			}
			if err := s.Start(InteractiveInput{Password: password}); err != nil {
				return nil // the owner reads the refusal, then chooses Exit
			}
			for range s.Events() {
			}
			return nil
		})
	}
	return run, workBase
}

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
// does: the first step not recorded, the first member of a download group
// whose last member is not recorded, and the Tor check only before it first
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
		run, _ := stopFixture(t, 0)
		stop := stopped(t, run(false, noStep(t)))
		if !stop.NotStarted || stop.Paused {
			t.Fatalf("got %+v, want not started", stop)
		}
	})

	t.Run("exit on a rerun", func(t *testing.T) {
		run, _ := stopFixture(t, 5)
		stop := stopped(t, run(false, noStep(t)))
		if stop.NotStarted || !stop.Paused || stop.DoneNum != 5 || stop.StepNum != 6 {
			t.Fatalf("got %+v, want paused after step 5, next step 6", stop)
		}
	})

	t.Run("stop inside a download group", func(t *testing.T) {
		first := keyIndex(t, "btc.download")
		run, _ := stopFixture(t, first)
		stop := stopped(t, run(true, func(s *InstallSession, i int) error {
			if i == first {
				s.Stop()
			}
			return nil
		}))
		if !stop.Paused || stop.DoneNum != first+1 || stop.StepNum != first+1 {
			t.Fatalf("got %+v, want paused after the download, step %d, and the download next", stop, first+1)
		}
	})

	t.Run("the Tor check is named before its first run", func(t *testing.T) {
		gate := keyIndex(t, "tor.gate")
		run, _ := stopFixture(t, gate)
		stop := stopped(t, run(false, noStep(t)))
		if !stop.Paused || stop.DoneNum != gate || stop.StepNum != gate+1 {
			t.Fatalf("got %+v, want paused after step %d, the Tor check next", stop, gate)
		}
	})

	t.Run("the Tor check is not named", func(t *testing.T) {
		gate := keyIndex(t, "tor.gate")
		run, _ := stopFixture(t, gate+1)
		stop := stopped(t, run(false, noStep(t)))
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
	const why = "no space left on device"
	refused := errors.New("write install-state.json: " + why)
	run, _ := stopFixtureWith(t, 0, func(s *InstallSession) {
		s.needCache = true
		s.persistDBCache = func(int) error { return refused }
	})
	err := run(true, noStep(t))
	var stop *StopError
	if !errors.As(err, &stop) || !stop.NotStarted || !errors.Is(err, refused) {
		t.Fatalf("got %#v, want not started with the refusal", err)
	}
	if !strings.Contains(strings.Join(stop.Lines(), "\n"), why) {
		t.Errorf("shell lines %q leave out the reason", stop.Lines())
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
		{"setting refused and restored", errors.New("sshd rejected the config, previous drop-in restored"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run, _ := stopFixture(t, off)
			err := run(true, func(_ *InstallSession, i int) error {
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

// Download folders live in /tmp, which Debian 13 keeps in memory. A run
// removes its own whatever way it ends, and removes those a killed run left,
// but nothing that is not vpn's own.
func TestRootInstallRemovesDownloadFolders(t *testing.T) {
	requireRootTestEnvironment(t)
	run, base := stopFixture(t, 0)
	mk := func(name string, mode os.FileMode) string {
		p := filepath.Join(base, name)
		if err := os.Mkdir(p, mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "archive"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	leftover := mk("vpn-install-111", 0o700)
	open := mk("vpn-install-222", 0o755)
	other := mk("other-333", 0o700)
	foreign := mk("vpn-install-444", 0o700)
	if err := os.Chown(foreign, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	target := mk("target", 0o700)
	link := filepath.Join(base, "vpn-install-555")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	ownDuringRun := 0
	err := run(true, func(s *InstallSession, i int) error {
		if i == 0 {
			matches, _ := filepath.Glob(filepath.Join(base, "vpn-install-*"))
			for _, m := range matches {
				if fi, err := os.Lstat(m); err == nil && fi.IsDir() && fi.Mode().Perm() == 0o700 && m != leftover && m != foreign {
					ownDuringRun++
				}
			}
			s.Stop()
		}
		return nil
	})
	var stop *StopError
	if !errors.As(err, &stop) || !stop.Paused {
		t.Fatalf("got %v, want a pause", err)
	}
	if ownDuringRun != 1 {
		t.Fatalf("the run had %d download folders of its own, want 1", ownDuringRun)
	}
	if _, err := os.Stat(leftover); !errors.Is(err, os.ErrNotExist) {
		t.Error("a killed run's folder was kept")
	}
	for _, kept := range []string{open, other, foreign, filepath.Join(target, "archive")} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("%s was removed: %v", kept, err)
		}
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Error("a link was followed or removed")
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	want := []string{"other-333", "target", "vpn-install-222", "vpn-install-444", "vpn-install-555"}
	if fmt.Sprint(left) != fmt.Sprint(want) {
		t.Errorf("left %v, want %v (this run's own folder removed too)", left, want)
	}
}
