package app

import (
	"errors"
	"math"

	"github.com/virtpriv/node/internal/lndrpc"
)

type ChannelCloseClient interface {
	ChannelCloseState(string) (lndrpc.ChannelCloseState, error)
	CloseChannel(lndrpc.ChannelCloseRequest) lndrpc.ChannelCloseResult
}

type ChannelCloseInput struct {
	ChannelPoint string
	Force        bool
	SatPerVbyte  int64
}

// PreparedChannelClose owns the channel, mode and fee policy shown for approval.
type PreparedChannelClose struct{ request lndrpc.ChannelCloseRequest }

func (p PreparedChannelClose) Request() lndrpc.ChannelCloseRequest { return p.request }

func PrepareChannelClose(input ChannelCloseInput) (PreparedChannelClose, error) {
	point, err := lndrpc.NormalizeChannelPoint(input.ChannelPoint)
	if err != nil {
		return PreparedChannelClose{}, err
	}
	if input.SatPerVbyte < 0 || input.SatPerVbyte > math.MaxInt64/1000 || (input.Force && input.SatPerVbyte != 0) {
		return PreparedChannelClose{}, errors.New("Invalid close fee policy")
	}
	return PreparedChannelClose{request: lndrpc.ChannelCloseRequest{
		ChannelPoint: point,
		Force:        input.Force, SatPerVbyte: uint64(input.SatPerVbyte),
	}}, nil
}

type ChannelCloseOutcome int

const (
	CloseNotSubmitted ChannelCloseOutcome = iota
	ClosePending
	CloseConfirmed
	CloseOutcomeUnknown
)

type ChannelCloseResult struct {
	State ChannelCloseOutcome
	Txid  string
	Err   error
}

// CloseChannel rechecks the initial-close state before one RPC. Other operators
// and peers can still change LND state between the reads and submission.
func CloseChannel(client ChannelCloseClient, prepared PreparedChannelClose) ChannelCloseResult {
	req := prepared.Request()
	if req.ChannelPoint == "" {
		return ChannelCloseResult{Err: errors.New("Review the channel before closing")}
	}
	if client == nil {
		return ChannelCloseResult{Err: errors.New("LND not connected")}
	}
	state, err := client.ChannelCloseState(req.ChannelPoint)
	if err != nil {
		return ChannelCloseResult{Err: err}
	}
	if state.Closing || !state.Found || state.StatusFlags != "ChanStatusDefault" {
		return ChannelCloseResult{Err: errors.New("Channel is unavailable or already closing. Check pending channels and history")}
	}
	if !req.Force && (!state.Active || state.PendingHTLCs != 0) {
		return ChannelCloseResult{Err: errors.New("Cooperative close requires an active peer and no pending HTLCs. Review channel state")}
	}
	result := client.CloseChannel(req)
	if !result.Submitted {
		return ChannelCloseResult{Err: result.Err}
	}
	if result.Err != nil || result.ClosingTxid == "" {
		err := result.Err
		if err == nil {
			err = errors.New("LND returned no closing transaction ID")
		}
		return ChannelCloseResult{State: CloseOutcomeUnknown, Err: err}
	}
	outcome := ClosePending
	if result.Confirmed {
		outcome = CloseConfirmed
	}
	return ChannelCloseResult{State: outcome, Txid: result.ClosingTxid}
}
