package installer

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/virtpriv/node/internal/loginpassword"
)

func interactiveFixture(t *testing.T, steps []InstallStep, complete func() error) (*InstallSession, InteractiveInput) {
	t.Helper()
	ledger := newLedger()
	path := filepath.Join(t.TempDir(), "ledger.json")
	if err := ledger.save(path); err != nil {
		t.Fatal(err)
	}
	runner, err := newStepRunner(steps, "test", ledger, path)
	if err != nil {
		t.Fatal(err)
	}
	dec := &InstallDecisions{}
	s := newInstallSession(runner, dec, complete)
	pw, err := loginpassword.New("exact-test-password")
	if err != nil {
		t.Fatal(err)
	}
	return s, InteractiveInput{Password: pw}
}

func TestInteractiveExitJoinsAdmittedStep(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		laterCalls := 0
		s, input := interactiveFixture(t, []InstallStep{
			{Key: "binary.install", Name: "Binary", Run: plain(func() error { close(entered); <-release; return nil })},
			{Key: "apt.base", Name: "Packages", Run: plain(func() error { laterCalls++; return nil })},
		}, func() error { t.Error("interrupted install finalized"); return nil })
		uiErr := errors.New("terminal lost")
		returned := make(chan struct{})
		var result RunResult
		var err error
		go func() {
			result, err = runInstallInteractive(s, InstallView{}, func(_ InstallView, s *InstallSession) error {
				if err := s.Start(input); err != nil {
					return err
				}
				<-entered
				return uiErr
			})
			close(returned)
		}()
		<-entered
		synctest.Wait()
		select {
		case <-returned:
			t.Error("frontend exit returned before step settled")
		default:
		}
		ledger, readErr := readLedger(s.runner.ledgerPath)
		if readErr != nil {
			t.Error(readErr)
		} else if ledger.done("binary.install") {
			t.Error("unfinished step recorded")
		}
		close(release)
		<-returned
		if result.Outcome != RunInterrupted || result.StepNum != 2 || !errors.Is(err, uiErr) {
			t.Fatalf("exit result=%+v err=%v", result, err)
		}
		if laterCalls != 0 || !mustReadLedger(t, s.runner.ledgerPath).done("binary.install") {
			t.Fatal("stop lost the admitted step or ran a later step")
		}
		if err := s.Start(input); err == nil {
			t.Fatal("stopped session restarted")
		}
	})
}

func TestInteractiveFinalizationDoesNotNeedObserver(t *testing.T) {
	for _, failCompletion := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "publication failure"}[failCompletion], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				var completionErr error
				if failCompletion {
					completionErr = errors.New("completion publication failed")
				}
				var s *InstallSession
				var input InteractiveInput
				s, input = interactiveFixture(t, []InstallStep{{Key: "binary.install", Name: "Binary", Run: plain(func() error { return nil })}}, func() error {
					calls++
					ledger, err := readLedger(s.runner.ledgerPath)
					if err != nil {
						return err
					}
					if !ledger.done("binary.install") {
						return errors.New("finalization preceded durable step")
					}
					return completionErr
				})
				if err := s.Start(input); err != nil {
					t.Fatal(err)
				}
				// No progress read may be needed to finish or release the worker.
				<-s.done
				result, err := s.close()
				if result.Outcome != RunComplete || calls != 1 || !errors.Is(err, completionErr) {
					t.Fatalf("result=%+v calls=%d err=%v", result, calls, err)
				}
				var events []InstallEvent
				for e := range s.Events() {
					events = append(events, e)
				}
				if len(events) != 3 || events[0].Status != StepRunning || events[1].Status != StepDone || !events[2].Final {
					t.Fatalf("expected running, done, final progress, got %+v", events)
				}
				if events[2].Result.Outcome != RunComplete || !errors.Is(events[2].CompletionErr, completionErr) {
					t.Fatal("final event lost execution or publication result")
				}
				if s.dec.Password.Text() != "" {
					t.Fatal("completed session retained password")
				}
			})
		})
	}
}

func TestInteractiveFailureAndPublicationStopExecution(t *testing.T) {
	for _, publicationFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "step failure", true: "ledger failure"}[publicationFailure], func(t *testing.T) {
			parent := filepath.Join(t.TempDir(), "not-a-directory")
			failure := errors.New("step failed")
			later := 0
			var s *InstallSession
			s, input := interactiveFixture(t, []InstallStep{
				{Key: "binary.install", Name: "Binary", Run: plain(func() error {
					if !publicationFailure {
						return failure
					}
					// A file cannot act as the ledger's parent directory.
					if err := os.WriteFile(parent, []byte("x"), 0600); err != nil {
						return err
					}
					s.runner.ledgerPath = filepath.Join(parent, "ledger.json")
					return nil
				})},
				{Key: "apt.base", Name: "Packages", Run: plain(func() error { later++; return nil })},
			}, func() error { t.Error("failed run finalized"); return nil })
			if err := s.Start(input); err != nil {
				t.Fatal(err)
			}
			<-s.done
			result, err := s.close()
			if result.Outcome != RunFailed || result.StepNum != 1 || result.StepName != "Binary" || result.Err == nil || err != nil || later != 0 {
				t.Fatalf("result=%+v err=%v later=%d", result, err, later)
			}
			if !publicationFailure && !errors.Is(result.Err, failure) {
				t.Fatal("step cause lost")
			}
			if publicationFailure && !strings.Contains(result.Err.Error(), "ledger publication failed") {
				t.Fatal("publication uncertainty lost")
			}
		})
	}
}

// A start with a password the policy refuses admits no step, and a retry
// with a good one can still start the install, once.
func TestInteractiveStartRefusesABadPasswordAndCanBeRetried(t *testing.T) {
	calls := 0
	s, input := interactiveFixture(t, []InstallStep{
		{Key: "binary.install", Name: "Binary", Run: plain(func() error { calls++; return nil })},
	}, func() error { return nil })
	if !s.needIdentity {
		t.Fatal("a fresh install does not ask for the password")
	}
	if err := s.Start(InteractiveInput{}); err == nil {
		t.Fatal("missing password accepted")
	}
	if calls != 0 || s.started || s.refusal() == nil {
		t.Fatal("invalid input crossed mutation boundary")
	}
	if err := s.Start(input); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(input); err == nil {
		t.Fatal("duplicate start accepted")
	}
	<-s.done
	if calls != 1 || s.refusal() != nil {
		t.Fatalf("calls=%d refusal=%v", calls, s.refusal())
	}
}

func TestInteractiveStopBeforeStartAndLastStepCompletion(t *testing.T) {
	t.Run("before start", func(t *testing.T) {
		s, input := interactiveFixture(t, []InstallStep{{Key: "binary.install", Name: "Binary", Run: plain(func() error { t.Error("stopped step ran"); return nil })}}, func() error { t.Error("stopped run finalized"); return nil })
		result, err := runInstallInteractive(s, InstallView{}, func(InstallView, *InstallSession) error { return nil })
		if result.Outcome != RunInterrupted || err != nil {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		if err := s.Start(input); err == nil {
			t.Fatal("delayed start accepted after frontend exit")
		}
		s.Stop()
		final, ok := <-s.Events()
		if !ok || !final.Final || final.Result.Outcome != RunInterrupted {
			t.Fatal("early exit left an observer without a terminal result")
		}
		if _, ok := <-s.Events(); ok {
			t.Fatal("early-exit result stream stayed open")
		}
	})
	t.Run("last admitted step", func(t *testing.T) {
		finalized := 0
		var s *InstallSession
		s, input := interactiveFixture(t, []InstallStep{{Key: "binary.install", Name: "Binary", Run: plain(func() error { s.Stop(); return nil })}}, func() error { finalized++; return nil })
		if err := s.Start(input); err != nil {
			t.Fatal(err)
		}
		<-s.done
		result, err := s.close()
		if result.Outcome != RunComplete || finalized != 1 || err != nil {
			t.Fatalf("last-step result=%+v finalized=%d err=%v", result, finalized, err)
		}
	})
}

func TestInteractiveResumeExecutesGateAndPublishesSkips(t *testing.T) {
	calls := make(map[string]int)
	steps := testSteps(calls)
	ledger := newLedger()
	// Resume after identity. Mutations stay saved, but this pass must recheck
	// its gate before reaching the remaining step.
	for _, step := range steps[:6] {
		if err := ledger.markDone(step.Key, "previous"); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), "ledger.json")
	if err := ledger.save(path); err != nil {
		t.Fatal(err)
	}
	runner, err := newStepRunner(steps, "candidate", ledger, path)
	if err != nil {
		t.Fatal(err)
	}
	finalized := 0
	s := newInstallSession(runner, &InstallDecisions{},
		func() error { finalized++; return nil })
	if s.needIdentity {
		t.Fatal("resume asked for the password again")
	}
	if err := s.Start(InteractiveInput{}); err != nil {
		t.Fatal(err)
	}
	<-s.done
	result, err := s.close()
	if err != nil || result.Outcome != RunComplete || finalized != 1 {
		t.Fatalf("result=%+v finalized=%d err=%v", result, finalized, err)
	}
	skips := 0
	for e := range s.Events() {
		if !e.Final && e.Status == StepSkipped {
			skips++
		}
	}
	if skips != 5 || len(calls) != 2 || calls["firewall"] != 1 || calls["tor.configure"] != 1 {
		t.Fatalf("resume skips=%d calls=%v", skips, calls)
	}
	if !mustReadLedger(t, path).done("tor.configure") {
		t.Fatal("resume did not publish remaining progress")
	}
}

func TestRootRunInstallExitRetainsRunLock(t *testing.T) {
	requireRootTestEnvironment(t)
	f := newLifecycleFixture(t)
	f.initialize(t)
	installStartupFixture(t, f, func() error {
		return nil
	})
	deps := newInstallStartupDependencies()
	password, err := loginpassword.New("exact-test-password")
	if err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		returned := make(chan error, 1)
		uiErr := errors.New("terminal lost")
		go func() {
			returned <- RunInstall(InstallOptions{}, func(_ InstallView, s *InstallSession) error {
				// Exercise the production coordinator and lock, replacing only host
				// mutations and redirecting progress to the temporary lifecycle.
				s.runner.ledgerPath = f.fs.ledger
				s.complete = func() error {
					t.Error("interrupted install finalized")
					return errors.New("unexpected finalization")
				}
				for i := range s.runner.steps {
					s.runner.steps[i].Run = plain(func() error {
						t.Error("stop admitted a later step")
						return errors.New("unexpected host step")
					})
				}
				s.runner.steps[0].Run = plain(func() error { close(entered); <-release; return nil })
				if err := s.Start(InteractiveInput{Password: password}); err != nil {
					return err
				}
				<-entered
				return uiErr
			})
		}()
		select {
		case <-entered:
		case err := <-returned:
			t.Fatalf("installation never entered the held step: %v", err)
		}
		synctest.Wait()
		other, err := acquireRunLock(deps.runtimeDir, deps.installLock)
		if err == nil {
			other.Close()
			t.Error("RunInstall released its lock before admitted work settled")
		} else if !strings.Contains(err.Error(), "already running") {
			t.Error(err)
		}
		close(release)
		if err := <-returned; !errors.Is(err, uiErr) {
			t.Fatalf("frontend error lost: %v", err)
		}
		if !mustReadLedger(t, f.fs.ledger).done("binary.install") {
			t.Fatal("RunInstall returned before publishing admitted work")
		}
		other, err = acquireRunLock(deps.runtimeDir, deps.installLock)
		if err != nil {
			t.Fatal(err)
		}
		other.Close()
	})
}

// An install that ends early tells main what happened by its kind: paused
// between steps, or a step failed. Once the install record shows root's SSH
// login turned off, every error says so, and the install screen learns it from
// the record before the run and again in the final event.
func TestRootRunInstallReportsPauseFailureAndRootLoginOff(t *testing.T) {
	requireRootTestEnvironment(t)
	rootOffAt := -1
	for i, key := range baseInstallStepKeys {
		if key == rootLoginOffStep {
			rootOffAt = i
		}
	}
	if rootOffAt < 0 {
		t.Fatal("no step turns root's SSH login off")
	}
	setup := func(t *testing.T, done int, preflightErr error) string {
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
			return preflightErr
		})
		return f.fs.ledger
	}
	// run drives the real session, with the host steps replaced by fn.
	run := func(t *testing.T, ledgerPath string, fn func(s *InstallSession, i int) error) (InstallView, InstallEvent, error) {
		t.Helper()
		var view InstallView
		var final InstallEvent
		err := RunInstall(InstallOptions{}, func(v InstallView, s *InstallSession) error {
			view = v
			s.runner.ledgerPath = ledgerPath
			s.complete = func() error { return errors.New("unexpected finalization") }
			for i := range s.runner.steps {
				s.runner.steps[i].Run = plain(func() error { return fn(s, i) })
			}
			password, err := loginpassword.New("exact-test-password")
			if err != nil {
				return err
			}
			if err := s.Start(InteractiveInput{Password: password}); err != nil {
				return err
			}
			for e := range s.Events() {
				if e.Final {
					final = e
				}
			}
			return nil
		})
		return view, final, err
	}

	t.Run("pause before root login is off", func(t *testing.T) {
		path := setup(t, 0, nil)
		view, final, err := run(t, path, func(s *InstallSession, i int) error {
			if i != 0 {
				t.Error("stop admitted a later step")
			}
			s.Stop()
			return nil
		})
		var stop *StopError
		if !errors.As(err, &stop) || !stop.Paused || stop.StepNum != 2 || stop.RootLoginOff {
			t.Fatalf("got %#v, want a pause at step 2 with root login on", err)
		}
		if view.RootLoginOff || final.RootLoginOff {
			t.Fatal("the screen was told root login is off")
		}
	})

	t.Run("failure after root login is off", func(t *testing.T) {
		path := setup(t, rootOffAt+1, nil)
		stepErr := errors.New("step broke")
		view, final, err := run(t, path, func(_ *InstallSession, i int) error {
			if i == rootOffAt+1 {
				return stepErr
			}
			return nil
		})
		var stop *StopError
		if !errors.As(err, &stop) || stop.Paused || stop.StepNum != rootOffAt+2 || !stop.RootLoginOff || !errors.Is(err, stepErr) {
			t.Fatalf("got %#v, want the failed step with root login off", err)
		}
		if !view.RootLoginOff || !final.RootLoginOff {
			t.Fatal("the screen was not told root login is off")
		}
	})

	t.Run("check before any step, root login off or not", func(t *testing.T) {
		for _, done := range []int{rootOffAt, rootOffAt + 1} {
			checkErr := errors.New("machine check failed")
			path := setup(t, done, checkErr)
			_, _, err := run(t, path, func(*InstallSession, int) error {
				t.Error("a step ran after the machine check failed")
				return nil
			})
			var stop *StopError
			rootOff := errors.As(err, &stop) && stop.RootLoginOff
			if !errors.Is(err, checkErr) || rootOff != (done > rootOffAt) {
				t.Fatalf("with %d steps done got %#v", done, err)
			}
		}
	})
}
