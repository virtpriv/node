// internal/bitcoin/client.go

// Package bitcoin talks to the local bitcoind over its JSON-RPC
// interface, authenticating with the node's own staged RPC
// credential (see internal/host/rpcauth.go). Plain HTTP to
// 127.0.0.1 with basic auth — bitcoind's RPC is loopback-only
// on this box — and no privileged operation anywhere: the
// password is read from the staging board, which the admin
// user can read by group.
package bitcoin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/virtualprivatenode/vpn/internal/helper"
	"github.com/virtualprivatenode/vpn/internal/paths"
)

// RPCUser is the operator RPC identity shared by the client, host provisioning
// and the bitcoin-cli wrapper. LND has its own independent credential.
const RPCUser = "vpn"

type BlockchainInfo struct {
	Blocks   int
	Headers  int
	Progress float64
	Synced   bool
	// SizeOnDisk is bitcoind's own measure of its data
	// footprint in bytes — which is why no privileged
	// du of the data dir is needed for the status screen.
	SizeOnDisk int64
}

type blockchainInfoResponse struct {
	Chain                string  `json:"chain"`
	Blocks               int     `json:"blocks"`
	Headers              int     `json:"headers"`
	VerificationProgress float64 `json:"verificationprogress"`
	InitialBlockDownload bool    `json:"initialblockdownload"`
	SizeOnDisk           int64   `json:"size_on_disk"`
	SignetChallenge      string  `json:"signet_challenge"`
}

// BlockchainIdentity is the minimum live evidence needed to prove that the
// running Core instance matches the immutable installed profile. Chain alone
// is insufficient for signet because public and custom signets both report
// "signet" and share the genesis block.
type BlockchainIdentity struct {
	Chain           string
	Genesis         string
	SignetChallenge string
}

func GetBlockchainIdentity(rpcPort int) (BlockchainIdentity, error) {
	var info blockchainInfoResponse
	if err := rpcCall(rpcPort, "getblockchaininfo", nil, &info); err != nil {
		return BlockchainIdentity{}, err
	}
	var genesis string
	if err := rpcCall(rpcPort, "getblockhash", []any{0}, &genesis); err != nil {
		return BlockchainIdentity{}, err
	}
	return BlockchainIdentity{
		Chain:           info.Chain,
		Genesis:         genesis,
		SignetChallenge: info.SignetChallenge,
	}, nil
}

// GetSubversion identifies the running daemon through its authenticated RPC.
func GetSubversion(rpcPort int) (string, error) {
	var reply struct {
		Subversion string `json:"subversion"`
	}
	if err := rpcCall(rpcPort, "getnetworkinfo", nil, &reply); err != nil {
		return "", err
	}
	return reply.Subversion, nil
}

// rpcCall performs one JSON-RPC call against loopback. The
// credential never touches argv or the environment: it is read
// from the board and sent as HTTP basic auth.
func rpcCall(rpcPort int, method string,
	params []any, result any) error {
	return rpcCallContext(context.Background(), rpcPort, method, params, result)
}

func rpcCallContext(ctx context.Context, rpcPort int, method string, params []any, result any) error {
	pass, err := helper.ReadBoardString(paths.StateBitcoindRPCPass)
	if err != nil {
		return err
	}
	if params == nil {
		params = []any{}
	}
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "1.0",
		"id":      "vpn",
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d/", rpcPort),
		bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.SetBasicAuth(RPCUser, pass)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("bitcoind unreachable: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		// The staged password and the conf's rpcauth line are
		// replaced together at install; disagreement means one
		// of them was changed outside this app.
		return fmt.Errorf(
			"bitcoind rejected the staged RPC credential — " +
				"bitcoin.conf and the staged password disagree")
	case http.StatusForbidden:
		return fmt.Errorf(
			"bitcoind denied method %s for user %s", method, RPCUser)
	}
	var rpcResp struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return fmt.Errorf("decode %s response: %w", method, err)
	}
	if rpcResp.Error != nil {
		return fmt.Errorf("%s: rpc error %d: %s",
			method, rpcResp.Error.Code, rpcResp.Error.Message)
	}
	if result != nil {
		if err := json.Unmarshal(rpcResp.Result, result); err != nil {
			return fmt.Errorf("decode %s result: %w", method, err)
		}
	}
	return nil
}

// GetBlockchainInfo reports read failures separately from a valid empty chain.
func GetBlockchainInfo(ctx context.Context, rpcPort int) (BlockchainInfo, error) {
	return readBlockchainInfo(func(result any) error {
		return rpcCallContext(ctx, rpcPort, "getblockchaininfo", nil, result)
	})
}

func readBlockchainInfo(read func(any) error) (BlockchainInfo, error) {
	var resp blockchainInfoResponse
	if err := read(&resp); err != nil {
		return BlockchainInfo{}, err
	}
	return BlockchainInfo{
		Blocks: resp.Blocks, Headers: resp.Headers,
		Progress: resp.VerificationProgress, Synced: !resp.InitialBlockDownload,
		SizeOnDisk: resp.SizeOnDisk,
	}, nil
}

// FeeEstimate retains Core's returned target, which may differ from the request.
type FeeEstimate struct {
	Blocks   int
	SatPerVB float64
}

// EstimateSmartFee keeps Core's default estimate mode and the existing 1 sat/vB
// minimum. The result is a rate suggestion, not a transaction fee quote.
func EstimateSmartFee(ctx context.Context, rpcPort, target int) (FeeEstimate, error) {
	return readFeeEstimate(target, func(result any) error {
		return rpcCallContext(ctx, rpcPort, "estimatesmartfee", []any{target}, result)
	})
}

func readFeeEstimate(target int, read func(any) error) (FeeEstimate, error) {
	var resp struct {
		FeeRate float64  `json:"feerate"`
		Blocks  int      `json:"blocks"`
		Errors  []string `json:"errors"`
	}
	if err := read(&resp); err != nil {
		return FeeEstimate{}, err
	}
	if resp.FeeRate <= 0 || len(resp.Errors) > 0 {
		return FeeEstimate{}, fmt.Errorf("no estimate for target %d (%s)",
			target, strings.Join(resp.Errors, "; "))
	}
	if resp.Blocks < 2 || resp.Blocks > 1008 {
		return FeeEstimate{}, fmt.Errorf("invalid fee estimate target %d", resp.Blocks)
	}
	// bitcoind returns BTC/kvB; sat/vB = BTC/kvB × 1e8 / 1000.
	satPerVB := resp.FeeRate * 100000
	if satPerVB < 1 {
		satPerVB = 1
	}
	return FeeEstimate{Blocks: resp.Blocks, SatPerVB: satPerVB}, nil
}

func FormatProgress(progress float64) string {
	return fmt.Sprintf("%.2f%%", progress*100)
}

// FormatSize renders a byte count the way the status screen
// shows data-directory sizes ("12.3 GB").
func FormatSize(bytes int64) string {
	const gb = 1 << 30
	const mb = 1 << 20
	switch {
	case bytes >= gb:
		return fmt.Sprintf("%.1f GB", float64(bytes)/gb)
	case bytes >= mb:
		return fmt.Sprintf("%.0f MB", float64(bytes)/mb)
	case bytes > 0:
		return fmt.Sprintf("%d B", bytes)
	default:
		return "N/A"
	}
}
