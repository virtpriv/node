package app

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/virtpriv/node/internal/lndrpc"
)

type ChannelOpenClient interface {
	ConnectPeer(string, string) error
	WaitForPeer(string, time.Duration) error
	ListUnspent(int32, int32) ([]lndrpc.UTXO, error)
	OpenChannel(lndrpc.ChannelOpenRequest) lndrpc.ChannelOpenResult
}

type ChannelOpenInput struct {
	Pubkey, Host              string
	AmountSats                int64
	FundMax, Private, Taproot bool
	SatPerVbyte               int64
	Outpoints                 []string
}

// PreparedChannelOpen owns the reviewed request. Selected coins are the allowed
// funding pool; LND can use a subset and determines fees, capacity and change.
// Preparation does not reserve coins or construct a funding transaction.
type PreparedChannelOpen struct {
	request lndrpc.ChannelOpenRequest
	host    string
	total   int64
}

func (p PreparedChannelOpen) Request() lndrpc.ChannelOpenRequest {
	r := p.request
	r.Outpoints = slices.Clone(r.Outpoints)
	return r
}

func (p PreparedChannelOpen) SelectedTotal() int64 { return p.total }

func ValidateChannelPeer(pubkey, host string) (string, string, error) {
	pubkey, host = strings.TrimSpace(pubkey), strings.TrimSpace(host)
	raw, err := hex.DecodeString(pubkey)
	if err != nil || len(raw) != 33 {
		return "", "", errors.New("Enter a compressed node public key (66 hex characters)")
	}
	key, err := btcec.ParsePubKey(raw)
	if err != nil {
		return "", "", errors.New("Enter a valid node public key")
	}
	name, port, err := net.SplitHostPort(host)
	n, portErr := strconv.Atoi(port)
	if err != nil || name == "" || strings.ContainsAny(name, " \t\r\n") || portErr != nil || n < 1 || n > 65535 {
		return "", "", errors.New("Enter a peer host and port (host:port)")
	}
	return hex.EncodeToString(key.SerializeCompressed()), host, nil
}

func ValidateChannelAmount(amount int64) error {
	if amount < 20000 || amount > 1000000000 {
		return errors.New("Enter a channel size between 20,000 and 1,000,000,000 sats")
	}
	return nil
}

func PrepareChannelOpen(input ChannelOpenInput, available []lndrpc.UTXO) (PreparedChannelOpen, error) {
	pubkey, host, err := ValidateChannelPeer(input.Pubkey, input.Host)
	if err != nil {
		return PreparedChannelOpen{}, err
	}
	if input.Taproot && !input.Private {
		return PreparedChannelOpen{}, errors.New("Taproot channels must be private")
	}
	// Zero retains the existing automatic fee policy. Bound conversion to sat/kvB.
	if input.SatPerVbyte < 0 || input.SatPerVbyte > math.MaxInt64/1000 {
		return PreparedChannelOpen{}, errors.New("Enter a valid fee rate")
	}
	if len(input.Outpoints) == 0 {
		return PreparedChannelOpen{}, errors.New("Select UTXOs in Coin control first")
	}
	coins, err := resolveCoins(input.Outpoints, available)
	if err != nil {
		return PreparedChannelOpen{}, err
	}
	var total int64
	for _, coin := range coins {
		if coin.AmountSats <= 0 || coin.AmountSats > btcutil.MaxSatoshi-total {
			return PreparedChannelOpen{}, errors.New("Invalid selected coin value")
		}
		total += coin.AmountSats
	}
	amount := input.AmountSats
	if input.FundMax {
		amount = 0
		if total < 20000 {
			return PreparedChannelOpen{}, errors.New("Select at least 20,000 sats; LND must also cover fees and reserves")
		}
	} else {
		if err := ValidateChannelAmount(amount); err != nil {
			return PreparedChannelOpen{}, err
		}
		if amount >= total {
			return PreparedChannelOpen{}, errors.New("Selected coins must cover the channel amount plus fees; use Max to let LND determine capacity")
		}
	}
	return PreparedChannelOpen{
		host: host, total: total,
		request: lndrpc.ChannelOpenRequest{
			Pubkey: pubkey, AmountSats: amount, FundMax: input.FundMax,
			Private: input.Private, Taproot: input.Taproot,
			SatPerVbyte: uint64(input.SatPerVbyte),
			Outpoints:   slices.Clone(input.Outpoints), MinConfs: 0, SpendUnconfirmed: true,
		},
	}, nil
}

type ChannelOpenState int

const (
	ChannelNotSubmitted ChannelOpenState = iota
	ChannelBroadcast
	ChannelOutcomeUnknown
)

type ChannelOpenResult struct {
	State       ChannelOpenState
	Txid        string
	OutputIndex uint32
	Err         error
}

// OpenChannel checks peer readiness and selected availability before one funding
// call. Preflight reserves nothing; LND checks coins under its wallet lock.
func OpenChannel(client ChannelOpenClient, prepared PreparedChannelOpen) ChannelOpenResult {
	if prepared.request.Pubkey == "" || len(prepared.request.Outpoints) == 0 {
		return ChannelOpenResult{Err: errors.New("Review the channel before opening")}
	}
	if client == nil {
		return ChannelOpenResult{Err: errors.New("LND not connected")}
	}
	req := prepared.Request()
	// Persistent connection requests may return before the peer connects.
	connectErr := client.ConnectPeer(req.Pubkey, prepared.host)
	if err := client.WaitForPeer(req.Pubkey, 60*time.Second); err != nil {
		if connectErr != nil {
			err = fmt.Errorf("%v; peer readiness: %w", connectErr, err)
		}
		return ChannelOpenResult{Err: fmt.Errorf("Could not connect: %w", err)}
	}
	available, err := client.ListUnspent(req.MinConfs, math.MaxInt32)
	if err == nil {
		_, err = resolveCoins(req.Outpoints, available)
	}
	if err != nil {
		return ChannelOpenResult{Err: err}
	}
	result := client.OpenChannel(req)
	if !result.Submitted {
		return ChannelOpenResult{Err: result.Err}
	}
	if result.Err != nil || result.FundingTxID == "" {
		err := result.Err
		if err == nil {
			err = errors.New("LND returned no funding transaction ID")
		}
		return ChannelOpenResult{State: ChannelOutcomeUnknown, Err: err}
	}
	return ChannelOpenResult{State: ChannelBroadcast, Txid: result.FundingTxID, OutputIndex: result.OutputIndex}
}
