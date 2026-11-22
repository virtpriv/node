package installer

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/virtpriv/node/internal/logger"
)

// A pause asked while a step waits ends the wait. The step is neither failed
// nor recorded, the screen is told it has not begun, its line is gone, and
// the next run does it.
func TestAPauseEndsAWaitingStepWithoutRecordingIt(t *testing.T) {
	waiting := make(chan struct{})
	later := 0
	list := func(wait func(context.Context, func(string)) error) []InstallStep {
		return []InstallStep{
			{Key: "binary.install", Name: "Binary", Run: plain(func() error { return nil })},
			{Key: "apt.base", Name: "Waits", Run: wait},
			{Key: "firewall", Name: "Later", Run: plain(func() error { later++; return nil })},
		}
	}
	s, input := interactiveFixture(t, list(func(ctx context.Context, say func(string)) error {
		say("how far the wait is")
		close(waiting)
		<-ctx.Done()
		return ctx.Err()
	}), func() error { t.Error("a paused install was finished"); return nil })
	if err := s.Start(input); err != nil {
		t.Fatal(err)
	}
	<-waiting
	if step, text := s.Doing(); step != 1 || text != "how far the wait is" {
		t.Fatalf("the screen reads step %d, %q", step, text)
	}
	result, err := s.close()
	if err != nil || result.Outcome != RunInterrupted || result.StepNum != 2 || result.Err != nil {
		t.Fatalf("got %+v, %v, want a pause at step 2 with no failure", result, err)
	}
	if later != 0 {
		t.Fatal("a step ran after the pause")
	}
	ledger := mustReadLedger(t, s.runner.ledgerPath)
	if !ledger.done("binary.install") || ledger.done("apt.base") {
		t.Fatal("the record does not hold step 1 alone")
	}
	var last InstallEvent
	for e := range s.Events() {
		if !e.Final && e.Index == 1 {
			last = e
		}
	}
	if last.Status != StepPending || last.Err != nil {
		t.Fatalf("the screen was last told %+v about the stopped step", last)
	}
	if _, text := s.Doing(); text != "" {
		t.Fatalf("the stopped step's line %q stayed", text)
	}

	// The next run does the stopped step.
	again := 0
	runner, err := newStepRunner(list(plain(func() error { again++; return nil })), "test", ledger, s.runner.ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	if res := runner.run(context.Background()); res.Outcome != RunComplete || again != 1 || later != 1 {
		t.Fatalf("the next run: %+v, the stopped step ran %d times, the later one %d", res, again, later)
	}
}

// A rerun that skips steps reports every step in its place. Its log lines,
// a step's own line among them, and the line the screen reads keep the
// numbers of the whole list.
func TestARerunKeepsTheWholeListsNumbersAndOrder(t *testing.T) {
	var log bytes.Buffer
	prev := logger.SetOutput(&log)
	t.Cleanup(func() { logger.SetOutput(prev) })

	list := testSteps(map[string]int{})
	ledger := newLedger()
	for _, step := range list[:6] {
		if err := ledger.markDone(step.Key, "previous"); err != nil {
			t.Fatal(err)
		}
	}
	runner, err := newStepRunner(list, "test", ledger, filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	// The check, step 3 of 7, runs on every pass. Here it says a line.
	read := -1
	runner.steps[2].Run = func(_ context.Context, say func(string)) error {
		say("the check's own line")
		read, _ = runner.Doing()
		return nil
	}
	var shown []string
	runner.show = func(i int, status StepStatus, _ error) {
		shown = append(shown, string(rune('1'+i))+[...]string{"pending", "running", "done", "failed", "skipped"}[status])
	}
	if res := runner.run(context.Background()); res.Outcome != RunComplete {
		t.Fatalf("got %+v", res)
	}
	want := "1skipped 2skipped 3running 3done 4skipped 5skipped 6skipped 7running 7done"
	if got := strings.Join(shown, " "); got != want {
		t.Fatalf("the front end saw %q, want %q", got, want)
	}
	if read != 2 {
		t.Fatalf("the screen reads the line under step %d, want step 3", read+1)
	}
	// The log's step numbers in order. A skipped step has one line, a step
	// that ran has a start and an end, and one more for a line it said.
	var got []string
	for _, n := range regexp.MustCompile(`step (\d+/\d+)`).FindAllStringSubmatch(log.String(), -1) {
		got = append(got, n[1])
	}
	wantLog := "1/7 2/7 3/7 3/7 3/7 4/7 5/7 6/7 7/7 7/7"
	if strings.Join(got, " ") != wantLog {
		t.Fatalf("the log's step numbers are %q, want %q", got, wantLog)
	}
	for l := range strings.SplitSeq(log.String(), "\n") {
		if strings.Contains(l, "the check's own line") && !strings.Contains(l, "3/7") {
			t.Fatalf("the step's own line is logged under another number: %q", l)
		}
	}
}

// What a step says is read, never sent, so a step can say as much as it
// likes while nobody reads the session's events.
func TestAStepThatSaysALotNeverWaitsForAReader(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, input := interactiveFixture(t, []InstallStep{
			{Key: "binary.install", Name: "Talks", Run: func(_ context.Context, say func(string)) error {
				for range 1000 {
					say("still working")
				}
				return nil
			}},
		}, func() error { return nil })
		if err := s.Start(input); err != nil {
			t.Fatal(err)
		}
		<-s.done
		if result, err := s.close(); err != nil || result.Outcome != RunComplete {
			t.Fatalf("got %+v, %v", result, err)
		}
	})
}

// The Tor check's wait gives way to a pause the moment it is asked, and
// says how far Tor is. It asks Tor once before it gives way, so that a Tor
// that is ready passes, which after the upgrade keeps a finished step.
func TestTheTorWaitGivesWayToAPauseButAsksTorFirst(t *testing.T) {
	ask := torProgress
	t.Cleanup(func() { torProgress = ask })
	synctest.Test(t, func(t *testing.T) {
		torProgress = func() (int, error) { return 52, nil }
		ctx, stop := context.WithCancel(context.Background())
		time.AfterFunc(7*time.Second, stop)
		var said []string
		start := time.Now()
		err := waitForTorBootstrap(ctx, func(text string) { said = append(said, text) }, torStallWindow, torBootstrapCeiling)
		if !errors.Is(err, context.Canceled) || time.Since(start) != 7*time.Second {
			t.Fatalf("the wait ended after %v with %v, want the pause after 7s", time.Since(start), err)
		}
		if len(said) != 1 || !strings.Contains(said[0], "52") {
			t.Fatalf("the wait said %q, want Tor's number once", said)
		}

		// The pause is already asked. A Tor that is ready still passes.
		torProgress = func() (int, error) { return 100, nil }
		if err := waitForTorBootstrap(ctx, func(string) {}, torStallWindow, torBootstrapCeiling); err != nil {
			t.Fatalf("a ready Tor did not pass: %v", err)
		}
		// A Tor that is not ready gives way without a sleep, whether it
		// answers or not, also in the check after the upgrade.
		start = time.Now()
		for _, answer := range []func() (int, error){
			func() (int, error) { return 52, nil },
			func() (int, error) { return 0, errors.New("no answer") },
		} {
			torProgress = answer
			if err := waitForTorBootstrap(ctx, func(string) {}, torStallWindow, torBootstrapCeiling); !errors.Is(err, context.Canceled) || time.Since(start) != 0 {
				t.Fatalf("a Tor that is not ready: %v after %v", err, time.Since(start))
			}
			if err := verifyTorAfterUpgrade(ctx, func(string) {}); !errors.Is(err, context.Canceled) || time.Since(start) != 0 {
				t.Fatalf("the check after the upgrade: %v after %v", err, time.Since(start))
			}
		}
	})
}
