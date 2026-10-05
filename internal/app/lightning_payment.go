// Package app owns node workflows independently of terminal presentation.
package app

import (
	"errors"
	"fmt"
	"strings"

	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/lndrpc"
)

// LightningPaymentClient is the daemon access needed to prepare and pay an
// invoice. LND retains responsibility for routing and recovering stream outcomes.
type LightningPaymentClient interface {
	DecodePayReq(string) (*lndrpc.DecodedPayReq, error)
	SendPayment(string) (*lndrpc.SendPaymentResult, error)
}

// PaymentRequest contains an invoice that passed the installed network prefilter.
// LND must still decode and validate the invoice; a prefix is not proof of validity.
type PaymentRequest struct {
	invoice string
}

func (r PaymentRequest) Invoice() string { return r.invoice }

// ParseLightningPayment normalizes pasted input and applies the node's network
// policy before any daemon call. It does not authorize sending a payment.
func ParseLightningPayment(network, raw string) (PaymentRequest, error) {
	if strings.TrimSpace(raw) == "" {
		return PaymentRequest{}, errors.New("Paste a payment request")
	}
	invoice := strings.NewReplacer("[", "", "]", "", "\"", "", "'", "").Replace(raw)
	invoice = strings.TrimSpace(invoice)
	invoice = strings.TrimPrefix(invoice, "lightning:")
	invoice = strings.TrimPrefix(invoice, "LIGHTNING:")
	profile, err := config.NetworkConfigFromName(network)
	if err != nil {
		return PaymentRequest{}, errors.New("Unsupported node network profile")
	}
	if !profile.AcceptsInvoicePrefix(invoice) {
		return PaymentRequest{}, fmt.Errorf("Invoice is not for %s", profile.DisplayName)
	}
	return PaymentRequest{invoice: invoice}, nil
}

// PreparedPayment keeps the decoded details and their exact invoice together.
// Its fields are private so editing a form or a details copy cannot change what
// will be submitted after confirmation.
type PreparedPayment struct {
	request PaymentRequest
	details lndrpc.DecodedPayReq
}

func (p PreparedPayment) Details() lndrpc.DecodedPayReq { return p.details }

func PrepareLightningPayment(client LightningPaymentClient, request PaymentRequest) (PreparedPayment, error) {
	if request.invoice == "" {
		return PreparedPayment{}, errors.New("Paste a payment request")
	}
	if client == nil {
		return PreparedPayment{}, errors.New("LND not connected")
	}
	decoded, err := client.DecodePayReq(request.invoice)
	if err != nil {
		return PreparedPayment{}, err
	}
	if decoded == nil {
		return PreparedPayment{}, errors.New("LND returned no invoice details")
	}
	if decoded.IsExpired {
		return PreparedPayment{}, errors.New("This invoice has expired")
	}
	return PreparedPayment{request: request, details: *decoded}, nil
}

// SendLightningPayment submits a prepared invoice with at most one daemon call.
// It does not retry or reinterpret the outcome. Callers own confirmation and
// duplicate-action prevention; PreparedPayment is not a single-use token.
// LND revalidates the invoice at execution and retains its existing fee policy.
func SendLightningPayment(client LightningPaymentClient, payment PreparedPayment) (*lndrpc.SendPaymentResult, error) {
	if payment.request.invoice == "" {
		return nil, errors.New("Prepare the invoice before sending")
	}
	if client == nil {
		return nil, errors.New("LND not connected")
	}
	result, err := client.SendPayment(payment.request.invoice)
	if err == nil && result == nil {
		return nil, errors.New("LND returned no payment outcome; check Payment History before retrying")
	}
	return result, err
}
