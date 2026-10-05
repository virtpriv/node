package app

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/virtpriv/node/internal/lndrpc"
)

type channelClient struct {
	coins                        []lndrpc.UTXO
	calls                        []string
	sent                         []lndrpc.ChannelOpenRequest
	connectErr, waitErr, listErr error
	wait                         func()
	result                       lndrpc.ChannelOpenResult
}

func (c *channelClient) ConnectPeer(string, string) error {
	c.calls = append(c.calls, "connect")
	return c.connectErr
}
func (c *channelClient) WaitForPeer(string, time.Duration) error {
	c.calls = append(c.calls, "wait")
	if c.wait != nil {
		c.wait()
	}
	return c.waitErr
}
func (c *channelClient) ListUnspent(min, max int32) ([]lndrpc.UTXO, error) {
	c.calls = append(c.calls, "coins")
	if min != 0 || max != 2147483647 {
		return nil, errors.New("wrong confirmation policy")
	}
	return c.coins, c.listErr
}
func (c *channelClient) OpenChannel(req lndrpc.ChannelOpenRequest) lndrpc.ChannelOpenResult {
	c.calls = append(c.calls, "fund")
	c.sent = append(c.sent, req)
	return c.result
}

func channelInput() (ChannelOpenInput, []lndrpc.UTXO) {
	coin := lndrpc.UTXO{Txid: strings.Repeat("a", 64), AmountSats: 50000}
	return ChannelOpenInput{
		Pubkey: "0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798",
		Host:   "peer.onion:9735", AmountSats: 30000, Private: true, Taproot: true,
		SatPerVbyte: 9, Outpoints: []string{coin.Txid + ":0"},
	}, []lndrpc.UTXO{coin}
}

func TestChannelPreparationRejectsInvalidIntent(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*ChannelOpenInput)
	}{
		{"empty selection", func(i *ChannelOpenInput) { i.Outpoints = nil }},
		{"missing coin", func(i *ChannelOpenInput) { i.Outpoints[0] = strings.Repeat("b", 64) + ":0" }},
		{"duplicate coin", func(i *ChannelOpenInput) { i.Outpoints = append(i.Outpoints, i.Outpoints[0]) }},
		{"invalid curve point", func(i *ChannelOpenInput) { i.Pubkey = "02" + strings.Repeat("ff", 32) }},
		{"invalid host", func(i *ChannelOpenInput) { i.Host = "peer.onion" }},
		{"public Taproot", func(i *ChannelOpenInput) { i.Private = false }},
		{"below minimum", func(i *ChannelOpenInput) { i.AmountSats = 19999 }},
		{"above maximum", func(i *ChannelOpenInput) { i.AmountSats = 1000000001 }},
		{"no room for fees", func(i *ChannelOpenInput) { i.AmountSats = 50000 }},
		{"negative fee", func(i *ChannelOpenInput) { i.SatPerVbyte = -1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input, coins := channelInput()
			tc.edit(&input)
			if _, err := PrepareChannelOpen(input, coins); err == nil {
				t.Fatal("invalid intent accepted")
			}
		})
	}
}

func TestChannelPreparedRequestIsOwnedAndMaxIsAnIntent(t *testing.T) {
	for _, max := range []bool{false, true} {
		input, coins := channelInput()
		input.FundMax = max
		if max {
			input.SatPerVbyte = 0
		}
		prepared, err := PrepareChannelOpen(input, coins)
		if err != nil {
			t.Fatal(err)
		}
		want := prepared.Request()
		input.Outpoints[0] = "edited"
		copy := prepared.Request()
		copy.Outpoints[0] = "edited accessor"
		coins[0].AmountSats = 1
		if !reflect.DeepEqual(prepared.Request(), want) || prepared.SelectedTotal() != 50000 {
			t.Fatal("prepared intent aliases mutable data")
		}
		if max && (want.AmountSats != 0 || !want.FundMax || want.SatPerVbyte != 0) {
			t.Fatal("Max or automatic fee was replaced by a guessed value")
		}
	}
}

func TestChannelSubmissionPreflightAndOutcome(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*channelClient)
		state ChannelOpenState
		calls []string
	}{
		{"broadcast", func(c *channelClient) {}, ChannelBroadcast, []string{"connect", "wait", "coins", "fund"}},
		{"already connected despite connect error", func(c *channelClient) { c.connectErr = errors.New("already connected") }, ChannelBroadcast, []string{"connect", "wait", "coins", "fund"}},
		{"peer unavailable", func(c *channelClient) { c.waitErr = errors.New("peer timeout") }, ChannelNotSubmitted, []string{"connect", "wait"}},
		{"coin query fails", func(c *channelClient) { c.listErr = errors.New("wallet locked") }, ChannelNotSubmitted, []string{"connect", "wait", "coins"}},
		{"coin disappears while connecting", func(c *channelClient) { c.wait = func() { c.coins = nil } }, ChannelNotSubmitted, []string{"connect", "wait", "coins"}},
		{"local adapter refusal", func(c *channelClient) { c.result = lndrpc.ChannelOpenResult{Err: errors.New("not connected")} }, ChannelNotSubmitted, []string{"connect", "wait", "coins", "fund"}},
		{"response lost", func(c *channelClient) {
			c.result = lndrpc.ChannelOpenResult{Submitted: true, Err: errors.New("deadline exceeded")}
		}, ChannelOutcomeUnknown, []string{"connect", "wait", "coins", "fund"}},
		{"no funding ID", func(c *channelClient) { c.result = lndrpc.ChannelOpenResult{Submitted: true} }, ChannelOutcomeUnknown, []string{"connect", "wait", "coins", "fund"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input, coins := channelInput()
			p, err := PrepareChannelOpen(input, coins)
			if err != nil {
				t.Fatal(err)
			}
			c := &channelClient{coins: coins, result: lndrpc.ChannelOpenResult{Submitted: true, FundingTxID: strings.Repeat("c", 64), OutputIndex: 7}}
			tc.setup(c)
			result := OpenChannel(c, p)
			if result.State != tc.state || !reflect.DeepEqual(c.calls, tc.calls) {
				t.Fatalf("result=%+v calls=%v", result, c.calls)
			}
			if len(c.sent) > 1 {
				t.Fatal("funding was retried")
			}
			if len(c.sent) == 1 && !reflect.DeepEqual(c.sent[0], p.Request()) {
				t.Fatal("submission differs from review")
			}
			if tc.state != ChannelBroadcast && result.Err == nil {
				t.Fatal("failure lost its diagnostic")
			}
			if tc.state == ChannelBroadcast && (result.Txid == "" || result.OutputIndex != 7) {
				t.Fatal("lost channel point")
			}
		})
	}
	c := &channelClient{}
	if r := OpenChannel(c, PreparedChannelOpen{}); r.State != ChannelNotSubmitted || r.Err == nil || len(c.calls) != 0 {
		t.Fatal("unprepared request started work")
	}
}
