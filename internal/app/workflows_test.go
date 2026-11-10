package app

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/virtpriv/node/internal/api"
	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/p2p"
)

type waitingP2PConnection struct{ started, stopped bool }

func (c *waitingP2PConnection) ReconnectContext(ctx context.Context) {
	c.started = true
	<-ctx.Done()
	c.stopped = true
}

func TestP2PReconnectBelongsToWorkflowLifetime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := NewWorkflows("2.1.1")
		w.jobs.call = (&fakeVPND{reads: []func() (api.Job, error){job(api.JobDone, false)}}).call
		w.loadConfig = func() (*config.AppConfig, error) { return config.Default(), nil }
		client := &waitingP2PConnection{}
		op := w.UpgradeP2P(reviewedP2PRequest(t), client)
		synctest.Wait()
		if !client.started {
			t.Fatal("successful P2P change did not reconnect")
		}
		w.Close()
		if !client.stopped {
			t.Fatal("shutdown did not join the reconnect")
		}
		for range op.Events() {
		}
	})
}

func reviewedP2PRequest(t *testing.T) p2p.UpgradeRequest {
	t.Helper()
	request, err := p2p.NewUpgradeRequest("203.0.113.7")
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func TestP2PAddressReadsAreFreshAndOwned(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := NewWorkflows("2.1.1")
		defer w.Close()
		calls := 0
		w.address = func(context.Context) (string, error) {
			calls++
			if calls == 1 {
				return "", errors.New("route unavailable")
			}
			if calls == 2 {
				return "203.0.113.7", nil
			}
			return "203.0.113.8", nil
		}
		if _, err := w.ReadP2PAddress(context.Background()); err == nil {
			t.Fatal("failed observation became an address")
		}
		first, err := w.ReadP2PAddress(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		second, err := w.ReadP2PAddress(context.Background())
		if err != nil || first == second {
			t.Fatal("cached address was reused")
		}
		for _, how := range []string{"screen", "deadline", "terminal"} {
			w.address = func(ctx context.Context) (string, error) { <-ctx.Done(); return "203.0.113.7", nil }
			parent, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { _, err := w.ReadP2PAddress(parent); done <- err }()
			synctest.Wait()
			started := time.Now()
			wantErr := context.Canceled
			switch how {
			case "screen":
				cancel()
			case "deadline":
				wantErr = context.DeadlineExceeded
				time.Sleep(4 * time.Second)
			case "terminal":
				w.Close()
			}
			synctest.Wait()
			// Do not block receiving here: synctest would advance to the
			// deadline and could hide missing screen/terminal cancellation.
			select {
			case err := <-done:
				if !errors.Is(err, wantErr) {
					t.Fatalf("%s returned %v, want %v", how, err, wantErr)
				}
			default:
				t.Fatalf("%s did not release the address read", how)
			}
			if how != "deadline" && time.Since(started) != 0 {
				t.Fatalf("%s waited for a timer instead of cancellation", how)
			}
			cancel()
		}
		if _, err := w.ReadP2PAddress(context.Background()); !errors.Is(err, context.Canceled) {
			t.Fatal("read accepted after close")
		}
	})
}
