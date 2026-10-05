// internal/lndrpc/queries.go

package lndrpc

import (
	"context"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/virtpriv/node/internal/logger"
)

const defaultTimeout = 30 * time.Second

// ── Data types ───────────────────────────────────────────

type NodeInfo struct {
	Pubkey      string
	Alias       string
	Channels    int
	Peers       int
	BlockHeight int
	SyncedChain bool
	SyncedGraph bool
	Version     string
	URIs        []string // LND's advertised node URIs
}

type WalletBalance struct {
	TotalBalance       string
	ConfirmedBalance   string
	UnconfirmedBalance string
}

type Channel struct {
	ChanID         uint64
	ChannelPoint   string // "txid:index"
	RemotePubkey   string
	Capacity       int64
	LocalBalance   int64
	RemoteBalance  int64
	Active         bool
	Private        bool
	Initiator      bool
	PeerAlias      string
	CommitmentType string // "TAPROOT", "ANCHORS", etc.
}

type PendingChannelInfo struct {
	PendingOpen               int
	ForceClose                int
	WaitingClose              int
	PendingOpenChannels       []PendingChannel
	PendingForceCloseChannels []PendingForceCloseChannel
	WaitingCloseChannels      []WaitingCloseChannel
}

type PendingChannel struct {
	ChannelPoint string
	RemotePubkey string
	Capacity     int64
	LocalBalance int64
	PeerAlias    string
}

type PendingForceCloseChannel struct {
	RemotePubkey     string
	ChannelPoint     string
	Capacity         int64
	LocalBalance     int64
	LimboBalance     int64
	RecoveredBalance int64
	ClosingTxid      string
	MaturityHeight   int32
	BlocksRemaining  int32
	PeerAlias        string
}

type WaitingCloseChannel struct {
	RemotePubkey string
	ChannelPoint string
	Capacity     int64
	LocalBalance int64
	LimboBalance int64
	ClosingTxid  string
	PeerAlias    string
}

type ClosedChannel struct {
	ChannelPoint string
	RemotePubkey string
	Capacity     int64
	CloseType    string
	ClosingTxid  string
	PeerAlias    string
	SettledBal   int64
	CloseHeight  int32
}

type OnChainAddress struct {
	Address string
}

// WalletState is LND's live readiness state, exposed without leaking the
// generated protobuf type into the TUI.
type WalletState string

const (
	WalletStateUnknown WalletState = "UNKNOWN"
	WalletStateLocked  WalletState = "LOCKED"
	WalletStateActive  WalletState = "SERVER_ACTIVE"
)

type ChannelOpenRequest struct {
	Pubkey                    string
	AmountSats                int64
	Private, Taproot, FundMax bool
	Outpoints                 []string
	SatPerVbyte               uint64
	MinConfs                  int32
	SpendUnconfirmed          bool
}

// Submitted means the funding RPC was attempted, even if its response was lost.
// It does not imply that LND accepted or broadcast the transaction.
type ChannelOpenResult struct {
	FundingTxID string
	OutputIndex uint32
	Submitted   bool
	Err         error
}

type PeerInfo struct {
	PubKey  string
	Address string
	Inbound bool
	SatSent int64
	SatRecv int64
}

// ── Read queries ─────────────────────────────────────────

func (c *Client) GetInfo() (*NodeInfo, error) {
	return c.GetInfoContext(context.Background())
}

func (c *Client) GetInfoContext(parent context.Context) (*NodeInfo, error) {
	rpc := c.rpc()
	if rpc == nil {
		return nil, errNotConnected
	}
	ctx, cancel := c.callCtxFrom(parent, defaultTimeout)
	defer cancel()

	resp, err := rpc.GetInfo(ctx, &lnrpc.GetInfoRequest{})
	if err != nil {
		c.handleError(err)
		return nil, err
	}
	return &NodeInfo{
		Pubkey:      resp.GetIdentityPubkey(),
		Alias:       resp.GetAlias(),
		Channels:    int(resp.GetNumActiveChannels()),
		Peers:       int(resp.GetNumPeers()),
		BlockHeight: int(resp.GetBlockHeight()),
		SyncedChain: resp.GetSyncedToChain(),
		SyncedGraph: resp.GetSyncedToGraph(),
		Version:     resp.GetVersion(),
		URIs:        resp.GetUris(),
	}, nil
}

// GetState uses LND's state service, which remains available while the wallet
// is locked. This lets status distinguish an intentionally running-but-locked
// daemon from a stopped daemon or a generic RPC outage.
func (c *Client) GetState() (WalletState, error) {
	return c.GetStateContext(context.Background())
}

func (c *Client) GetStateContext(parent context.Context) (WalletState, error) {
	rpc := c.stateRPC()
	if rpc == nil {
		return WalletStateUnknown, errNotConnected
	}
	ctx, cancel := context.WithTimeout(parent, defaultTimeout)
	defer cancel()
	return readWalletSetupState(ctx, rpc)
}

func walletStateName(state lnrpc.WalletState) WalletState {
	switch state {
	case lnrpc.WalletState_LOCKED:
		return WalletStateLocked
	case lnrpc.WalletState_SERVER_ACTIVE:
		return WalletStateActive
	default:
		return WalletState(state.String())
	}
}

func (c *Client) GetWalletBalance() (*WalletBalance, error) {
	return c.GetWalletBalanceContext(context.Background())
}

func (c *Client) GetWalletBalanceContext(parent context.Context) (*WalletBalance, error) {
	rpc := c.rpc()
	if rpc == nil {
		return nil, errNotConnected
	}
	ctx, cancel := c.callCtxFrom(parent, defaultTimeout)
	defer cancel()

	resp, err := rpc.WalletBalance(ctx, &lnrpc.WalletBalanceRequest{})
	if err != nil {
		c.handleError(err)
		return nil, err
	}
	return &WalletBalance{
		TotalBalance:       satStr(resp.GetTotalBalance()),
		ConfirmedBalance:   satStr(resp.GetConfirmedBalance()),
		UnconfirmedBalance: satStr(resp.GetUnconfirmedBalance()),
	}, nil
}

func (c *Client) ListChannels() ([]Channel, error) {
	return c.ListChannelsContext(context.Background())
}

func (c *Client) ListChannelsContext(parent context.Context) ([]Channel, error) {
	rpc := c.rpc()
	if rpc == nil {
		return nil, errNotConnected
	}
	ctx, cancel := c.callCtxFrom(parent, defaultTimeout)
	defer cancel()

	resp, err := rpc.ListChannels(ctx, &lnrpc.ListChannelsRequest{})
	if err != nil {
		c.handleError(err)
		return nil, err
	}

	channels := make([]Channel, 0, len(resp.GetChannels()))
	for _, ch := range resp.GetChannels() {
		channels = append(channels, Channel{
			ChanID:         ch.GetChanId(),
			ChannelPoint:   ch.GetChannelPoint(),
			RemotePubkey:   ch.GetRemotePubkey(),
			Capacity:       ch.GetCapacity(),
			LocalBalance:   ch.GetLocalBalance(),
			RemoteBalance:  ch.GetRemoteBalance(),
			Active:         ch.GetActive(),
			Private:        ch.GetPrivate(),
			Initiator:      ch.GetInitiator(),
			CommitmentType: ch.GetCommitmentType().String(),
		})
	}
	for i := range channels {
		channels[i].PeerAlias = c.getPeerAliasContext(ctx, channels[i].RemotePubkey)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return channels, nil
}

func (c *Client) GetPendingChannels() (*PendingChannelInfo, error) {
	return c.GetPendingChannelsContext(context.Background())
}

func (c *Client) GetPendingChannelsContext(parent context.Context) (*PendingChannelInfo, error) {
	rpc := c.rpc()
	if rpc == nil {
		return nil, errNotConnected
	}
	ctx, cancel := c.callCtxFrom(parent, defaultTimeout)
	defer cancel()

	resp, err := rpc.PendingChannels(ctx, &lnrpc.PendingChannelsRequest{})
	if err != nil {
		c.handleError(err)
		return nil, err
	}

	var pendingChans []PendingChannel
	for _, pc := range resp.GetPendingOpenChannels() {
		ch := pc.GetChannel()
		if ch != nil {
			alias := c.getPeerAliasContext(ctx, ch.GetRemoteNodePub())
			pendingChans = append(pendingChans, PendingChannel{
				ChannelPoint: ch.GetChannelPoint(),
				RemotePubkey: ch.GetRemoteNodePub(),
				Capacity:     ch.GetCapacity(),
				LocalBalance: ch.GetLocalBalance(),
				PeerAlias:    alias,
			})
		}
	}

	var forceCloseChans []PendingForceCloseChannel
	for _, fc := range resp.GetPendingForceClosingChannels() {
		ch := fc.GetChannel()
		if ch != nil {
			alias := c.getPeerAliasContext(ctx,
				ch.GetRemoteNodePub())
			forceCloseChans = append(forceCloseChans,
				PendingForceCloseChannel{
					RemotePubkey:     ch.GetRemoteNodePub(),
					ChannelPoint:     ch.GetChannelPoint(),
					Capacity:         ch.GetCapacity(),
					LocalBalance:     ch.GetLocalBalance(),
					LimboBalance:     fc.GetLimboBalance(),
					RecoveredBalance: fc.GetRecoveredBalance(),
					ClosingTxid:      fc.GetClosingTxid(),
					MaturityHeight:   int32(fc.GetMaturityHeight()),
					BlocksRemaining:  fc.GetBlocksTilMaturity(),
					PeerAlias:        alias,
				})
		}
	}

	var waitingCloseChans []WaitingCloseChannel
	for _, wc := range resp.GetWaitingCloseChannels() {
		ch := wc.GetChannel()
		if ch != nil {
			alias := c.getPeerAliasContext(ctx,
				ch.GetRemoteNodePub())
			waitingCloseChans = append(
				waitingCloseChans,
				WaitingCloseChannel{
					RemotePubkey: ch.GetRemoteNodePub(),
					ChannelPoint: ch.GetChannelPoint(),
					Capacity:     ch.GetCapacity(),
					LocalBalance: ch.GetLocalBalance(),
					LimboBalance: wc.GetLimboBalance(),
					ClosingTxid:  wc.GetClosingTxid(),
					PeerAlias:    alias,
				})
		}
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return &PendingChannelInfo{
		PendingOpen:               len(resp.GetPendingOpenChannels()),
		ForceClose:                len(resp.GetPendingForceClosingChannels()),
		WaitingClose:              len(resp.GetWaitingCloseChannels()),
		PendingOpenChannels:       pendingChans,
		PendingForceCloseChannels: forceCloseChans,
		WaitingCloseChannels:      waitingCloseChans,
	}, nil
}

// ListClosedChannels returns all historically closed
// channels with close type and peer info.
func (c *Client) ListClosedChannels() (
	[]ClosedChannel, error,
) {
	return c.ListClosedChannelsContext(context.Background())
}

func (c *Client) ListClosedChannelsContext(parent context.Context) ([]ClosedChannel, error) {
	rpc := c.rpc()
	if rpc == nil {
		return nil, errNotConnected
	}
	ctx, cancel := c.callCtxFrom(parent, defaultTimeout)
	defer cancel()

	resp, err := rpc.ClosedChannels(ctx,
		&lnrpc.ClosedChannelsRequest{})
	if err != nil {
		c.handleError(err)
		return nil, err
	}

	var channels []ClosedChannel
	for _, ch := range resp.GetChannels() {
		closeType := "unknown"
		switch ch.GetCloseType() {
		case lnrpc.ChannelCloseSummary_COOPERATIVE_CLOSE:
			closeType = "cooperative"
		case lnrpc.ChannelCloseSummary_LOCAL_FORCE_CLOSE:
			closeType = "force"
		case lnrpc.ChannelCloseSummary_REMOTE_FORCE_CLOSE:
			closeType = "force"
		case lnrpc.ChannelCloseSummary_BREACH_CLOSE:
			closeType = "breach"
		case lnrpc.ChannelCloseSummary_FUNDING_CANCELED:
			closeType = "canceled"
		case lnrpc.ChannelCloseSummary_ABANDONED:
			closeType = "abandoned"
		}

		alias := c.getPeerAliasContext(ctx, ch.GetRemotePubkey())
		if alias == "" {
			pk := ch.GetRemotePubkey()
			if len(pk) > 12 {
				alias = pk[:12] + ".."
			} else {
				alias = pk
			}
		}

		channels = append(channels, ClosedChannel{
			ChannelPoint: ch.GetChannelPoint(),
			RemotePubkey: ch.GetRemotePubkey(),
			Capacity:     ch.GetCapacity(),
			CloseType:    closeType,
			ClosingTxid:  ch.GetClosingTxHash(),
			PeerAlias:    alias,
			SettledBal:   ch.GetSettledBalance(),
			CloseHeight:  int32(ch.GetCloseHeight()),
		})
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return channels, nil
}

func (c *Client) GetNewAddress() (*OnChainAddress, error) {
	rpc := c.rpc()
	if rpc == nil {
		return nil, errNotConnected
	}
	ctx, cancel := c.callCtx(defaultTimeout)
	defer cancel()

	resp, err := rpc.NewAddress(ctx, &lnrpc.NewAddressRequest{
		Type: lnrpc.AddressType_TAPROOT_PUBKEY,
	})
	if err != nil {
		c.handleError(err)
		return nil, err
	}
	return &OnChainAddress{Address: resp.GetAddress()}, nil
}

// ListPeers returns currently connected peers.
func (c *Client) ListPeers() ([]PeerInfo, error) {
	rpc := c.rpc()
	if rpc == nil {
		return nil, errNotConnected
	}
	ctx, cancel := c.callCtx(defaultTimeout)
	defer cancel()

	resp, err := rpc.ListPeers(ctx, &lnrpc.ListPeersRequest{})
	if err != nil {
		c.handleError(err)
		return nil, err
	}
	var peers []PeerInfo
	for _, p := range resp.GetPeers() {
		peers = append(peers, PeerInfo{
			PubKey:  p.GetPubKey(),
			Address: p.GetAddress(),
			Inbound: p.GetInbound(),
			SatSent: p.GetSatSent(),
			SatRecv: p.GetSatRecv(),
		})
	}
	return peers, nil
}

// ── Channel operations (fund-moving) ─────────────────────

// ConnectPeer connects to a Lightning peer. Uses perm=true for
// persistent connection. Does not fail if already connected.
func (c *Client) ConnectPeer(pubkey, host string) error {
	rpc := c.rpc()
	if rpc == nil {
		return errNotConnected
	}
	ctx, cancel := c.callCtx(60 * time.Second)
	defer cancel()

	_, err := rpc.ConnectPeer(ctx, &lnrpc.ConnectPeerRequest{
		Addr: &lnrpc.LightningAddress{
			Pubkey: pubkey,
			Host:   host,
		},
		Perm: true,
	})
	if err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "already connected") {
			return nil
		}
		logger.Status("LND peer connect warning: %v", err)
		return err
	}
	return nil
}

// WaitForPeer polls ListPeers until the pubkey appears. The timeout limits
// starting new polls, not lock waits or an in-flight RPC. The last poll can
// succeed after the interval, so this is not a total wall-clock bound.
func (c *Client) WaitForPeer(pubkey string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		peers, err := c.ListPeers()
		if err == nil {
			for _, p := range peers {
				if p.PubKey == pubkey {
					return nil
				}
			}
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("peer did not connect within %s", timeout)
}

// OpenChannel submits one funding request. The explicit outpoints restrict LND's
// funding pool; LND determines which coins to use and whether change is needed.
func (c *Client) OpenChannel(input ChannelOpenRequest) ChannelOpenResult {
	rpc := c.rpc()
	if rpc == nil {
		return ChannelOpenResult{Err: errNotConnected}
	}
	req, err := buildOpenChannelRequest(input)
	if err != nil {
		return ChannelOpenResult{Err: err}
	}
	ctx, cancel := c.callCtx(120 * time.Second)
	defer cancel()
	resp, err := rpc.OpenChannelSync(ctx, req)
	result := ChannelOpenResult{Submitted: true, Err: err}
	if err != nil {
		c.handleError(err)
		return result
	}
	txidBytes := resp.GetFundingTxidBytes()
	if len(txidBytes) != 32 {
		result.Err = fmt.Errorf("LND returned an invalid funding transaction ID")
		return result
	}
	reversed := make([]byte, 32)
	for i := range reversed {
		reversed[i] = txidBytes[31-i]
	}
	result.FundingTxID = hex.EncodeToString(reversed)
	result.OutputIndex = resp.GetOutputIndex()
	return result
}

func buildOpenChannelRequest(input ChannelOpenRequest) (*lnrpc.OpenChannelRequest, error) {
	if input.Taproot && !input.Private {
		return nil, fmt.Errorf("taproot channels must be private")
	}
	pubkey, err := hex.DecodeString(input.Pubkey)
	if err != nil || len(pubkey) != 33 {
		return nil, fmt.Errorf("invalid node public key")
	}
	if len(input.Outpoints) == 0 {
		return nil, fmt.Errorf("channel funding requires explicit coin selection")
	}
	if input.FundMax && input.AmountSats != 0 {
		return nil, fmt.Errorf("fund-max request must not carry a fixed amount")
	}
	req := &lnrpc.OpenChannelRequest{
		NodePubkey: pubkey, LocalFundingAmount: input.AmountSats,
		Private: input.Private, MinConfs: input.MinConfs,
		SpendUnconfirmed: input.SpendUnconfirmed, ScidAlias: input.Private,
		FundMax: input.FundMax, SatPerVbyte: input.SatPerVbyte,
	}
	if input.Taproot {
		req.CommitmentType = lnrpc.CommitmentType_TAPROOT
	}
	// Reject the entire selection if any outpoint is malformed or duplicated.
	seen := make(map[string]bool, len(input.Outpoints))
	for _, op := range input.Outpoints {
		outpoint, err := parseOutpoint(op)
		if err != nil {
			return nil, err
		}
		identity := fmt.Sprintf("%s:%d", strings.ToLower(outpoint.TxidStr), outpoint.OutputIndex)
		if seen[identity] {
			return nil, fmt.Errorf("duplicate selected outpoint: %s", op)
		}
		seen[identity] = true
		req.Outpoints = append(req.Outpoints, outpoint)
	}
	return req, nil
}

// ── Internal helpers ─────────────────────────────────────

func (c *Client) getPeerAlias(pubkey string) string {
	return c.getPeerAliasContext(context.Background(), pubkey)
}

func (c *Client) getPeerAliasContext(parent context.Context, pubkey string) string {
	rpc := c.rpc()
	if rpc == nil {
		return ""
	}
	ctx, cancel := c.callCtxFrom(parent, 3*time.Second)
	defer cancel()

	resp, err := rpc.GetNodeInfo(ctx, &lnrpc.NodeInfoRequest{
		PubKey:          pubkey,
		IncludeChannels: false,
	})
	if err != nil {
		return ""
	}
	if resp.GetNode() != nil {
		return resp.GetNode().GetAlias()
	}
	return ""
}

// parseOutpoint parses a txid:index outpoint string strictly.
// Every fund-moving call site that names an outpoint (channel
// funding, on-chain send with coin control, channel close)
// routes through here: a value that does not parse must abort
// the operation rather than silently narrow or retarget what
// the operator chose. Pure — unit-tested.
func parseOutpoint(op string) (*lnrpc.OutPoint, error) {
	parts := strings.SplitN(op, ":", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid outpoint %q", op)
	}
	txidRaw, err := hex.DecodeString(parts[0])
	if err != nil || len(txidRaw) != 32 {
		return nil, fmt.Errorf("invalid outpoint txid in %q", op)
	}
	idx, err := strconv.ParseUint(parts[1], 10, 32)
	if err != nil {
		return nil, fmt.Errorf("invalid outpoint index in %q", op)
	}
	return &lnrpc.OutPoint{
		TxidStr:     parts[0],
		OutputIndex: uint32(idx),
	}, nil
}

func (c *Client) handleError(err error) {
	switch classifyRPCError(err.Error()) {
	case rpcErrTransport:
		logger.Status("LND connection lost, will reconnect: %v",
			err)
		go c.Reconnect()
	case rpcErrCredential:
		// Reconnecting is only useful when the dial-time
		// repair could actually re-stage the credentials —
		// otherwise the dial would rebuild the same connection
		// from the same board copies and fail the same way. A
		// credential LND rejects PERMANENTLY (not stale — e.g.
		// a wallet recreated behind this process's back) would
		// otherwise produce a reconnect and a log line for
		// every failing call on every poll tick, forever. The
		// re-stage limiter is the damper: one repair attempt
		// per interval, quiet in between.
		if credRestageWorthwhile() {
			logger.Status("LND rejected the held credentials, "+
				"will reconnect and repair: %v", err)
			go c.Reconnect()
		}
	}
}

// RPC error classes, by what would help:
//
//   - transport: the connection itself is gone (Unavailable,
//     connection refused) — LND restarted or is down; rebuild
//     the connection and the next dial finds out.
//   - credential: LND answered but rejected the caller
//     (PermissionDenied, Unauthenticated) — the fail-closed
//     moment of a stale staged macaroon. Treating this as
//     terminal left that staleness permanent; routing it
//     through Reconnect (damped, above) lands it at the
//     dial-time repair, which re-stages both LND credentials
//     and retries once.
//   - other: in-progress states (deadline, starting up, not
//     yet ready) and application answers over a working
//     connection — nothing a reconnect can help.
type rpcErrClass int

const (
	rpcErrOther rpcErrClass = iota
	rpcErrTransport
	rpcErrCredential
)

// classifyRPCError uses text heuristics to choose the current reconnect policy.
// This is a local policy choice; gRPC also exposes typed status codes.
func classifyRPCError(errStr string) rpcErrClass {
	if strings.Contains(errStr, "DeadlineExceeded") ||
		strings.Contains(errStr, "context deadline") {
		return rpcErrOther
	}
	if strings.Contains(errStr, "starting up") ||
		strings.Contains(errStr, "not yet ready") {
		return rpcErrOther
	}
	if strings.Contains(errStr, "Unavailable") ||
		strings.Contains(errStr, "connection refused") {
		return rpcErrTransport
	}
	if strings.Contains(errStr, "PermissionDenied") ||
		strings.Contains(errStr, "Unauthenticated") {
		return rpcErrCredential
	}
	return rpcErrOther
}

func satStr(sats int64) string {
	return fmt.Sprintf("%d", sats)
}

var errNotConnected = fmt.Errorf("LND gRPC not connected")
