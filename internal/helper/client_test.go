package helper

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/virtpriv/node/internal/paths"
)

// An in-memory peer exercises the production scanner and cancellation path
// without requiring permission to create an operating-system socket.
func helperClientFixture(t *testing.T, ctx context.Context, handler func(net.Conn)) *Session {
	t.Helper()
	client, peer := net.Pipe()
	session := newSession(ctx, client, VerbSyncthingInstall)
	done := make(chan struct{})
	go func() { defer close(done); defer peer.Close(); handler(peer) }()
	t.Cleanup(func() { session.Close(); peer.Close(); <-done })
	return session
}

func TestSessionCancellationClosesBlockedReader(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		accepted := make(chan struct{})
		finish := make(chan struct{})
		finished := make(chan struct{})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		session := helperClientFixture(t, ctx, func(conn net.Conn) {
			close(accepted)
			<-finish
			// This stands for an already accepted operation. The client going away
			// must not be mistaken for cancellation of the peer's work.
			fmt.Fprintln(conn, `{"event":"step","index":0}`)
			close(finished)
		})
		<-accepted
		readDone := make(chan error, 1)
		go func() { readDone <- session.WaitStep(0) }()
		synctest.Wait()
		cancel()
		select {
		case err := <-readDone:
			if err == nil {
				t.Error("cancelled observation reported success")
			}
		case <-time.After(3 * time.Second):
			t.Error("cancellation failed to unblock progress read")
		}
		session.Close()
		close(finish)
		<-finished
	})
}

func TestSessionRequiresCompleteProtocol(t *testing.T) {
	for _, tc := range []struct {
		name, events string
		wantOK       bool
	}{
		{"success", "{\"event\":\"step\",\"index\":0}\n{\"event\":\"end\",\"ok\":true}\n", true},
		{"EOF after step", "{\"event\":\"step\",\"index\":0}\n", false},
		{"failed terminator", "{\"event\":\"step\",\"index\":0}\n{\"event\":\"end\",\"error\":\"injected failure\"}\n", false},
		{"missing step", "{\"event\":\"end\",\"ok\":true}\n", false},
		{"malformed", "not-json\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := helperClientFixture(t, t.Context(), func(conn net.Conn) { fmt.Fprint(conn, tc.events) })
			err := session.WaitStep(0)
			if err == nil {
				err = session.Wait(nil)
			}
			if (err == nil) != tc.wantOK {
				t.Fatalf("unexpected result: %v", err)
			}
		})
	}
}

// Only a request that never reached the helper may suggest a restart or a
// reboot. After the request is sent the helper may still be working, an apt
// run for example, and a reboot then could break it.
func TestHelperAdviceDependsOnWhetherTheRequestWasSent(t *testing.T) {
	// A short folder: a socket path has a length limit.
	dir, err := os.MkdirTemp("", "vpnh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir); helperSocket = paths.HelperSocket })

	t.Run("not sent", func(t *testing.T) {
		helperSocket = filepath.Join(dir, "absent.sock")
		_, err := Start(VerbSyncthingInstall, nil)
		if err == nil || !strings.Contains(err.Error(), "sudo systemctl restart vpn-helperd.socket") {
			t.Fatalf("closed helper without its repair: %v", err)
		}
	})
	t.Run("reply lost", func(t *testing.T) {
		helperSocket = filepath.Join(dir, "h.sock")
		ln, err := net.Listen("unix", helperSocket)
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		go func() {
			if conn, err := ln.Accept(); err == nil {
				io.ReadAll(conn)
				conn.Close()
			}
		}()
		err = Call(VerbSyncthingInstall, nil, nil)
		if err == nil || strings.Contains(err.Error(), "restart") || strings.Contains(err.Error(), "reboot") {
			t.Fatalf("lost reply suggested a restart or reboot: %v", err)
		}
	})
}
