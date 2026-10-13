// internal/lndrpc/client_test.go

package lndrpc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lightningnetwork/lnd/lnrpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestWalletStateNames(t *testing.T) {
	for state, want := range map[lnrpc.WalletState]WalletState{
		lnrpc.WalletState_LOCKED:        WalletStateLocked,
		lnrpc.WalletState_SERVER_ACTIVE: WalletStateActive,
		lnrpc.WalletState_UNLOCKED:      "UNLOCKED",
	} {
		if got := walletStateName(state); got != want {
			t.Errorf("walletStateName(%s) = %s, want %s", state, got, want)
		}
	}
}

func TestSatStr(t *testing.T) {
	tests := []struct {
		input int64
		want  string
	}{
		{0, "0"}, {1000000, "1000000"}, {-500, "-500"},
	}
	for _, tt := range tests {
		if got := satStr(tt.input); got != tt.want {
			t.Errorf("satStr(%d): got %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestNilClientSafety(t *testing.T) {
	c := &Client{}
	if _, err := c.GetState(); err == nil {
		t.Error("should error")
	}
	if _, err := c.GetInfo(); err == nil {
		t.Error("should error")
	}
	if _, err := c.GetWalletBalance(); err == nil {
		t.Error("should error")
	}
	if _, err := c.ListChannels(); err == nil {
		t.Error("should error")
	}
	if _, err := c.GetPendingChannels(); err == nil {
		t.Error("should error")
	}
	if _, err := c.GetNewAddress(); err == nil {
		t.Error("should error")
	}
	if _, err := c.ListPeers(); err == nil {
		t.Error("should error")
	}
	if err := c.ConnectPeer("a", "b"); err == nil {
		t.Error("should error")
	}
	if result := c.OpenChannel(ChannelOpenRequest{}); result.Err == nil || result.Submitted {
		t.Error("should error")
	}
	if _, err := c.SendPayment("lnbc1"); err == nil {
		t.Error("should error")
	}
	if result := c.CloseChannel(ChannelCloseRequest{ChannelPoint: strings.Repeat("ab", 32) + ":0"}); result.Err == nil || result.Submitted {
		t.Error("should error")
	}
}

// macaroonCtx attaches the credential metadata to every call
// while Reconnect can rewrite the credential field on another
// goroutine. This test runs both sides at once so the race
// detector, not code review, arbitrates the lock discipline.
// Readers hammer macaroonCtx while a writer rewrites the field
// under the same write lock the redial path holds. Run under
// go test -race. Without the detector it still verifies that
// every context carries exactly one non-empty macaroon value.
func TestMacaroonCtxConcurrentRewrite(t *testing.T) {
	c := &Client{macaroonHex: "00"}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 500 {
				md, ok := metadata.FromOutgoingContext(
					c.macaroonCtx())
				if !ok {
					t.Error("context carries no metadata")
					return
				}
				vals := md.Get("macaroon")
				if len(vals) != 1 || vals[0] == "" {
					t.Errorf("macaroon metadata: %q", vals)
					return
				}
			}
		}()
	}
	for i := 0; i < 500; i++ {
		c.mu.Lock()
		c.macaroonHex = fmt.Sprintf("%04x", i+1)
		c.mu.Unlock()
	}
	wg.Wait()
}

// Both fund-moving coin-control call sites route through
// parseOutpoint. Anything that does not parse must be an error,
// never a silently narrowed or retargeted selection.
func TestParseOutpoint(t *testing.T) {
	txid := strings.Repeat("ab", 32)
	op, err := parseOutpoint(txid + ":7")
	if err != nil || op.TxidStr != txid || op.OutputIndex != 7 {
		t.Fatalf("valid outpoint: got (%v, %v)", op, err)
	}
	if _, err := parseOutpoint(txid + ":0"); err != nil {
		t.Errorf("index 0 rejected: %v", err)
	}
	bad := []string{
		"",
		txid,
		txid + ":",
		txid + ":x",
		txid + ":1x2",
		txid + ":-1",
		txid + ":4294967296",
		"ab:0",
		"nothex" + strings.Repeat("a", 58) + ":0",
	}
	for _, s := range bad {
		if _, err := parseOutpoint(s); err == nil {
			t.Errorf("accepted %q", s)
		}
	}
}

// The routing fee limit scales with the amount, half a percent
// with a 30 sat floor, and a fixed cap when the amount is
// unknown (zero-amount invoice).
func TestPaymentFeeLimit(t *testing.T) {
	tests := []struct{ amt, want int64 }{
		{0, 1000},
		{-5, 1000},
		{1, 30},
		{50, 30},
		{6000, 30},
		{6200, 31},
		{200000, 1000},
		{10000000, 50000},
	}
	for _, tt := range tests {
		if got := paymentFeeLimitSat(tt.amt); got != tt.want {
			t.Errorf("paymentFeeLimitSat(%d): got %d, want %d",
				tt.amt, got, tt.want)
		}
	}
}

// The open TUI rebuilds its link to LND only when gRPC itself says the link
// is gone. LND sends its state and authentication answers with code Unknown,
// and a new link cannot help any of them. The lines marked as captured were
// seen on a public Signet node with LND 0.21.4-beta on 2026-10-13. The locked
// wallet and startup lines are LND's words, read in its source. The rest are
// gRPC transport answers or invented examples of the other codes.
func TestReconnectOnlyWhenTheLinkIsLost(t *testing.T) {
	lost := []error{
		status.Error(codes.Unavailable, "connection error: desc = \"transport: Error while dialing: dial tcp 127.0.0.1:10009: connect: connection refused\""),
		// A stale staged certificate fails the TLS handshake. The new link
		// then reaches the repair on connect, which copies it again.
		status.Error(codes.Unavailable, "connection error: desc = \"transport: authentication handshake failed: tls: failed to verify certificate: x509: certificate signed by unknown authority\""),
		fmt.Errorf("list channels: %w", status.Error(codes.Unavailable, "error reading from server: EOF")),
	}
	for _, err := range lost {
		if !linkLost(err) {
			t.Errorf("a lost link did not reconnect: %v", err)
		}
	}
	kept := []error{
		// Captured: a refused macaroon, and none at all.
		status.Error(codes.Unknown, "verification failed: signature mismatch after caveat verification"),
		status.Error(codes.Unknown, "expected 1 macaroon, got 0"),
		// Captured: Bitcoin Core stopped, then loading.
		status.Error(codes.Unknown, `unable to get best block info: invalid http POST response (nil), method: getblockchaininfo, id: 681290, last error=Post "http://127.0.0.1:38332": dial tcp 127.0.0.1:38332: connect: connection refused`),
		status.Error(codes.Unknown, "unable to get best block info: -28: Loading block index…"),
		status.Error(codes.Unknown, "wallet locked, unlock it to enable full RPC access"),
		status.Error(codes.Unknown, "the RPC server is in the process of starting up, but not yet ready to accept calls"),
		status.Error(codes.DeadlineExceeded, "context deadline exceeded"),
		status.Error(codes.Canceled, "context canceled"),
		context.DeadlineExceeded,
		status.Error(codes.InvalidArgument, "invalid payment request"),
		status.Error(codes.Internal, "server closed the stream without sending trailers"),
		// A refused credential does not rebuild an open link. The
		// repair on connect handles it at the next connection.
		status.Error(codes.Unauthenticated, "verification failed"),
		status.Error(codes.PermissionDenied, "permission denied"),
		// Words alone no longer count.
		errors.New("rpc error: code = Unavailable desc = connection refused"),
	}
	for _, err := range kept {
		if linkLost(err) {
			t.Errorf("a working link was rebuilt: %v", err)
		}
	}
}

// The re-stage limiter is process-wide and bounds the heal to
// one request per interval. With the heal keyed on any failed
// test call, this is what keeps a long outage cheap.
func TestRestageDue(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	if !restageDue(time.Time{}, now) {
		t.Error("first request should be due")
	}
	if restageDue(now.Add(-credRestageInterval+time.Second), now) {
		t.Error("due inside the interval")
	}
	if !restageDue(now.Add(-credRestageInterval), now) {
		t.Error("not due at exactly the interval")
	}
	if !restageDue(now.Add(-time.Hour), now) {
		t.Error("not due long after the interval")
	}
}

func TestCancelledReconnectDoesNotReadCredentialsOrRestage(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if client, err := NewContext(ctx); client != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled initialization returned client=%v err=%v", client, err)
	}
	client := &Client{}
	if err := client.dial(ctx, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled dial reached filesystem or RPC work: %v", err)
	}
	credRestageMu.Lock()
	before := credRestageAt
	credRestageMu.Unlock()
	if requestCredentialRestage(ctx) {
		t.Fatal("cancelled reconnect requested credential mutation")
	}
	credRestageMu.Lock()
	after := credRestageAt
	credRestageMu.Unlock()
	if !after.Equal(before) {
		t.Fatal("cancelled reconnect consumed the restage allowance")
	}
}
