package app

import (
	"errors"
	"fmt"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/lndrpc"
)

type OnChainReceiveClient interface {
	GetNewAddress() (*lndrpc.OnChainAddress, error)
}

// OnChainReceiveAddress is a validated P2TR address returned by LND.
type OnChainReceiveAddress struct {
	text string
}

func (a OnChainReceiveAddress) Text() string { return a.text }

// CreateOnChainAddress requests once. An error does not prove LND failed to
// derive an address; an explicit retry requests another, without retiring any
// previously issued address. Network encoding cannot distinguish signet from
// testnet4, so the installed daemon remains the authority for chain identity.
func CreateOnChainAddress(client OnChainReceiveClient, network, previous string) (OnChainReceiveAddress, error) {
	var params *chaincfg.Params
	switch network {
	case config.NetworkMainnet:
		params = &chaincfg.MainNetParams
	case config.NetworkTestnet4:
		params = &chaincfg.TestNet4Params
	case config.NetworkPublicSignet:
		params = &chaincfg.SigNetParams
	default:
		return OnChainReceiveAddress{}, errors.New("unsupported node network profile")
	}
	if client == nil {
		return OnChainReceiveAddress{}, errors.New("LND not connected")
	}
	result, err := client.GetNewAddress()
	if err != nil {
		return OnChainReceiveAddress{}, fmt.Errorf("address request not confirmed: %w", err)
	}
	if result == nil || result.Address == "" {
		return OnChainReceiveAddress{}, errors.New("LND returned no address")
	}
	decoded, err := btcutil.DecodeAddress(result.Address, params)
	if err != nil {
		return OnChainReceiveAddress{}, errors.New("LND returned an invalid address")
	}
	address, ok := decoded.(*btcutil.AddressTaproot)
	if !ok || !address.IsForNet(params) {
		return OnChainReceiveAddress{}, errors.New("LND returned an unexpected address type or network")
	}
	text := address.EncodeAddress()
	if text == previous {
		return OnChainReceiveAddress{}, errors.New("LND returned the previous address instead of a new one")
	}
	return OnChainReceiveAddress{text: text}, nil
}
