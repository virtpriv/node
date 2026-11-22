package system

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

// A sleep ends at once when a stop is asked, and says so. Without a stop it
// lasts its whole time.
func TestSleepEndsAtOnceWhenStopped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, stop := context.WithCancel(context.Background())
		start := time.Now()
		if err := Sleep(ctx, time.Minute); err != nil || time.Since(start) != time.Minute {
			t.Fatalf("a full sleep took %v and returned %v", time.Since(start), err)
		}
		time.AfterFunc(time.Second, stop)
		start = time.Now()
		if err := Sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) || time.Since(start) != time.Second {
			t.Fatalf("a stopped sleep took %v and returned %v", time.Since(start), err)
		}
		if err := Sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) || time.Since(start) != time.Second {
			t.Fatalf("a sleep after the stop took %v and returned %v", time.Since(start), err)
		}
		// Also a sleep of no time, where both ends are ready at once.
		for range 100 {
			if err := Sleep(ctx, 0); !errors.Is(err, context.Canceled) {
				t.Fatalf("a sleep of no time after the stop returned %v", err)
			}
		}
	})
}
