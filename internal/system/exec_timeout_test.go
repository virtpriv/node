package system

import (
	"testing"
	"time"
)

// The time limit must hold even when the command leaves a child process
// behind that keeps its output open. Without a bound the caller would wait
// for that child, however long it runs.
func TestRunOutputWithTimeoutHoldsItsLimit(t *testing.T) {
	const limit = 300 * time.Millisecond
	for _, tc := range []struct {
		name    string
		args    []string
		wantErr bool
		within  time.Duration
	}{
		{"finishes", []string{"sh", "-c", "echo done"}, false, time.Second},
		{"hangs", []string{"sleep", "8"}, true, 2 * time.Second},
		{"leaves a child holding its output", []string{"sh", "-c", "sleep 8 & wait"}, true, 5 * time.Second},
		{"exits but leaves a child holding its output", []string{"sh", "-c", "echo done; sleep 8 &"}, true, 5 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			out, err := RunOutputWithTimeout(limit, tc.args[0], tc.args[1:]...)
			took := time.Since(start)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v, want error %t", err, tc.wantErr)
			}
			if !tc.wantErr && out != "done" {
				t.Fatalf("output %q, want done", out)
			}
			if took > tc.within {
				t.Fatalf("returned after %s, want within %s", took, tc.within)
			}
		})
	}
}
