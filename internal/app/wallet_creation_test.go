package app

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/virtpriv/node/internal/lndrpc"
)

func TestWalletCreationReadiness(t *testing.T) {
	for _, state := range []lndrpc.WalletState{"NON_EXISTING", "LOCKED", "UNLOCKED", "RPC_ACTIVE", "SERVER_ACTIVE", "FUTURE_STATE"} {
		t.Run(string(state), func(t *testing.T) {
			w := NewWalletCreation()
			defer w.Close()
			w.read = func(context.Context) (lndrpc.WalletState, error) { return state, nil }
			w.stage = func(context.Context) error { t.Fatal("readiness staged credentials"); return nil }
			network, err := w.Prepare("public-signet")
			if state == "NON_EXISTING" {
				if err != nil || network != "signet" {
					t.Fatalf("network=%q err=%v", network, err)
				}
			} else if err == nil {
				t.Fatal("creation allowed without a known absent wallet")
			}
		})
	}
	w := NewWalletCreation()
	defer w.Close()
	w.read = func(context.Context) (lndrpc.WalletState, error) {
		t.Fatal("invalid profile reached LND")
		return "", nil
	}
	if _, err := w.Prepare("signet"); err == nil {
		t.Fatal("raw signet profile accepted")
	}
}

func TestWalletCreationReadinessBounds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := NewWalletCreation()
		defer w.Close()
		calls := 0
		w.read = func(ctx context.Context) (lndrpc.WalletState, error) {
			calls++
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(5 * time.Second):
				return "", errors.New("transport unavailable")
			}
		}
		start := time.Now()
		if _, err := w.Prepare("mainnet"); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("deadline: %v", err)
		}
		if time.Since(start) != 120*time.Second || calls < 2 {
			t.Fatalf("duration=%s calls=%d", time.Since(start), calls)
		}
	})
	synctest.Test(t, func(t *testing.T) {
		w := NewWalletCreation()
		calls := 0
		w.read = func(context.Context) (lndrpc.WalletState, error) {
			calls++
			if calls == 1 {
				return "WAITING_TO_START", nil
			}
			return "NON_EXISTING", nil
		}
		if _, err := w.Prepare("mainnet"); err != nil || calls != 2 {
			t.Fatalf("calls=%d err=%v", calls, err)
		}
		w.Close()
		if _, err := w.Prepare("mainnet"); !errors.Is(err, context.Canceled) || calls != 2 {
			t.Fatal("closed workflow started another probe")
		}
	})
}

func TestWalletFinalizationPreservesCreationEvidence(t *testing.T) {
	failure := errors.New("injected failure")
	for _, tc := range []struct {
		name                        string
		execution                   WalletExecution
		state                       lndrpc.WalletState
		readErr, stageErr           error
		presence                    WalletPresence
		stage, retry, complete, ack bool
	}{
		{"success", WalletExecution{Created: true, SeedAcknowledged: true}, "RPC_ACTIVE", nil, nil, WalletPresent, true, false, true, true},
		{"stage failed", WalletExecution{Created: true, SeedAcknowledged: true}, "RPC_ACTIVE", nil, failure, WalletPresent, true, true, false, true},
		{"lost process response", WalletExecution{Err: failure}, "LOCKED", nil, nil, WalletPresent, true, false, false, false},
		{"terminal restore failed", WalletExecution{Created: true, SeedAcknowledged: true, Err: failure}, "RPC_ACTIVE", nil, nil, WalletPresent, true, false, false, true},
		{"acknowledgement failed", WalletExecution{Created: true, Err: failure}, "RPC_ACTIVE", nil, nil, WalletPresent, true, false, false, false},
		{"read unavailable after success", WalletExecution{Created: true, SeedAcknowledged: true}, "", failure, nil, WalletPresent, true, false, true, true},
		{"unknown outcome", WalletExecution{Err: failure}, "", failure, nil, WalletUnknown, false, true, false, false},
		{"known absent", WalletExecution{Err: failure}, "NON_EXISTING", nil, nil, WalletAbsent, false, false, false, false},
		{"conflicting absence", WalletExecution{Created: true, SeedAcknowledged: true}, "NON_EXISTING", nil, nil, WalletUnknown, false, true, false, true},
		{"false acknowledgement", WalletExecution{SeedAcknowledged: true}, "LOCKED", nil, nil, WalletPresent, true, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := NewWalletCreation()
			defer w.Close()
			w.read = func(context.Context) (lndrpc.WalletState, error) { return tc.state, tc.readErr }
			staged := false
			w.stage = func(context.Context) error { staged = true; return tc.stageErr }
			r := w.Finalize(tc.execution)
			if r.Presence != tc.presence || staged != tc.stage || r.CanRetryFinalization() != tc.retry || (r.Err == nil) != tc.complete {
				t.Fatalf("result=%+v staged=%v", r, staged)
			}
			if r.CredentialsStaged != (tc.stage && tc.stageErr == nil) {
				t.Fatal("staging result lost")
			}
			if r.SeedAcknowledged != tc.ack {
				t.Fatalf("seed acknowledgement=%v, want %v", r.SeedAcknowledged, tc.ack)
			}
			if tc.execution.Err != nil && tc.name != "conflicting absence" && !errors.Is(r.Err, tc.execution.Err) {
				t.Fatal("process/terminal failure lost")
			}
		})
	}
}

func TestWalletFinalizationRetryAndShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := NewWalletCreation()
		w.read = func(context.Context) (lndrpc.WalletState, error) { return "RPC_ACTIVE", nil }
		calls := 0
		w.stage = func(ctx context.Context) error {
			calls++
			if calls == 1 {
				return errors.New("staging failed")
			}
			if calls == 2 {
				return nil
			}
			<-ctx.Done()
			return ctx.Err()
		}
		execution := WalletExecution{Created: true, SeedAcknowledged: true}
		if !w.Finalize(execution).CanRetryFinalization() {
			t.Fatal("missing retry after staging failure")
		}
		if r := w.Finalize(execution); r.Err != nil || !r.CredentialsStaged {
			t.Fatalf("retry: %+v", r)
		}
		done := make(chan WalletCreationResult, 1)
		go func() { done <- w.Finalize(execution) }()
		synctest.Wait()
		w.Close()
		if r := <-done; !errors.Is(r.Err, context.Canceled) {
			t.Fatalf("shutdown: %+v", r)
		}
		w.Finalize(execution)
		if calls != 3 {
			t.Fatal("closed workflow staged again")
		}
	})
}
