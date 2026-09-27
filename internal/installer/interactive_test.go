package installer

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/virtualprivatenode/vpn/internal/host"
	"github.com/virtualprivatenode/vpn/internal/loginpassword"
)

func interactiveFixture(t *testing.T, steps []InstallStep, complete func() error) (*InstallSession, InteractiveInput) {
	t.Helper()
	ledger := testLedger()
	path := filepath.Join(t.TempDir(), "ledger.json")
	if err := ledger.save(path); err != nil {
		t.Fatal(err)
	}
	runner, err := newStepRunner(steps, "test", ledger, path)
	if err != nil {
		t.Fatal(err)
	}
	dec := &InstallDecisions{}
	s := newInstallSession(runner, dec, func(int) error { return errors.New("unexpected cache write") }, complete)
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
			{Key: "binary.install", Name: "Binary", Fn: func() error { close(entered); <-release; return nil }},
			{Key: "apt.base", Name: "Packages", Fn: func() error { laterCalls++; return nil }},
		}, func() error { t.Error("interrupted install finalized"); return nil })
		uiErr := errors.New("terminal lost")
		returned := make(chan struct{})
		var result RunResult
		var err error
		var open bool
		go func() {
			result, open, err = runInstallInteractive(s, InstallView{}, func(_ InstallView, s *InstallSession) (bool, error) {
				if err := s.Start(input); err != nil {
					return false, err
				}
				<-entered
				return true, uiErr
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
		if result.Outcome != RunInterrupted || result.StepNum != 2 || open || !errors.Is(err, uiErr) {
			t.Fatalf("exit result=%+v open=%v err=%v", result, open, err)
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
				s, input = interactiveFixture(t, []InstallStep{{Key: "binary.install", Name: "Binary", Fn: func() error { return nil }}}, func() error {
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
					t.Fatalf("expected running, done, final progress; got %+v", events)
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
				{Key: "binary.install", Name: "Binary", Fn: func() error {
					if !publicationFailure {
						return failure
					}
					// A file cannot act as the ledger's parent directory.
					if err := os.WriteFile(parent, []byte("x"), 0600); err != nil {
						return err
					}
					s.runner.ledgerPath = filepath.Join(parent, "ledger.json")
					return nil
				}},
				{Key: "apt.base", Name: "Packages", Fn: func() error { later++; return nil }},
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

func TestInteractiveInputValidationAndRetry(t *testing.T) {
	calls, writes := 0, 0
	s, input := interactiveFixture(t, []InstallStep{{Key: "binary.install", Name: "Binary", Fn: func() error { calls++; return nil }}}, func() error { return nil })
	s.needHardware = true
	writeErr := errors.New("cache decision not saved")
	s.persistDBCache = func(int) error { writes++; return writeErr }
	input.DBCacheMB = 512
	for _, invalid := range []InteractiveInput{
		{DBCacheMB: 512},
		{Password: input.Password, DBCacheMB: 999},
	} {
		if err := s.Start(invalid); err == nil {
			t.Fatal("invalid decisions accepted")
		}
	}
	if writes != 0 || calls != 0 || s.started {
		t.Fatal("invalid input crossed mutation boundary")
	}
	if err := s.Start(input); !errors.Is(err, writeErr) {
		t.Fatalf("cache error=%v", err)
	}
	if s.started || calls != 0 || s.dec.Password.Text() != "" {
		t.Fatal("failed decision persistence admitted execution")
	}
	s.persistDBCache = func(int) error { writes++; return nil }
	if err := s.Start(input); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(input); err == nil {
		t.Fatal("duplicate start accepted")
	}
	<-s.done
	if calls != 1 || writes != 2 {
		t.Fatalf("calls=%d writes=%d", calls, writes)
	}
}

func TestInteractiveStopBeforeStartAndLastStepCompletion(t *testing.T) {
	t.Run("before start", func(t *testing.T) {
		s, input := interactiveFixture(t, []InstallStep{{Key: "binary.install", Name: "Binary", Fn: func() error { t.Error("stopped step ran"); return nil }}}, func() error { t.Error("stopped run finalized"); return nil })
		result, open, err := runInstallInteractive(s, InstallView{}, func(InstallView, *InstallSession) (bool, error) { return true, nil })
		if result.Outcome != RunInterrupted || open || err != nil {
			t.Fatalf("result=%+v open=%v err=%v", result, open, err)
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
		s, input := interactiveFixture(t, []InstallStep{{Key: "binary.install", Name: "Binary", Fn: func() error { s.Stop(); return nil }}}, func() error { finalized++; return nil })
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
	ledger := testLedger()
	// Resume after identity: mutations and the completed pipeline stay saved,
	// but this pass must recheck its gate before reaching the remaining step.
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
		func(int) error { t.Error("resume overwrote cache choice"); return nil },
		func() error { finalized++; return nil })
	if s.needIdentity || s.needHardware {
		t.Fatal("resume requested previously recorded choices")
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
	if skips != 5 || len(calls) != 2 || calls["firewall"] != 1 || calls["service-identities.v1"] != 1 {
		t.Fatalf("resume skips=%d calls=%v", skips, calls)
	}
	if !mustReadLedger(t, path).done("service-identities.v1") {
		t.Fatal("resume did not publish remaining progress")
	}
}

func TestRootRunInstallExitRetainsRunLock(t *testing.T) {
	requireRootTestEnvironment(t)
	f := newLifecycleFixture(t)
	ledger := f.initialize(t)
	if err := ledger.setDbCache(512); err != nil {
		t.Fatal(err)
	}
	if err := ledger.save(f.fs.ledger); err != nil {
		t.Fatal(err)
	}
	installStartupFixture(t, f, func() (host.SSHObservation, error) {
		return host.SSHObservation{PasswordAuth: true}, nil
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
			returned <- RunInstall(InstallOptions{}, func(_ InstallView, s *InstallSession) (bool, error) {
				// Exercise the production coordinator and lock, replacing only host
				// mutations and redirecting progress to the temporary lifecycle.
				s.runner.ledgerPath = f.fs.ledger
				s.persistDBCache = func(int) error { return errors.New("unexpected cache write") }
				s.complete = func() error {
					t.Error("interrupted install finalized")
					return errors.New("unexpected finalization")
				}
				for i := range s.runner.steps {
					s.runner.steps[i].Fn = func() error {
						t.Error("stop admitted a later step")
						return errors.New("unexpected host step")
					}
				}
				s.runner.steps[0].Fn = func() error { close(entered); <-release; return nil }
				if err := s.Start(InteractiveInput{Password: password}); err != nil {
					return false, err
				}
				<-entered
				return false, uiErr
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
