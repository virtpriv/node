package app

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"github.com/virtpriv/node/internal/bitcoin"
	"github.com/virtpriv/node/internal/config"
)

func TestFeeCollectionProfilesAndPartialResults(t *testing.T) {
	for _, network := range []string{config.NetworkMainnet, config.NetworkTestnet4, config.NetworkPublicSignet} {
		t.Run(network, func(t *testing.T) {
			r := NewFeeReader()
			t.Cleanup(r.Close)
			profile, _ := config.NetworkConfigFromName(network)
			var targets []int
			r.estimate = func(_ context.Context, port, target int) (bitcoin.FeeEstimate, error) {
				if port != profile.RPCPort {
					t.Fatalf("estimate sent to wrong profile port: %d", port)
				}
				targets = append(targets, target)
				switch target {
				case 1:
					return bitcoin.FeeEstimate{Blocks: 2, SatPerVB: 17.5}, nil
				case 25:
					return bitcoin.FeeEstimate{Blocks: 30, SatPerVB: 3.25}, nil
				default:
					return bitcoin.FeeEstimate{}, errors.New("unavailable")
				}
			}
			result := r.Collect(t.Context(), network)
			want := [3]bitcoin.FeeEstimate{{Blocks: 2, SatPerVB: 17.5}, {}, {Blocks: 30, SatPerVB: 3.25}}
			if result.Err != nil || result.Tiers != want {
				t.Fatalf("lost or mixed returned target/rate pairs: %+v", result)
			}
			if !reflect.DeepEqual(targets, []int{1, 6, 25}) {
				t.Fatalf("unexpected suggestion requests: %v", targets)
			}
		})
	}
}

func TestFeeCollectionUnavailableAndInvalidProfile(t *testing.T) {
	r := NewFeeReader()
	t.Cleanup(r.Close)
	calls := 0
	r.estimate = func(context.Context, int, int) (bitcoin.FeeEstimate, error) {
		calls++
		return bitcoin.FeeEstimate{}, errors.New("unavailable")
	}
	if invalid := r.Collect(t.Context(), "unknown-profile"); invalid.Err == nil || calls != 0 {
		t.Fatal("invalid profile reached fee source")
	}
	if failed := r.Collect(t.Context(), config.NetworkMainnet); failed.Err == nil || failed.Tiers != [3]bitcoin.FeeEstimate{} || calls != 3 {
		t.Fatal("unavailable estimates skipped targets or became a usable fallback")
	}
}

func TestFeeCollectionCancellationAndShutdown(t *testing.T) {
	for _, reason := range []string{"form", "session", "deadline"} {
		t.Run(reason, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := NewFeeReader()
				defer r.Close()
				caller, cancel := context.WithCancel(t.Context())
				defer cancel()
				entered := make(chan context.Context, 1)
				cleanup := make(chan struct{})
				calls := 0
				r.estimate = func(ctx context.Context, _, _ int) (bitcoin.FeeEstimate, error) {
					calls++
					if calls == 1 {
						return bitcoin.FeeEstimate{Blocks: 2, SatPerVB: 8}, nil
					}
					entered <- ctx
					<-ctx.Done()
					if reason == "session" {
						<-cleanup
					}
					return bitcoin.FeeEstimate{}, ctx.Err()
				}
				done := make(chan FeeSuggestions, 1)
				go func() { done <- r.Collect(caller, config.NetworkMainnet) }()
				ctx := <-entered
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) != 30*time.Second {
					t.Fatal("fee collection is unbounded")
				}
				want := context.Canceled
				switch reason {
				case "form":
					cancel()
				case "session":
					closed := make(chan struct{})
					func() {
						defer close(cleanup)
						go func() { r.Close(); close(closed) }()
						synctest.Wait()
						select {
						case <-closed:
							t.Error("session closed before canceled source finished cleanup")
						default:
						}
					}()
					<-closed
				case "deadline":
					want = context.DeadlineExceeded
				}
				result := <-done
				if !errors.Is(result.Err, want) || result.Tiers != [3]bitcoin.FeeEstimate{} || calls != 2 {
					t.Fatalf("interrupted collection published partial data or continued: %+v, calls %d", result, calls)
				}
				r.Close()
				if late := r.Collect(t.Context(), config.NetworkMainnet); !errors.Is(late.Err, context.Canceled) || calls != 2 {
					t.Fatal("late command entered closed reader")
				}
			})
		})
	}
}
