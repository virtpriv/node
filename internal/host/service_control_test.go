package host

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"testing"
	"time"

	"github.com/virtpriv/node/internal/servicecontrol"
)

// The child stands in for systemctl and cannot operate on real services.
func TestServiceControlProcess(t *testing.T) {
	switch os.Getenv("VPN_SERVICE_CONTROL_TEST") {
	case "success":
		os.Exit(0)
	case "fail":
		os.Exit(1)
	case "block":
		time.Sleep(5 * time.Minute)
		os.Exit(2)
	}
}

func TestServiceControlExecutionAndPostcondition(t *testing.T) {
	for _, tc := range []struct {
		name, action, mode, state string
		queryErr                  error
		wantOK                    bool
	}{
		{name: "worker started", action: "start", mode: "success", state: "active", wantOK: true},
		{name: "worker restarted", action: "restart", mode: "success", state: "active", wantOK: true},
		{name: "worker stopped", action: "stop", mode: "success", state: "inactive", wantOK: true},
		{name: "failed query is not stopped", action: "stop", mode: "success", queryErr: errors.New("system bus unavailable")},
		{name: "still stopping", action: "stop", mode: "success", state: "deactivating"},
		{name: "still active", action: "stop", mode: "success", state: "active"},
		{name: "failed unit", action: "restart", mode: "success", state: "failed"},
		{name: "still activating", action: "start", mode: "success", state: "activating"},
		{name: "command failure", action: "restart", mode: "fail"},
		{name: "command timeout", action: "restart", mode: "block"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, err := servicecontrol.New("tor", tc.action)
			if err != nil {
				t.Fatal(err)
			}
			var child *exec.Cmd
			timeout := 5 * time.Second
			if tc.mode == "block" {
				timeout = 200 * time.Millisecond
			}
			queries := 0
			completion, err := controlService(request, timeout,
				func(ctx context.Context, name string, args ...string) *exec.Cmd {
					if name != "systemctl" || !reflect.DeepEqual(args, []string{tc.action, "tor@default.service"}) {
						t.Fatalf("wrong command: %s %v", name, args)
					}
					child = exec.CommandContext(ctx, os.Args[0], "-test.run=^TestServiceControlProcess$")
					child.Env = append(os.Environ(), "VPN_SERVICE_CONTROL_TEST="+tc.mode)
					return child
				}, func(_ context.Context, unit string) (string, error) {
					queries++
					if child.ProcessState == nil || unit != "tor@default.service" {
						t.Fatal("postcondition queried before completion or against the wrapper")
					}
					return tc.state, tc.queryErr
				})
			if (err == nil) != tc.wantOK {
				t.Fatalf("completion %+v, err %v", completion, err)
			}
			if child.ProcessState == nil {
				t.Fatal("child was not reaped")
			}
			wantQueries := 0
			if tc.mode == "success" {
				wantQueries = 1
			}
			if queries != wantQueries {
				t.Fatalf("queries = %d, want %d", queries, wantQueries)
			}
			if tc.mode == "block" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("timeout: %v", err)
			}
		})
	}
}

func TestServiceControlRejectsUnvalidatedRequest(t *testing.T) {
	_, err := controlService(servicecontrol.Request{}, time.Second,
		func(context.Context, string, ...string) *exec.Cmd {
			t.Fatal("zero request reached execution")
			return nil
		}, nil)
	if err == nil {
		t.Fatal("zero request accepted")
	}
}
