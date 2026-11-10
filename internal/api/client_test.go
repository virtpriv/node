package api

import (
	"bufio"
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

// An in-memory peer exercises the production reader and cancellation path
// without permission to create an operating-system socket.
func exchangeWith(t *testing.T, ctx context.Context, handler func(net.Conn)) error {
	t.Helper()
	client, peer := net.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); defer peer.Close(); handler(peer) }()
	t.Cleanup(func() { peer.Close(); <-done })
	return exchange(ctx, client, VerbStartJob, []byte(`{"api":1,"verb":"start-job"}`), nil)
}

// readRequest reads the one request line, as vpnd does.
func readRequest(conn net.Conn) { bufio.NewReader(conn).ReadString('\n') }

func TestCancellationClosesBlockedReader(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		finish := make(chan struct{})
		result := make(chan error, 1)
		go func() {
			result <- exchangeWith(t, ctx, func(conn net.Conn) {
				readRequest(conn)
				<-finish
			})
		}()
		synctest.Wait()
		cancel()
		select {
		case err := <-result:
			if err == nil {
				t.Error("cancelled request reported success")
			}
		case <-time.After(3 * time.Second):
			t.Error("cancellation failed to unblock the answer read")
		}
		close(finish)
	})
}

func TestAnswerDecides(t *testing.T) {
	for _, tc := range []struct {
		name, answer string
		wantOK       bool
		unchanged    bool
	}{
		{"refused before any change", "{\"ok\":false,\"error\":\"refused\",\"code\":\"nothing_changed\"}\n", false, true},
		{"busy", "{\"ok\":false,\"error\":\"Reboot is running (job x). Nothing was changed.\",\"code\":\"busy\"}\n", false, true},
		{"outside the API range", "{\"ok\":false,\"error\":\"VPN was updated.\",\"code\":\"unsupported_api\"}\n", false, true},
		{"success", "{\"ok\":true}\n", true, false},
		{"closed without an answer", "", false, false},
		{"failed", "{\"ok\":false,\"error\":\"injected failure\"}\n", false, false},
		{"malformed", "not-json\n", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := exchangeWith(t, t.Context(), func(conn net.Conn) {
				readRequest(conn)
				fmt.Fprint(conn, tc.answer)
			})
			if (err == nil) != tc.wantOK {
				t.Fatalf("unexpected result: %v", err)
			}
			if NothingChanged(err) != tc.unchanged {
				t.Fatalf("nothing-changed reported %v for %v", !tc.unchanged, err)
			}
			if tc.name == "refused before any change" && err.Error() != "refused" {
				t.Fatalf("the code changed vpnd's words: %q", err)
			}
		})
	}
}

// Only a request that never reached vpnd may suggest a restart or a reboot.
// After the request is sent a job may run, an apt run for example, and a
// reboot then could break it.
func TestAdviceDependsOnWhetherTheRequestWasSent(t *testing.T) {
	// A short folder: a socket path has a length limit.
	dir, err := os.MkdirTemp("", "vpnd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir); socket = paths.VPNDSocket })

	t.Run("not sent", func(t *testing.T) {
		socket = filepath.Join(dir, "absent.sock")
		err := Call(t.Context(), VerbStartJob, nil, nil)
		if err == nil || !strings.Contains(err.Error(), "sudo systemctl restart vpnd.socket") {
			t.Fatalf("closed vpnd without its repair: %v", err)
		}
		if !NothingChanged(err) {
			t.Fatal("a request that never reached vpnd may have changed something")
		}
	})
	t.Run("answer lost", func(t *testing.T) {
		socket = filepath.Join(dir, "v.sock")
		ln, err := net.Listen("unix", socket)
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
		err = Call(t.Context(), VerbStartJob, nil, nil)
		if err == nil || strings.Contains(err.Error(), "restart") || strings.Contains(err.Error(), "reboot") {
			t.Fatalf("lost answer suggested a restart or reboot: %v", err)
		}
		if NothingChanged(err) {
			t.Fatal("a lost answer claimed that nothing changed")
		}
	})
}
