package app

import (
	"errors"
	"testing"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/lndrpc"
)

type addressClient struct {
	result *lndrpc.OnChainAddress
	err    error
	calls  int
}

func (c *addressClient) GetNewAddress() (*lndrpc.OnChainAddress, error) {
	c.calls++
	return c.result, c.err
}

func TestCreateOnChainAddressValidatesDaemonResults(t *testing.T) {
	main, err := btcutil.NewAddressTaproot(make([]byte, 32), &chaincfg.MainNetParams)
	if err != nil {
		t.Fatal(err)
	}
	test, err := btcutil.NewAddressTaproot(make([]byte, 32), &chaincfg.TestNet4Params)
	if err != nil {
		t.Fatal(err)
	}
	witness, err := btcutil.NewAddressWitnessPubKeyHash(make([]byte, 20), &chaincfg.MainNetParams)
	if err != nil {
		t.Fatal(err)
	}
	lost := errors.New("response lost")
	for _, tc := range []struct {
		name, network, address, previous string
		err                              error
		nilResult, wantOK                bool
	}{
		{name: "mainnet", network: config.NetworkMainnet, address: main.EncodeAddress(), wantOK: true},
		{name: "testnet4", network: config.NetworkTestnet4, address: test.EncodeAddress(), wantOK: true},
		{name: "signet", network: config.NetworkPublicSignet, address: test.EncodeAddress(), wantOK: true},
		{name: "wrong network", network: config.NetworkMainnet, address: test.EncodeAddress()},
		{name: "wrong type", network: config.NetworkMainnet, address: witness.EncodeAddress()},
		{name: "malformed", network: config.NetworkMainnet, address: "bc1p-invalid"},
		{name: "whitespace", network: config.NetworkMainnet, address: main.EncodeAddress() + "\n"},
		{name: "empty", network: config.NetworkMainnet},
		{name: "nil", network: config.NetworkMainnet, nilResult: true},
		{name: "unchanged", network: config.NetworkMainnet, address: main.EncodeAddress(), previous: main.EncodeAddress()},
		{name: "lost response", network: config.NetworkMainnet, address: main.EncodeAddress(), err: lost},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &addressClient{result: &lndrpc.OnChainAddress{Address: tc.address}, err: tc.err}
			if tc.nilResult {
				client.result = nil
			}
			result, err := CreateOnChainAddress(client, tc.network, tc.previous)
			if (err == nil) != tc.wantOK || client.calls != 1 {
				t.Fatalf("result=%q err=%v calls=%d", result.Text(), err, client.calls)
			}
			if tc.wantOK && result.Text() != tc.address || !tc.wantOK && result.Text() != "" {
				t.Fatalf("unexpected usable address %q", result.Text())
			}
			if tc.err != nil && !errors.Is(err, tc.err) {
				t.Fatal("underlying failure was lost")
			}
		})
	}
	client := &addressClient{}
	if _, err := CreateOnChainAddress(client, "regtest", ""); err == nil || client.calls != 0 {
		t.Fatal("unsupported profile reached LND")
	}
	if _, err := CreateOnChainAddress(nil, config.NetworkMainnet, ""); err == nil {
		t.Fatal("missing client was accepted")
	}
}
