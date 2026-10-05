// internal/lndrpc/client.go

// Package lndrpc provides a gRPC client for LND.
//
// Ordinary credential reads use root-staged files readable by the admin user.
// Initialization and reconnect repair may ask the helper to refresh those files.
// The client holds the macaroon in memory and injects it into authenticated RPCs;
// reconnect can replace the loaded credentials.
//
// Connections use TLS to the loopback address. Close shuts down the current
// transport; it does not provide secure credential erasure.
//
// Read queries and fund-moving RPCs share this one client;
// every fund-moving call sits behind an explicit confirmation
// flow in its caller.
package lndrpc

import (
	"context"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"

	"github.com/lightningnetwork/lnd/lnrpc"

	"github.com/virtpriv/node/internal/helper"
	"github.com/virtpriv/node/internal/logger"
	"github.com/virtpriv/node/internal/paths"
)

// Client wraps an LND gRPC connection with macaroon authentication.
type Client struct {
	conn        *grpc.ClientConn
	lightning   lnrpc.LightningClient
	state       lnrpc.StateClient
	macaroonHex string
	mu          sync.RWMutex
}

// NewContext initializes an existing wallet's client with the caller's lifetime.
// An unavailable GetInfo response retains the transport for normal queries;
// unreadable staged credentials and cancellation return no client. Startup uses
// the existing probe and rate-limited repair policy, not the creation path.
func NewContext(ctx context.Context) (*Client, error) {
	c := &Client{}
	c.mu.Lock()
	err := c.dial(ctx, true)
	c.mu.Unlock()
	if err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// dial establishes the connection; the caller holds c.mu.
//
// allowHeal enables the staged-credential self-heal: LND can
// regenerate its TLS certificate OUTSIDE any helper operation
// (an automatic restart after a crash, a reboot, or a typed
// configuration change), and its macaroon can be
// recreated the same way — and the staged board copies then no
// longer match what LND accepts. That staleness reliably
// surfaces exactly here, as a failed test call. The heal keys
// on the ABSENCE OF SUCCESS, not on recognizing the failure's
// text: enumerating the failure messages worth healing is a
// whitelist, and a whitelist misses (a dial error this client
// once produced was worded in a way no pattern anticipated).
// On any test-call failure, the client asks the helper to
// re-stage the LND credentials — rate-limited process-wide, so
// failures with nothing to heal (LND down, wallet locked) cost
// at most one re-stage per interval — and dials again ONCE
// with the refreshed board copies.
func (c *Client) dial(parent context.Context, allowHeal bool) error {
	if err := parent.Err(); err != nil {
		return err
	}
	if err := c.loadStaged(); err != nil {
		return err
	}
	conn := c.conn

	// Test the connection with a longer timeout.
	// During IBD, LND's GetInfo queries Bitcoin Core which can
	// be slow. The probe context is built inline rather than
	// through macaroonCtx: dial runs under the write lock and
	// macaroonCtx takes the read lock.
	md := metadata.New(map[string]string{"macaroon": c.macaroonHex})
	probeCtx := metadata.NewOutgoingContext(parent, md)
	ctx, cancel := context.WithTimeout(probeCtx, 30*time.Second)
	defer cancel()

	_, err := c.lightning.GetInfo(ctx, &lnrpc.GetInfoRequest{})
	if err == nil {
		logger.Status("LND gRPC connected and ready")
		return nil
	}
	if parent.Err() != nil {
		conn.Close()
		c.conn, c.lightning, c.state = nil, nil, nil
		return parent.Err()
	}
	if allowHeal && requestCredentialRestage(parent) {
		logger.Status("LND gRPC test call failed (%v) — staged "+
			"credentials re-staged in case they were stale, "+
			"reconnecting once", err)
		c.conn.Close()
		c.conn = nil
		c.lightning = nil
		c.state = nil
		return c.dial(parent, false)
	}
	if parent.Err() != nil {
		conn.Close()
		c.conn, c.lightning, c.state = nil, nil, nil
		return parent.Err()
	}
	logger.Status("LND gRPC connected, waiting for RPC ready: %v", err)
	return nil
}

// Credential self-heal state: at most one re-stage request per
// interval, PROCESS-WIDE — with the heal keyed on any failed
// test call, this limiter is what keeps a long LND outage from
// turning every reconnect poll into helper traffic and journal
// noise.
var (
	credRestageMu sync.Mutex
	credRestageAt time.Time
)

const credRestageInterval = 5 * time.Minute

// restageDue reports whether enough time has passed since the
// last re-stage request for another one. Pure — unit-tested.
func restageDue(last, now time.Time) bool {
	return last.IsZero() || now.Sub(last) >= credRestageInterval
}

// credRestageWorthwhile reports whether a re-stage request made
// now would pass the limiter, WITHOUT consuming it. The
// credential-rejection path in handleError checks this before
// reconnecting: when no repair can happen, no reconnect can
// help, and staying quiet caps a permanently rejected
// credential at one reconnect-and-repair attempt (and one log
// line) per limiter interval instead of one per failing call.
func credRestageWorthwhile() bool {
	credRestageMu.Lock()
	defer credRestageMu.Unlock()
	return restageDue(credRestageAt, time.Now())
}

// requestCredentialRestage asks the helper to refresh the staged
// LND credentials from current reality. Reports whether a
// re-stage actually happened (rate limit passed and the helper
// succeeded), so the caller only re-dials when the board copies
// could have changed.
func requestCredentialRestage(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	credRestageMu.Lock()
	defer credRestageMu.Unlock()
	if !restageDue(credRestageAt, time.Now()) {
		return false
	}
	credRestageAt = time.Now()
	session, err := helper.StartContext(ctx, helper.VerbStageLNDCredentials, nil)
	if err == nil {
		err = session.Wait(nil)
	}
	if err != nil {
		logger.Status("stage-lnd-credentials: %v", err)
		return false
	}
	return true
}

// Reconnect attempts to re-establish the gRPC connection.
// Called when an RPC fails, indicating LND may have restarted.
func (c *Client) Reconnect() {
	c.ReconnectContext(context.Background())
}

// ReconnectContext observes the caller's lifetime for the connection probe and
// credential restaging. A cancelled caller cannot schedule a later reconnect.
func (c *Client) ReconnectContext(ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ctx.Err() != nil {
		return
	}
	if c.conn != nil {
		c.conn.Close()
	}
	c.conn, c.lightning, c.state = nil, nil, nil
	if err := c.dial(ctx, true); err != nil {
		logger.Status("LND gRPC reconnect failed: %v", err)
	}
}

// Close shuts down the current gRPC transport. It does not cancel or retire
// queued background reconnects, which may establish a later connection.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
		c.lightning = nil
		c.state = nil
	}
}

// macaroonCtx returns a context with the macaroon injected as
// gRPC metadata. The field is read under the lock — Reconnect
// rewrites it on another goroutine.
func (c *Client) macaroonCtx() context.Context {
	return c.macaroonCtxFrom(context.Background())
}

// callCtx returns a context with macaroon and a timeout.
func (c *Client) callCtx(timeout time.Duration) (context.Context, context.CancelFunc) {
	return c.callCtxFrom(context.Background(), timeout)
}

// callCtxFrom keeps the caller's cancellation when attaching credentials.
func (c *Client) callCtxFrom(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(c.macaroonCtxFrom(parent), timeout)
}

func (c *Client) macaroonCtxFrom(parent context.Context) context.Context {
	c.mu.RLock()
	macHex := c.macaroonHex
	c.mu.RUnlock()
	md, _ := metadata.FromOutgoingContext(parent)
	if md == nil {
		md = metadata.MD{}
	}
	md.Set("macaroon", macHex)
	return metadata.NewOutgoingContext(parent, md)
}

// rpc returns the Lightning client, or nil if not connected.
func (c *Client) rpc() lnrpc.LightningClient {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lightning
}

func (c *Client) stateRPC() lnrpc.StateClient {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state
}

// NewStagedClient opens a lazy transport from credentials already staged by the
// wallet workflow. It does no GetInfo probe or repair and does not claim RPC
// readiness. Normal queries observe readiness using the existing client policy.
func NewStagedClient() (*Client, error) {
	c := &Client{}
	if err := c.loadStaged(); err != nil {
		return nil, err
	}
	return c, nil
}

func stagedTLSCredentials() (credentials.TransportCredentials, error) {
	certData, err := helper.ReadBoard(paths.StateLNDTLSCert)
	if err != nil {
		return nil, fmt.Errorf("read TLS cert: %w", err)
	}

	certPool := x509.NewCertPool()
	if !certPool.AppendCertsFromPEM(certData) {
		return nil, fmt.Errorf("failed to parse TLS cert")
	}

	return credentials.NewClientTLSFromCert(certPool, ""), nil
}

func (c *Client) loadStaged() error {
	tlsCreds, err := stagedTLSCredentials()
	if err != nil {
		return err
	}

	// Read the staged admin macaroon copy (staged at wallet
	// creation; re-staged whenever an operation invalidates it).
	macBytes, err := helper.ReadBoard(paths.StateLNDMacaroon)
	if err != nil {
		return fmt.Errorf("read macaroon: %w", err)
	}
	c.macaroonHex = hex.EncodeToString(macBytes)

	// Preserve the normal client's receive limit and conservative keepalive.
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(tlsCreds),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(50 * 1024 * 1024)),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:    time.Minute,
			Timeout: 20 * time.Second,
		}),
	}

	// Match LND's configured loopback listener without hostname resolution.
	conn, err := grpc.NewClient(paths.LNDGRPCEndpoint, opts...)
	if err != nil {
		return fmt.Errorf("grpc connect: %w", err)
	}

	c.conn = conn
	c.lightning = lnrpc.NewLightningClient(conn)
	c.state = lnrpc.NewStateClient(conn)

	return nil
}
