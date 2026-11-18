// internal/bitcoin/client.go

// Package bitcoin talks to the local bitcoind over its JSON-RPC
// interface, authenticating with the node's own staged RPC
// credential (see internal/host/rpcauth.go). Plain HTTP to
// 127.0.0.1 with basic auth, because bitcoind's RPC listens only
// on loopback on this machine, and no privileged operation
// anywhere. The password is read from the staging board, which
// the admin user can read by group.
package bitcoin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/virtpriv/node/internal/board"
	"github.com/virtpriv/node/internal/paths"
)

// RPCUser is the operator RPC identity shared by the client, host provisioning
// and the bitcoin-cli wrapper. LND has its own independent credential.
const RPCUser = "vpn"

type blockchainInfoResponse struct {
	Chain           string `json:"chain"`
	SignetChallenge string `json:"signet_challenge"`
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
// credential never touches argv or the environment. It is read
// from the board and sent as HTTP basic auth.
func rpcCall(rpcPort int, method string,
	params []any, result any) error {
	return rpcCallContext(context.Background(), rpcPort, method, params, result)
}

func rpcCallContext(ctx context.Context, rpcPort int, method string, params []any, result any) error {
	pass, err := board.ReadString(paths.StateBitcoindRPCPass)
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
		// replaced together at install. Disagreement means one
		// of them was changed outside this app.
		return fmt.Errorf(
			"bitcoind rejected the staged RPC credential because " +
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
