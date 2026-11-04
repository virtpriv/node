package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"testing/synctest"
	"time"
)

func TestRebootBoundAndClose(t *testing.T) {
	for _, closeEarly := range []bool{false, true} {
		t.Run(map[bool]string{false: "deadline", true: "close joins without consumer"}[closeEarly], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := NewReboots()
				defer s.Close()
				release := make(chan struct{})
				calls := 0
				s.call = func(ctx context.Context, _ string, _, _ any) error {
					calls++
					<-ctx.Done()
					if closeEarly {
						<-release
					}
					return ctx.Err()
				}
				started := time.Now()
				results := s.Request()
				wantErr := context.DeadlineExceeded
				if closeEarly {
					wantErr = context.Canceled
					synctest.Wait()
					closed := make(chan struct{})
					go func() { s.Close(); close(closed) }()
					synctest.Wait()
					select {
					case <-closed:
						t.Fatal("Close did not join reader")
					default:
					}
					close(release)
					<-closed
				}
				result := <-results
				if calls != 1 || result.Outcome != RebootUnconfirmed || !errors.Is(result.Err, wantErr) {
					t.Fatalf("retried or misreported observation loss: %+v, calls %d", result, calls)
				}
				if !closeEarly && time.Since(started) != 95*time.Minute {
					t.Fatal("wrong observation bound")
				}
				if _, ok := <-results; ok {
					t.Fatal("duplicate result")
				}
				s.Close()
				if result := <-s.Request(); result.Outcome != RebootNotStarted || calls != 1 {
					t.Fatal("accepted work after shutdown")
				}
			})
		})
	}
}

func TestRebootRequiresAcceptanceReply(t *testing.T) {
	for _, tc := range []struct {
		name, payload string
		err           error
		want          RebootOutcome
	}{
		{"accepted", `{"accepted":true}`, nil, RebootAccepted},
		{"old helper", "", nil, RebootUnconfirmed},
		{"not accepted", `{"accepted":false}`, nil, RebootUnconfirmed},
		{"connection lost", "", io.ErrUnexpectedEOF, RebootUnconfirmed},
		{"helper refusal", "", errors.New("systemctl failed"), RebootUnconfirmed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := NewReboots()
			defer r.Close()
			calls := 0
			r.call = func(_ context.Context, verb string, params, result any) error {
				calls++
				if verb != "reboot" || params != nil {
					t.Errorf("unexpected request: %s %+v", verb, params)
				}
				if tc.payload != "" {
					if err := json.Unmarshal([]byte(tc.payload), result); err != nil {
						return err
					}
				}
				return tc.err
			}
			result := <-r.Request()
			if calls != 1 || result.Outcome != tc.want || (result.Err == nil) != (tc.want == RebootAccepted) {
				t.Fatalf("result %+v, calls %d", result, calls)
			}
			if tc.err != nil && !errors.Is(result.Err, tc.err) {
				t.Fatalf("lost error: %v", result.Err)
			}
		})
	}
}
