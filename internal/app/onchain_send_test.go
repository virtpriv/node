package app

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/lndrpc"
)

type onChainClient struct {
	coins            []lndrpc.UTXO
	listErr, sendErr error
	sent             []lndrpc.SendCoinsRequest
	result           *lndrpc.SendCoinsResult
	minConfs         int32
}

func (c *onChainClient) ListUnspent(min, max int32) ([]lndrpc.UTXO, error) {
	c.minConfs = min
	return c.coins, c.listErr
}

func (c *onChainClient) SendCoins(req lndrpc.SendCoinsRequest) (*lndrpc.SendCoinsResult, error) {
	c.sent = append(c.sent, req)
	return c.result, c.sendErr
}

func sendInput(t *testing.T) OnChainSendInput {
	t.Helper()
	addr, err := btcutil.NewAddressWitnessPubKeyHash(make([]byte, 20), &chaincfg.MainNetParams)
	if err != nil {
		t.Fatal(err)
	}
	return OnChainSendInput{Address: addr.EncodeAddress(), AmountSats: 1000, SatPerVbyte: 9, Label: "reviewed"}
}

func TestCoinSelectionPreservesIdentityAcrossRefresh(t *testing.T) {
	a := lndrpc.UTXO{Txid: strings.Repeat("a", 64), AmountSats: 1000}
	b := lndrpc.UTXO{Txid: strings.Repeat("b", 64), AmountSats: 2000}
	c := lndrpc.UTXO{Txid: strings.Repeat("c", 64), AmountSats: 3000}
	var selection CoinSelection
	selection.Toggle(b)
	for _, coins := range [][]lndrpc.UTXO{{a, b}, {b, a}, {c, a, b}} {
		total, err := selection.Total(coins)
		if err != nil || total != 2000 || !selection.Contains(b) || selection.Contains(c) {
			t.Fatalf("refresh changed selected coin: total=%d err=%v", total, err)
		}
	}
	for _, coins := range [][]lndrpc.UTXO{{a, c}, nil} {
		if _, err := selection.Total(coins); err == nil || selection.Len() != 1 {
			t.Fatal("missing selection silently became automatic selection")
		}
	}
	copy := selection.Outpoints()
	copy[0] = coinOutpoint(c)
	if !selection.Contains(b) {
		t.Fatal("caller mutated selected identity")
	}
	selection.Clear()
	if total, err := selection.Total(nil); err != nil || total != 0 || selection.Len() != 0 {
		t.Fatal("explicit clear did not restore empty selection")
	}
}

func TestOnChainPreparationRejectsInvalidIntent(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*OnChainSendInput)
	}{
		{"checksum", func(i *OnChainSendInput) { i.Address += "x" }},
		{"amount", func(i *OnChainSendInput) { i.AmountSats = 545 }},
		{"fee", func(i *OnChainSendInput) { i.SatPerVbyte = -1 }},
		{"supply", func(i *OnChainSendInput) { i.AmountSats = btcutil.MaxSatoshi + 1 }},
		{"label", func(i *OnChainSendInput) { i.Label = strings.Repeat("a", 501) }},
		{"missing coin", func(i *OnChainSendInput) { i.Outpoints = []string{strings.Repeat("a", 64) + ":0"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := sendInput(t)
			tc.edit(&input)
			prepared, err := PrepareOnChainSend(config.NetworkMainnet, input, nil)
			if err == nil {
				t.Fatal("invalid intent accepted")
			}
			client := &onChainClient{}
			if result := SendOnChain(client, prepared); result.State != OnChainNotSubmitted || len(client.sent) != 0 {
				t.Fatal("invalid preparation authorized submission")
			}
		})
	}
	for _, network := range []string{config.NetworkPublicSignet, config.NetworkTestnet4, "signet"} {
		if _, err := PrepareOnChainSend(network, sendInput(t), nil); err == nil {
			t.Fatal("foreign address or unknown profile accepted")
		}
	}
	for _, network := range []string{config.NetworkPublicSignet, config.NetworkTestnet4} {
		input := sendInput(t)
		address, _ := btcutil.NewAddressTaproot(make([]byte, 32), &chaincfg.SigNetParams)
		input.Address = address.EncodeAddress()
		if _, err := PrepareOnChainSend(network, input, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := PrepareOnChainSend(config.NetworkMainnet, input, nil); err == nil {
			t.Fatal("test-network address accepted for mainnet")
		}
	}
}

func TestOnChainSubmissionOwnsReviewedRequest(t *testing.T) {
	coin := lndrpc.UTXO{Txid: strings.Repeat("a", 64), AmountSats: 10000, Confirmations: 0}
	for _, sendAll := range []bool{false, true} {
		input := sendInput(t)
		input.SendAll = sendAll
		input.Outpoints = []string{coinOutpoint(coin)}
		prepared, err := PrepareOnChainSend(config.NetworkMainnet, input, []lndrpc.UTXO{coin})
		if err != nil {
			t.Fatal(err)
		}
		reviewed := prepared.Request()
		input.Outpoints[0] = "replaced"
		copy := prepared.Request()
		copy.Outpoints[0] = "replaced again"
		coins := prepared.Coins()
		coins[0].AmountSats = 1
		client := &onChainClient{coins: []lndrpc.UTXO{coin}, result: &lndrpc.SendCoinsResult{Txid: "tx"}}
		result := SendOnChain(client, prepared)
		if result.State != OnChainBroadcast || len(client.sent) != 1 || !reflect.DeepEqual(client.sent[0], reviewed) {
			t.Fatalf("review and submission differ: %+v %+v", reviewed, client.sent)
		}
		if client.minConfs != 0 || !reviewed.SpendUnconfirmed || (sendAll && reviewed.AmountSats != 0) {
			t.Fatal("unconfirmed or Max semantics changed")
		}
		if prepared.Coins()[0].AmountSats != 10000 {
			t.Fatal("review metadata mutated")
		}
	}
}

func TestOnChainPreflightAndUnknownOutcomesNeverRetryOrWidenSelection(t *testing.T) {
	coin := lndrpc.UTXO{Txid: strings.Repeat("a", 64), AmountSats: 10000}
	input := sendInput(t)
	input.Outpoints = []string{coinOutpoint(coin)}
	prepared, err := PrepareOnChainSend(config.NetworkMainnet, input, []lndrpc.UTXO{coin})
	if err != nil {
		t.Fatal(err)
	}
	for _, client := range []*onChainClient{{}, {listErr: errors.New("wallet unavailable")}} {
		if result := SendOnChain(client, prepared); result.State != OnChainNotSubmitted || result.Err == nil || len(client.sent) != 0 {
			t.Fatal("failed preflight sent a transaction")
		}
	}
	for _, client := range []*onChainClient{
		{coins: []lndrpc.UTXO{coin}, sendErr: errors.New("response lost")},
		{coins: []lndrpc.UTXO{coin}},
		{coins: []lndrpc.UTXO{coin}, result: &lndrpc.SendCoinsResult{}},
	} {
		if result := SendOnChain(client, prepared); result.State != OnChainOutcomeUnknown || result.Err == nil || len(client.sent) != 1 {
			t.Fatal("ambiguous response became success, definite failure, or a retry")
		}
	}
	input.Outpoints = nil
	prepared, err = PrepareOnChainSend(config.NetworkMainnet, input, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &onChainClient{result: &lndrpc.SendCoinsResult{Txid: "auto"}}
	if result := SendOnChain(client, prepared); result.State != OnChainBroadcast || len(client.sent[0].Outpoints) != 0 {
		t.Fatal("intentional automatic selection no longer works")
	}
}
