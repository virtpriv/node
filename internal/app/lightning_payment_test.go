package app

import (
	"errors"
	"testing"

	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/lndrpc"
)

type paymentClient struct {
	decoded         *lndrpc.DecodedPayReq
	decodeErr       error
	decodedRequests []string
	sentRequests    []string
}

func (c *paymentClient) DecodePayReq(request string) (*lndrpc.DecodedPayReq, error) {
	c.decodedRequests = append(c.decodedRequests, request)
	return c.decoded, c.decodeErr
}

func (c *paymentClient) SendPayment(request string) (*lndrpc.SendPaymentResult, error) {
	c.sentRequests = append(c.sentRequests, request)
	return nil, nil
}

// Installed-network selection is exercised through the TUI's existing
// profile test; the complete prefix matrix belongs to config's network tests.
func TestPaymentRequestNormalizationAndInvalidInput(t *testing.T) {
	for _, raw := range []string{"lnbc1example", "  [\"lightning:lnbc1example\"]  ", "'LIGHTNING:lnbc1example'"} {
		request, err := ParseLightningPayment(config.NetworkMainnet, raw)
		if err != nil || request.Invoice() != "lnbc1example" {
			t.Fatalf("input %q: request=%q err=%v", raw, request.Invoice(), err)
		}
	}
	for _, tc := range []struct{ network, raw string }{
		{config.NetworkMainnet, " \n "},
		{"unsupported", "lnbc1example"},
		{config.NetworkMainnet, "not-an-invoice"},
	} {
		if _, err := ParseLightningPayment(tc.network, tc.raw); err == nil {
			t.Fatalf("accepted invalid request: %+v", tc)
		}
	}
}

func TestPaymentPreparationRefusesInvalidInvoicesWithoutSending(t *testing.T) {
	request, err := ParseLightningPayment(config.NetworkMainnet, "lnbc1example")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		decoded *lndrpc.DecodedPayReq
		err     error
	}{
		{"decode rejection", nil, errors.New("invalid invoice signature")},
		{"expired", &lndrpc.DecodedPayReq{IsExpired: true}, nil},
		{"missing details", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &paymentClient{decoded: tc.decoded, decodeErr: tc.err}
			payment, err := PrepareLightningPayment(client, request)
			if err == nil {
				t.Fatal("preparation unexpectedly succeeded")
			}
			if _, err := SendLightningPayment(client, payment); err == nil {
				t.Fatal("failed preparation could be sent")
			}
			if len(client.sentRequests) != 0 {
				t.Fatal("sent an unprepared invoice")
			}
		})
	}
	if _, err := PrepareLightningPayment(nil, request); err == nil {
		t.Fatal("accepted missing client")
	}
	client := &paymentClient{}
	if _, err := PrepareLightningPayment(client, PaymentRequest{}); err == nil || len(client.decodedRequests) != 0 {
		t.Fatal("empty request reached decoder")
	}
}

func TestPreparedPaymentOwnsDecodedDetails(t *testing.T) {
	request, err := ParseLightningPayment(config.NetworkMainnet, "lnbc1example")
	if err != nil {
		t.Fatal(err)
	}
	client := &paymentClient{decoded: &lndrpc.DecodedPayReq{AmountSats: 42}}
	payment, err := PrepareLightningPayment(client, request)
	if err != nil {
		t.Fatal(err)
	}
	client.decoded.AmountSats = 99
	details := payment.Details()
	details.AmountSats = 100
	if payment.Details().AmountSats != 42 {
		t.Fatal("prepared details changed through an external reference")
	}
}
