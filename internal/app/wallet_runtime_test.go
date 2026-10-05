package app

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/virtpriv/node/internal/lndrpc"
)

func TestWalletObservationRequiresExplicitFact(t *testing.T) {
	for _, tc := range []struct {
		body string
		want WalletPresence
	}{
		{`{"wallet_exists":true}`, WalletPresent},
		{`{"wallet_exists":false}`, WalletAbsent},
		{"", WalletUnknown}, {`null`, WalletUnknown}, {`{}`, WalletUnknown},
		{`{"wallet_exists":null}`, WalletUnknown}, {`{"wallet_exists":"false"}`, WalletUnknown},
	} {
		t.Run(tc.body, func(t *testing.T) {
			r := NewWalletRuntime()
			defer r.Close()
			r.read = func(_ context.Context, result any) error {
				if tc.body == "" {
					return nil // Successful terminator with no result body.
				}
				return json.Unmarshal([]byte(tc.body), result)
			}
			got := r.Read(t.Context())
			if got.Presence != tc.want || (got.Err != nil) != (tc.want == WalletUnknown) {
				t.Fatalf("incomplete response became a fact: %+v", got)
			}
		})
	}
	r := NewWalletRuntime()
	defer r.Close()
	failure := errors.New("helper unavailable")
	r.read = func(_ context.Context, result any) error {
		json.Unmarshal([]byte(`{"wallet_exists":false}`), result)
		return failure
	}
	if got := r.Read(t.Context()); got.Presence != WalletUnknown || !errors.Is(got.Err, failure) {
		t.Fatalf("failed observation became absence: %+v", got)
	}
}

func TestWalletRuntimeCancelsAndJoinsReadsAndInitialization(t *testing.T) {
	for _, opening := range []bool{false, true} {
		for _, reason := range []string{"caller", "deadline", "session"} {
			t.Run(fmtWalletCase(opening, reason), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					r := NewWalletRuntime()
					defer r.Close()
					caller, cancel := context.WithCancel(t.Context())
					defer cancel()
					entered := make(chan struct{})
					cleanup := make(chan struct{})
					closedClients, calls := 0, 0
					r.closeClient = func(*lndrpc.Client) { closedClients++ }
					wait := func(ctx context.Context) {
						calls++
						close(entered)
						<-ctx.Done()
						if reason == "session" {
							<-cleanup
						}
					}
					r.read = func(ctx context.Context, result any) error {
						wait(ctx)
						return json.Unmarshal([]byte(`{"wallet_exists":true}`), result)
					}
					r.open = func(ctx context.Context, _ bool) (*lndrpc.Client, error) {
						wait(ctx)
						return &lndrpc.Client{}, nil // A late success still needs cleanup.
					}
					done := make(chan error, 1)
					start := time.Now()
					go func() {
						if opening {
							result := r.Open(caller, false)
							if result.Client != nil {
								t.Error("canceled initialization returned a client")
							}
							done <- result.Err
						} else {
							result := r.Read(caller)
							if result.Presence != WalletUnknown {
								t.Error("canceled read published a fact")
							}
							done <- result.Err
						}
					}()
					<-entered
					want := context.Canceled
					switch reason {
					case "caller":
						cancel()
					case "deadline":
						want = context.DeadlineExceeded
					case "session":
						closed := make(chan struct{})
						go func() { r.Close(); close(closed) }()
						synctest.Wait()
						select {
						case <-closed:
							t.Fatal("Close returned before canceled worker cleanup")
						default:
						}
						close(cleanup)
						<-closed
					}
					if err := <-done; !errors.Is(err, want) {
						t.Fatalf("cancellation: %v", err)
					}
					if reason == "deadline" {
						limit := 30 * time.Second
						if opening {
							limit = 75 * time.Second
						}
						if time.Since(start) != limit {
							t.Fatalf("wrong total lifetime: %s", time.Since(start))
						}
					}
					r.Close()
					if r.Open(t.Context(), false).Err == nil || r.Read(t.Context()).Err == nil || calls != 1 {
						t.Fatal("closed owner started more work")
					}
					if opening && closedClients != 1 {
						t.Fatalf("late client closed %d times", closedClients)
					}
				})
			})
		}
	}
}

func fmtWalletCase(opening bool, reason string) string {
	if opening {
		return "initialize/" + reason
	}
	return "observe/" + reason
}

func TestWalletRuntimeClientOwnership(t *testing.T) {
	r := NewWalletRuntime()
	closed := make(map[*lndrpc.Client]int)
	r.closeClient = func(c *lndrpc.Client) { closed[c]++ }
	r.open = func(context.Context, bool) (*lndrpc.Client, error) { return &lndrpc.Client{}, nil }
	claimed := r.Open(t.Context(), false).Client
	discarded := r.Open(t.Context(), true).Client
	undelivered := r.Open(t.Context(), false).Client
	if !r.Claim(claimed) || r.Claim(claimed) {
		t.Fatal("client ownership did not transfer exactly once")
	}
	r.Discard(claimed)
	r.Discard(discarded)
	r.Discard(discarded)
	r.Close()
	r.Close()
	if closed[claimed] != 0 || closed[discarded] != 1 || closed[undelivered] != 1 || r.Claim(undelivered) {
		t.Fatalf("incorrect ownership/cleanup: %v", closed)
	}
}

func TestWalletRuntimeRejectsCanceledAndFailedInitialization(t *testing.T) {
	r := NewWalletRuntime()
	defer r.Close()
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	calls, closed := 0, 0
	r.closeClient = func(*lndrpc.Client) { closed++ }
	failure := errors.New("staged credentials unreadable")
	r.open = func(context.Context, bool) (*lndrpc.Client, error) {
		calls++
		return &lndrpc.Client{}, failure
	}
	if got := r.Open(canceled, false); !errors.Is(got.Err, context.Canceled) || calls != 0 {
		t.Fatal("canceled command entered initialization")
	}
	if got := r.Open(t.Context(), false); !errors.Is(got.Err, failure) || got.Client != nil || closed != 1 {
		t.Fatalf("failed initialization leaked a client: %+v", got)
	}
	r.open = func(context.Context, bool) (*lndrpc.Client, error) { return nil, nil }
	if got := r.Open(t.Context(), true); got.Err == nil {
		t.Fatal("missing client reported success")
	}
}
