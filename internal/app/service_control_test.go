package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/virtpriv/node/internal/helper"
	"github.com/virtpriv/node/internal/servicecontrol"
)

func TestServiceControlBoundAndClose(t *testing.T) {
	for _, closeEarly := range []bool{false, true} {
		t.Run(map[bool]string{false: "deadline", true: "close joins without consumer"}[closeEarly], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := NewServiceControls()
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
				request, _ := servicecontrol.New("lnd", "restart")
				started := time.Now()
				results := s.Control(request)
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
				if calls != 1 || result.Outcome != ServiceActionUnconfirmed || !errors.Is(result.Err, wantErr) {
					t.Fatalf("retried or misreported observation loss: %+v, calls %d", result, calls)
				}
				if !closeEarly && time.Since(started) != 35*time.Minute {
					t.Fatal("wrong observation bound")
				}
				if _, ok := <-results; ok {
					t.Fatal("duplicate result")
				}
				s.Close()
				if result := <-s.Control(request); result.Outcome != ServiceActionNotStarted || calls != 1 {
					t.Fatal("accepted work after shutdown")
				}
			})
		})
	}
}

func TestServiceControlHelperBoundary(t *testing.T) {
	request, _ := servicecontrol.New("tor", "stop")
	refused := errors.New("helper refused service control")
	for _, tc := range []struct {
		name, payload string
		err           error
		want          ServiceActionOutcome
	}{
		{"verified worker stop", `{"unit":"tor@default.service","action":"stop","state":"inactive"}`, nil, ServiceActionCompleted},
		{"legacy missing result", "", nil, ServiceActionUnconfirmed},
		{"wrapper completion", `{"unit":"tor.service","action":"stop","state":"inactive"}`, nil, ServiceActionUnconfirmed},
		{"different action", `{"unit":"tor@default.service","action":"restart","state":"active"}`, nil, ServiceActionUnconfirmed},
		{"still running", `{"unit":"tor@default.service","action":"stop","state":"active"}`, nil, ServiceActionUnconfirmed},
		{"malformed result", `{`, nil, ServiceActionUnconfirmed},
		{"helper error", "", refused, ServiceActionUnconfirmed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewServiceControls()
			defer s.Close()
			calls := 0
			// Replace transport only: request construction, completion validation
			// and outcome classification all run through the application owner.
			s.call = func(_ context.Context, verb string, params, result any) error {
				calls++
				if verb != helper.VerbServiceAction || params != (helper.ServiceActionParams{Unit: "tor", Action: "stop"}) {
					t.Errorf("wrong helper request: %s %+v", verb, params)
				}
				if tc.err != nil || tc.payload == "" {
					return tc.err
				}
				return json.Unmarshal([]byte(tc.payload), result)
			}
			result := <-s.Control(request)
			if result.Outcome != tc.want || calls != 1 || (result.Err == nil) != (tc.want == ServiceActionCompleted) {
				t.Fatalf("unexpected result %+v, calls %d", result, calls)
			}
			if tc.err != nil && !errors.Is(result.Err, tc.err) {
				t.Fatalf("helper error lost: %v", result.Err)
			}
		})
	}
}

func TestServiceControlRejectsUnvalidatedRequest(t *testing.T) {
	s := NewServiceControls()
	defer s.Close()
	s.call = func(context.Context, string, any, any) error {
		t.Error("unvalidated request reached helper transport")
		return nil
	}
	if result := <-s.Control(servicecontrol.Request{}); result.Outcome != ServiceActionNotStarted || result.Err == nil {
		t.Fatalf("unvalidated request accepted: %+v", result)
	}
}

func TestTorObservationUsesWorkerWhileWrapperIsActive(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\ncase \"$*\" in\n 'show --property=ActiveState --value tor@default.service') echo inactive;;\n 'show --property=ActiveState --value tor.service'|'show --property=ActiveState --value tor') echo active;;\n *) exit 2;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	collector := NewStatusCollector()
	defer collector.Close()
	active, err := collector.sources.service(t.Context(), "tor")
	if err != nil || active {
		t.Fatalf("worker observation: active %v, err %v", active, err)
	}
}
