package app

import (
	"errors"
	"math"
	"slices"
	"strings"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/lndrpc"
)

type OnChainSendClient interface {
	ListUnspent(int32, int32) ([]lndrpc.UTXO, error)
	SendCoins(lndrpc.SendCoinsRequest) (*lndrpc.SendCoinsResult, error)
}

type OnChainSendInput struct {
	Address     string
	AmountSats  int64
	SendAll     bool
	SatPerVbyte int64
	Label       string
	Outpoints   []string
}

// PreparedOnChainSend owns the reviewed intent, including automatic selection
// when Outpoints is empty. It is not a signed transaction or a single-use token.
// LND's pinned EstimateFee API cannot quote manual-rate or send-all requests;
// no total fee, net Max amount, or change amount is promised here.
type PreparedOnChainSend struct {
	request lndrpc.SendCoinsRequest
	coins   []lndrpc.UTXO
}

func (p PreparedOnChainSend) Request() lndrpc.SendCoinsRequest {
	r := p.request
	r.Outpoints = slices.Clone(r.Outpoints)
	return r
}

func (p PreparedOnChainSend) Coins() []lndrpc.UTXO { return slices.Clone(p.coins) }

func PrepareOnChainSend(network string, input OnChainSendInput, available []lndrpc.UTXO) (PreparedOnChainSend, error) {
	var params *chaincfg.Params
	switch network {
	case config.NetworkMainnet:
		params = &chaincfg.MainNetParams
	case config.NetworkTestnet4:
		params = &chaincfg.TestNet4Params
	case config.NetworkPublicSignet:
		params = &chaincfg.SigNetParams
	default:
		return PreparedOnChainSend{}, errors.New("Unsupported node network profile")
	}
	address := strings.TrimSpace(input.Address)
	decoded, err := btcutil.DecodeAddress(address, params)
	if err != nil || !decoded.IsForNet(params) {
		return PreparedOnChainSend{}, errors.New("Enter a valid address for this node's network")
	}
	if _, bareKey := decoded.(*btcutil.AddressPubKey); bareKey {
		return PreparedOnChainSend{}, errors.New("Sending to a bare public key is unsupported")
	}
	// Keep the existing minimum amount policy. LND checks output-specific dust.
	if !input.SendAll && (input.AmountSats < 546 || input.AmountSats > btcutil.MaxSatoshi) {
		return PreparedOnChainSend{}, errors.New("Enter an amount between 546 sats and the Bitcoin supply limit")
	}
	if input.SatPerVbyte < 1 {
		return PreparedOnChainSend{}, errors.New("Enter a fee rate of at least 1 sat/vB")
	}
	label := strings.TrimSpace(input.Label)
	if len(label) > 500 {
		return PreparedOnChainSend{}, errors.New("Transaction label exceeds 500 bytes")
	}
	coins, err := resolveCoins(input.Outpoints, available)
	if err != nil {
		return PreparedOnChainSend{}, err
	}
	amount := input.AmountSats
	if input.SendAll {
		amount = 0
	}
	return PreparedOnChainSend{
		request: lndrpc.SendCoinsRequest{
			Address: decoded.EncodeAddress(), AmountSats: amount,
			SatPerVbyte: input.SatPerVbyte, SendAll: input.SendAll,
			Label: label, Outpoints: slices.Clone(input.Outpoints),
			MinConfs: 0, SpendUnconfirmed: true,
		},
		coins: coins,
	}, nil
}

type OnChainSendState int

const (
	OnChainNotSubmitted OnChainSendState = iota
	OnChainBroadcast
	OnChainOutcomeUnknown
)

type OnChainSendResult struct {
	State OnChainSendState
	Txid  string
	Err   error
}

// SendOnChain rechecks explicit inputs and makes at most one send RPC. LND
// validates and selects under its own wallet lock; this precheck reserves nothing.
// A lost RPC response cannot prove that no transaction was published.
func SendOnChain(client OnChainSendClient, prepared PreparedOnChainSend) OnChainSendResult {
	if prepared.request.Address == "" {
		return OnChainSendResult{Err: errors.New("Review the transaction before sending")}
	}
	if client == nil {
		return OnChainSendResult{Err: errors.New("LND not connected")}
	}
	req := prepared.Request()
	if len(req.Outpoints) > 0 {
		available, err := client.ListUnspent(req.MinConfs, math.MaxInt32)
		if err == nil {
			_, err = resolveCoins(req.Outpoints, available)
		}
		if err != nil {
			return OnChainSendResult{Err: err}
		}
	}
	result, err := client.SendCoins(req)
	if err != nil {
		return OnChainSendResult{State: OnChainOutcomeUnknown, Err: err}
	}
	if result == nil || result.Txid == "" {
		return OnChainSendResult{State: OnChainOutcomeUnknown, Err: errors.New("LND returned no transaction ID")}
	}
	return OnChainSendResult{State: OnChainBroadcast, Txid: result.Txid}
}
