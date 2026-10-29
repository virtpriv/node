package update

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/virtpriv/node/internal/host"
	"github.com/virtpriv/node/internal/update/protocol"
)

// failedWith runs the fixture update with h prepared by setup and returns the
// saved record of the failure.
func failedWith(t *testing.T, setup func(*workflowHarness)) *job {
	t.Helper()
	j := workflowFixture()
	h := newHarness(t, j)
	setup(h)
	if err := runJob(j, h.ops()); err == nil {
		t.Fatal("the update did not fail")
	}
	saved := h.reload()
	if saved.Phase != "failed" || saved.Error == "" {
		t.Fatalf("failure not saved: phase %s, error %q", saved.Phase, saved.Error)
	}
	return saved
}

func sameFailure(got, want *protocol.Failure) bool {
	if got == nil || want == nil {
		return got == want
	}
	return *got == *want
}

// The installed Node Updates screen gives an exact next step only for a code
// it knows. Each known failure must save its code and service, and keep the
// raw error as it was, since the screen shows that under the failed step.
func TestKnownFailuresSaveTheirCode(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*workflowHarness)
		want  protocol.Failure
		text  string
	}{
		{"a stopped service at the node check", func(h *workflowHarness) {
			h.running[protocol.LND], h.absentReason = false, host.ErrUpdateServiceNotActive
		}, protocol.Failure{Code: protocol.FailureServiceNotRunning, Service: protocol.LND},
			"cancel this update and restart lnd first: lnd is not active"},
		{"another program at the node check", func(h *workflowHarness) {
			h.running[protocol.Bitcoin], h.absentReason = false, host.ErrUpdateServiceChanged
		}, protocol.Failure{Code: protocol.FailureServiceChanged, Service: protocol.Bitcoin},
			"cancel this update and restart bitcoin first: bitcoin is running a different executable"},
		{"an unclean stop", func(h *workflowHarness) {
			h.failure, h.failWith = "stop:lnd", fmt.Errorf("lnd %w", host.ErrUpdateServiceStop)
		}, protocol.Failure{Code: protocol.FailureStopFailed, Service: protocol.LND},
			"lnd did not stop cleanly, no binaries were replaced"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saved := failedWith(t, tc.setup)
			if !sameFailure(saved.Failure, &tc.want) {
				t.Fatalf("saved failure %+v, want %+v", saved.Failure, tc.want)
			}
			if first, _, _ := strings.Cut(saved.Error, "\n"); first != tc.text {
				t.Fatalf("raw error %q, want %q", first, tc.text)
			}
		})
	}

	t.Run("an interrupted start", func(t *testing.T) {
		j := workflowFixture()
		h := newHarness(t, j)
		h.interrupt, h.after = "start:lnd", true
		runInterrupted(t, j, h.ops())
		h.interrupt = ""
		h.running[protocol.LND] = false // the reboot ended lnd
		if err := runJob(h.reload(), h.ops()); err == nil {
			t.Fatal("an interrupted start did not require Retry")
		}
		want := protocol.Failure{Code: protocol.FailureStartInterrupted, Service: protocol.LND}
		saved := h.reload()
		if !sameFailure(saved.Failure, &want) {
			t.Fatalf("saved failure %+v, want %+v", saved.Failure, want)
		}
		if first, _, _ := strings.Cut(saved.Error, "\n"); !strings.HasPrefix(first, "lnd startup was interrupted, review the failure and retry: ") {
			t.Fatalf("raw error %q", first)
		}
	})
}

// Any other failure gets the screen's general advice, so it saves no code.
// That includes a host error a code exists for when it comes from the
// cleanup after another failure, not from the failure itself.
func TestOtherFailuresSaveNoCode(t *testing.T) {
	for name, setup := range map[string]func(*workflowHarness){
		"a failed download": func(h *workflowHarness) { h.failure = "stage" },
		"a failed stop command": func(h *workflowHarness) {
			h.failure, h.failWith = "stop:lnd", errors.New("stop lnd: exit status 1")
		},
		"a node check that cannot read the process": func(h *workflowHarness) { h.running[protocol.LND] = false },
		"a failed install whose cleanup finds an unclean stop": func(h *workflowHarness) {
			h.failure, h.quarantineErr = "install", fmt.Errorf("lnd %w", host.ErrUpdateServiceStop)
		},
	} {
		t.Run(name, func(t *testing.T) {
			if saved := failedWith(t, setup); saved.Failure != nil {
				t.Fatalf("saved failure %+v for a failure without a code", saved.Failure)
			}
		})
	}
}

// A cleanup error joined to a known failure is shown in the raw error but
// must not take away the code of the failure it follows.
func TestCleanupKeepsTheCode(t *testing.T) {
	saved := failedWith(t, func(h *workflowHarness) {
		h.failure, h.failWith = "stop:lnd", fmt.Errorf("lnd %w", host.ErrUpdateServiceStop)
		h.quarantineErr = errors.New("guard lnd: exit status 1")
	})
	want := protocol.Failure{Code: protocol.FailureStopFailed, Service: protocol.LND}
	if !sameFailure(saved.Failure, &want) {
		t.Fatalf("saved failure %+v, want %+v", saved.Failure, want)
	}
	if !strings.Contains(saved.Error, "guard lnd: exit status 1") {
		t.Fatal("the cleanup error is missing from the raw error")
	}
}

// After a Retry the old code must not remain, whether the next run succeeds
// or fails in another way. The record keeps the code here, as a helper that
// did not remove it would leave it.
func TestRetryLeavesNoOldCode(t *testing.T) {
	for name, next := range map[string]string{"succeeds": "", "fails without a code": "install"} {
		t.Run(name, func(t *testing.T) {
			j := workflowFixture()
			h := newHarness(t, j)
			h.failure, h.failWith = "stop:lnd", fmt.Errorf("lnd %w", host.ErrUpdateServiceStop)
			if err := runJob(j, h.ops()); err == nil {
				t.Fatal("injected failure did not fail the update")
			}
			j = h.reload()
			if j.Failure == nil {
				t.Fatal("the first failure saved no code")
			}
			j.Phase, j.Error = "retry", ""
			h.failure, h.failWith = next, nil
			err := runJob(j, h.ops())
			if (err == nil) != (next == "") {
				t.Fatalf("retry result %v", err)
			}
			if saved := h.reload(); saved.Failure != nil {
				t.Fatalf("old code %+v kept after the retry, phase %s", saved.Failure, saved.Phase)
			}
		})
	}
}

// The worker saves the screen's next step and words with each failure, so a
// later release can lead an installed screen with its own advice.
func TestFailuresSaveTheirNextStep(t *testing.T) {
	stop := failedWith(t, func(h *workflowHarness) {
		h.failure, h.failWith = "stop:lnd", fmt.Errorf("lnd %w", host.ErrUpdateServiceStop)
	})
	if stop.Next != protocol.NextContinue || stop.Advice == "" {
		t.Fatalf("an LND stop saved next %q, advice %q", stop.Next, stop.Advice)
	}
	other := failedWith(t, func(h *workflowHarness) { h.failure = "install" })
	if other.Next != protocol.NextRetry || other.Advice == "" {
		t.Fatalf("a failure without a code saved next %q, advice %q", other.Next, other.Advice)
	}
}
