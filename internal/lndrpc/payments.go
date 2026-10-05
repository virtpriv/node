package lndrpc

import (
	"context"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lnrpc/routerrpc"

	"github.com/virtpriv/node/internal/logger"
)

// SendPaymentResult holds the outcome of a payment attempt.
type SendPaymentResult struct {
	Preimage string
	FeeSats  int64
	Status   string
	Hops     []RouteHop
	Error    string
}

// paymentFeeLimitSat derives the routing fee limit from the
// payment amount: half a percent, with a 30 sat floor so small
// payments still route. A flat limit is wrong in both
// directions — it authorizes an enormous relative fee on a tiny
// payment and starves a large one of viable routes. Zero-amount
// invoices (amount chosen by the sender at pay time is unknown
// here) fall back to a fixed cap. The specific constants are a
// first pass and may be tuned. Pure — unit-tested.
func paymentFeeLimitSat(amountSats int64) int64 {
	if amountSats <= 0 {
		return 1000
	}
	limit := amountSats / 200
	if limit < 30 {
		limit = 30
	}
	return limit
}

// SendPayment sends a payment using SendPaymentV2 (streaming).
// Blocks until the payment succeeds, fails, or times out.
// Returns route hops on success for visualization.
//
// If the update stream drops before a terminal status, the
// payment may still be running inside LND, and the one wrong
// answer is reporting failure — an operator who retries a
// payment that later settles has paid twice. The stream is
// re-attached by payment hash instead, and if no terminal
// status arrives before the deadline the result says IN_FLIGHT
// with an instruction to check history before retrying.
func (c *Client) SendPayment(payReq string) (*SendPaymentResult, error) {
	if c.rpc() == nil {
		return nil, errNotConnected
	}
	c.mu.RLock()
	conn := c.conn
	c.mu.RUnlock()

	if conn == nil {
		return nil, errNotConnected
	}

	routerClient := routerrpc.NewRouterClient(conn)

	// Decode first: the amount drives the fee limit and the
	// payment hash is the re-attach handle if the stream dies.
	feeLimit := int64(1000)
	var payHash []byte
	if decoded, err := c.DecodePayReq(payReq); err == nil {
		feeLimit = paymentFeeLimitSat(decoded.AmountSats)
		if raw, err := hex.DecodeString(
			decoded.PaymentHash); err == nil && len(raw) == 32 {
			payHash = raw
		}
	}

	ctx, cancel := context.WithTimeout(c.macaroonCtx(), 120*time.Second)
	defer cancel()

	stream, err := routerClient.SendPaymentV2(ctx,
		&routerrpc.SendPaymentRequest{
			PaymentRequest:    payReq,
			TimeoutSeconds:    60,
			FeeLimitSat:       feeLimit,
			MaxParts:          16,
			NoInflightUpdates: true,
		})
	if err != nil {
		return nil, fmt.Errorf("send payment: %w", err)
	}

	for {
		payment, err := stream.Recv()
		if err != nil {
			return c.recoverPaymentOutcome(ctx, payHash, err)
		}

		switch payment.GetStatus() {
		case lnrpc.Payment_SUCCEEDED:
			return c.successResult(payment), nil

		case lnrpc.Payment_FAILED:
			reason := payment.GetFailureReason().String()
			logger.TUI("Payment failed: %s", reason)
			return &SendPaymentResult{
				Status: "FAILED",
				Error:  reason,
			}, nil

		case lnrpc.Payment_IN_FLIGHT:
			continue

		default:
			continue
		}
	}
}

// successResult builds the success outcome, extracting the
// route from the settled HTLC for visualization.
func (c *Client) successResult(payment *lnrpc.Payment) *SendPaymentResult {
	result := &SendPaymentResult{
		Preimage: payment.GetPaymentPreimage(),
		FeeSats:  payment.GetFeeSat(),
		Status:   "SUCCEEDED",
	}
	for _, htlc := range payment.GetHtlcs() {
		if htlc.GetStatus() == lnrpc.HTLCAttempt_SUCCEEDED &&
			htlc.GetRoute() != nil {
			for _, hop := range htlc.GetRoute().GetHops() {
				result.Hops = append(result.Hops, RouteHop{
					PubKey:   hop.GetPubKey(),
					ChanID:   hop.GetChanId(),
					FeeSats:  hop.GetFeeMsat() / 1000,
					AmtToFwd: hop.GetAmtToForwardMsat() / 1000,
				})
			}
			break
		}
	}
	for i := range result.Hops {
		result.Hops[i].Alias = c.getPeerAlias(
			result.Hops[i].PubKey)
	}
	logger.TUI("Payment succeeded: fee=%d sats, hops=%d",
		result.FeeSats, len(result.Hops))
	return result
}

// recoverPaymentOutcome re-attaches to a payment whose update
// stream died without a terminal status. It never invents an
// outcome: it returns the tracked terminal status, or an
// explicit IN_FLIGHT result when the deadline passes first.
// Without a payment hash to track by (the decode failed), the
// stream error is surfaced as-is.
func (c *Client) recoverPaymentOutcome(
	ctx context.Context, payHash []byte, streamErr error,
) (*SendPaymentResult, error) {
	if len(payHash) == 0 {
		return nil, fmt.Errorf("payment stream: %w", streamErr)
	}
	logger.TUI("Payment stream dropped (%v) — tracking by hash",
		streamErr)

	for ctx.Err() == nil {
		c.mu.RLock()
		conn := c.conn
		c.mu.RUnlock()
		if conn == nil {
			break
		}
		stream, err := routerrpc.NewRouterClient(conn).
			TrackPaymentV2(ctx, &routerrpc.TrackPaymentRequest{
				PaymentHash:       payHash,
				NoInflightUpdates: true,
			})
		if err == nil {
			for {
				payment, err := stream.Recv()
				if err != nil {
					break
				}
				switch payment.GetStatus() {
				case lnrpc.Payment_SUCCEEDED:
					return c.successResult(payment), nil
				case lnrpc.Payment_FAILED:
					return &SendPaymentResult{
						Status: "FAILED",
						Error: payment.
							GetFailureReason().String(),
					}, nil
				}
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}

	logger.TUI("Payment outcome unresolved before deadline — " +
		"reported in flight")
	return &SendPaymentResult{
		Status: "IN_FLIGHT",
		Error: "Payment still in flight. Check Payment History " +
			"before retrying: sending again could pay twice.",
	}, nil
}
