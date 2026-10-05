package app

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/virtpriv/node/internal/lndrpc"
)

type closeClient struct {
	state    lndrpc.ChannelCloseState
	err      error
	result   lndrpc.ChannelCloseResult
	checked  string
	requests []lndrpc.ChannelCloseRequest
}

func (c *closeClient) ChannelCloseState(point string) (lndrpc.ChannelCloseState, error) {
	c.checked = point
	return c.state, c.err
}
func (c *closeClient) CloseChannel(req lndrpc.ChannelCloseRequest) lndrpc.ChannelCloseResult {
	c.requests = append(c.requests, req)
	return c.result
}
func readyCloseClient() *closeClient {
	return &closeClient{state: lndrpc.ChannelCloseState{Found: true, Active: true, StatusFlags: "ChanStatusDefault"}, result: lndrpc.ChannelCloseResult{Submitted: true, ClosingTxid: strings.Repeat("b", 64)}}
}
func TestClosePreparationAndIntent(t *testing.T) {
	point := strings.Repeat("a", 64) + ":7"
	for _, input := range []ChannelCloseInput{
		{ChannelPoint: point, SatPerVbyte: -1}, {ChannelPoint: point, SatPerVbyte: math.MaxInt64/1000 + 1},
		{ChannelPoint: point, Force: true, SatPerVbyte: 1}, {ChannelPoint: ""}, {ChannelPoint: strings.Repeat("a", 64) + ":4294967296"},
	} {
		if _, err := PrepareChannelClose(input); err == nil {
			t.Fatalf("accepted invalid intent: %+v", input)
		}
	}
	for _, input := range []ChannelCloseInput{{ChannelPoint: point}, {ChannelPoint: point, SatPerVbyte: 12}, {ChannelPoint: point, Force: true}} {
		prepared, err := PrepareChannelClose(input)
		if err != nil {
			t.Fatal(err)
		}
		copy := prepared.Request()
		copy.ChannelPoint = strings.Repeat("c", 64) + ":1"
		copy.Force = !copy.Force
		copy.SatPerVbyte = 999
		c := readyCloseClient()
		result := CloseChannel(c, prepared)
		if result.State != ClosePending || len(c.requests) != 1 || c.checked != point {
			t.Fatalf("lost close identity: %+v", result)
		}
		req := c.requests[0]
		if req.ChannelPoint != input.ChannelPoint || req.Force != input.Force || req.SatPerVbyte != uint64(input.SatPerVbyte) {
			t.Fatalf("changed reviewed intent: %+v", req)
		}
	}
	c := readyCloseClient()
	if result := CloseChannel(c, PreparedChannelClose{}); result.Err == nil || c.checked != "" || len(c.requests) != 0 {
		t.Fatal("unprepared close reached client")
	}
	prepared, _ := PrepareChannelClose(ChannelCloseInput{ChannelPoint: point})
	if result := CloseChannel(nil, prepared); result.State != CloseNotSubmitted || result.Err == nil {
		t.Fatal("nil client was not refused")
	}
}
func TestClosePreflightRefusesStaleOrIneligibleState(t *testing.T) {
	for _, tc := range []struct {
		name           string
		change         func(*closeClient)
		force, allowed bool
	}{
		{"missing", func(c *closeClient) { c.state.Found = false }, false, false},
		{"pending", func(c *closeClient) { c.state.Closing = true }, false, false},
		{"pending force", func(c *closeClient) { c.state.Closing = true }, true, false},
		{"abnormal", func(c *closeClient) { c.state.StatusFlags = "ChanStatusLocalDataLoss" }, true, false},
		{"unknown flags", func(c *closeClient) { c.state.StatusFlags = "" }, false, false},
		{"lookup failed", func(c *closeClient) { c.err = errors.New("state read failed") }, false, false},
		{"offline cooperative", func(c *closeClient) { c.state.Active = false }, false, false},
		{"offline force", func(c *closeClient) { c.state.Active = false }, true, true},
		{"HTLC cooperative", func(c *closeClient) { c.state.PendingHTLCs = 1 }, false, false},
		{"HTLC force", func(c *closeClient) { c.state.PendingHTLCs = 1 }, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := readyCloseClient()
			tc.change(c)
			prepared, _ := PrepareChannelClose(ChannelCloseInput{ChannelPoint: strings.Repeat("a", 64) + ":0", Force: tc.force})
			result := CloseChannel(c, prepared)
			if tc.allowed {
				if len(c.requests) != 1 || c.requests[0].Force != tc.force {
					t.Fatal("lost explicit force intent")
				}
				return
			}
			if len(c.requests) != 0 || result.State != CloseNotSubmitted || result.Err == nil {
				t.Fatalf("ineligible state submitted: %+v", result)
			}
		})
	}
}
func TestCloseOutcomeDoesNotInventFailureOrRecovery(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result lndrpc.ChannelCloseResult
		want   ChannelCloseOutcome
	}{
		{"not attempted", lndrpc.ChannelCloseResult{Err: errors.New("disconnected")}, CloseNotSubmitted},
		{"lost stream", lndrpc.ChannelCloseResult{Submitted: true, Err: errors.New("deadline exceeded")}, CloseOutcomeUnknown},
		{"missing txid", lndrpc.ChannelCloseResult{Submitted: true}, CloseOutcomeUnknown},
		{"candidate", lndrpc.ChannelCloseResult{Submitted: true, ClosingTxid: strings.Repeat("b", 64)}, ClosePending},
		{"confirmed", lndrpc.ChannelCloseResult{Submitted: true, Confirmed: true, ClosingTxid: strings.Repeat("b", 64)}, CloseConfirmed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := readyCloseClient()
			c.result = tc.result
			prepared, _ := PrepareChannelClose(ChannelCloseInput{ChannelPoint: strings.Repeat("a", 64) + ":0"})
			result := CloseChannel(c, prepared)
			if result.State != tc.want || len(c.requests) != 1 {
				t.Fatalf("wrong outcome or retried RPC: %+v", result)
			}
		})
	}
}
