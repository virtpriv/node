package system

import (
	"context"
	"time"
)

// Sleep waits for d, or until ctx ends. It returns ctx's error when ctx has
// ended, also when it had ended before the call, and nil after a full sleep.
// A step that waits uses it so that a stop the owner asked for ends the
// wait at once.
func Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
