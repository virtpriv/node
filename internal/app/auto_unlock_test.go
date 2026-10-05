package app

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/virtpriv/node/internal/autounlock"
)

func TestAutoUnlockObservations(t *testing.T) {
	password, err := autounlock.NewPassword("test wallet password")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		enable  bool
		outcome autounlock.Outcome
		failure error
		want    AutoUnlockObservation
	}{
		{"enabled", true, autounlock.Enabled, nil, AutoUnlockObserved},
		{"rejected and restored", true, autounlock.VerificationFailed, nil, AutoUnlockObserved},
		{"verification timed out and restored", true, autounlock.VerificationTimedOut, nil, AutoUnlockObserved},
		{"enable repair", true, autounlock.RepairRequired, nil, AutoUnlockObserved},
		{"disabled", false, autounlock.Disabled, nil, AutoUnlockObserved},
		{"disable rolled back", false, autounlock.StillEnabled, nil, AutoUnlockObserved},
		{"disable repair", false, autounlock.RepairRequired, nil, AutoUnlockObserved},
		{"lost enable response", true, autounlock.Enabled, errors.New("response lost"), AutoUnlockUnknown},
		{"lost disable response", false, autounlock.Disabled, errors.New("response lost"), AutoUnlockUnknown},
		{"missing result", true, "", nil, AutoUnlockUnknown},
		{"unexpected result", true, "future_outcome", nil, AutoUnlockUnknown},
		{"wrong operation result", false, autounlock.Enabled, nil, AutoUnlockUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := NewAutoUnlockChanges()
			defer w.Close()
			calls := 0
			transition := autounlock.Result{Outcome: tc.outcome, FailedStep: "test stage", Detail: "test recovery"}
			w.change = func(_ context.Context, enable bool, got autounlock.Password) (autounlock.Result, error) {
				calls++
				if enable != tc.enable || (enable && got.Text() != password.Text()) || (!enable && got.Text() != "") {
					t.Error("operation or password changed")
				}
				return transition, tc.failure
			}
			var results <-chan AutoUnlockResult
			if tc.enable {
				results = w.Enable(password)
			} else {
				results = w.Disable()
			}
			result := <-results
			if result.Observation != tc.want || calls != 1 || (result.Err == nil) != (tc.want == AutoUnlockObserved) {
				t.Fatalf("incorrect observation: %+v, calls=%d", result, calls)
			}
			if tc.want == AutoUnlockObserved && result.Transition != transition {
				t.Fatal("host recovery classification was changed")
			}
			if tc.want == AutoUnlockUnknown && result.Transition != (autounlock.Result{}) {
				t.Fatal("lost observation published a host state")
			}
			if _, ok := <-results; ok {
				t.Fatal("more than one completion")
			}
		})
	}
}

func TestAutoUnlockObservationDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := NewAutoUnlockChanges()
		defer w.Close()
		calls := 0
		w.change = func(ctx context.Context, _ bool, _ autounlock.Password) (autounlock.Result, error) {
			calls++
			<-ctx.Done()
			return autounlock.Result{}, ctx.Err()
		}
		started := time.Now()
		result := <-w.Disable()
		if result.Observation != AutoUnlockUnknown || !errors.Is(result.Err, context.DeadlineExceeded) || time.Since(started) != 25*time.Minute || calls != 1 {
			t.Fatalf("observation deadline or no-retry contract failed: %+v", result)
		}
	})
}

func TestAutoUnlockShutdownWithoutConsumer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := NewAutoUnlockChanges()
		calls := 0
		release := make(chan struct{})
		w.change = func(ctx context.Context, _ bool, _ autounlock.Password) (autounlock.Result, error) {
			calls++
			<-ctx.Done()
			<-release
			return autounlock.Result{}, ctx.Err()
		}
		if result := <-w.Enable(autounlock.Password{}); result.Observation != AutoUnlockNotStarted || calls != 0 {
			t.Fatal("invalid password reached adapter")
		}
		results := w.Disable()
		synctest.Wait()
		closed := make(chan struct{})
		go func() { w.Close(); close(closed) }()
		synctest.Wait()
		select {
		case <-closed:
			t.Fatal("Close failed to join its reader")
		default:
		}
		close(release)
		<-closed
		result := <-results
		if result.Observation != AutoUnlockUnknown || !errors.Is(result.Err, context.Canceled) {
			t.Fatal("shutdown implied root cancellation or rollback")
		}
		if result := <-w.Disable(); result.Observation != AutoUnlockNotStarted || calls != 1 {
			t.Fatal("operation started after shutdown")
		}
	})
}
