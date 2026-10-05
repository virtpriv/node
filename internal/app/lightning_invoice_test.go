package app

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/virtpriv/node/internal/lndrpc"
)

type invoiceClient struct {
	invoice *lndrpc.Invoice
	err     error
	creates int
	lookups int
}

func (c *invoiceClient) AddInvoice(int64, string, bool) (*lndrpc.Invoice, error) {
	c.creates++
	return c.invoice, c.err
}

func (c *invoiceClient) LookupInvoice([]byte) (*lndrpc.Invoice, error) {
	c.lookups++
	return c.invoice, c.err
}

func TestInvoiceCreationRefusesInvalidRequestsAndResponses(t *testing.T) {
	client := &invoiceClient{}
	for _, amount := range []int64{0, -1} {
		if _, err := CreateLightningInvoice(client, InvoiceRequest{AmountSats: amount}); err == nil || client.creates != 0 {
			t.Fatal("invalid amount reached LND")
		}
	}
	for _, invoice := range []*lndrpc.Invoice{
		nil,
		{PaymentHash: strings.Repeat("01", 32)},
		{PaymentRequest: "lntbs1example", PaymentHash: "not-a-hash"},
		{PaymentRequest: "lntbs1example", PaymentHash: "01"},
	} {
		client.invoice = invoice
		created, err := CreateLightningInvoice(client, InvoiceRequest{AmountSats: 42})
		if err == nil {
			t.Fatalf("accepted unusable invoice: %+v", invoice)
		}
		if _, err := CheckLightningInvoice(client, created); err == nil || client.lookups != 0 {
			t.Fatal("failed creation started monitoring")
		}
	}
}

func TestInvoiceLookupPreservesPendingAndUnknownOutcomes(t *testing.T) {
	client := &invoiceClient{invoice: &lndrpc.Invoice{
		PaymentRequest: "lntbs1example", PaymentHash: strings.Repeat("01", 32),
	}}
	invoice, err := CreateLightningInvoice(client, InvoiceRequest{AmountSats: 42})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		invoice *lndrpc.Invoice
		err     error
		want    InvoiceState
	}{
		{"open", &lndrpc.Invoice{}, nil, InvoicePending},
		{"paid", &lndrpc.Invoice{Settled: true}, nil, InvoicePaid},
		{"expired", &lndrpc.Invoice{IsExpired: true}, nil, InvoiceExpired},
		{"canceled", &lndrpc.Invoice{Canceled: true}, nil, InvoiceCanceled},
		{"missing", nil, fmt.Errorf("lookup: %w", lndrpc.ErrInvoiceNotFound), InvoiceMissing},
		{"lookup error", nil, errors.New("connection lost"), InvoicePending},
		{"missing status", nil, nil, InvoicePending},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client.invoice, client.err = tc.invoice, tc.err
			state, err := CheckLightningInvoice(client, invoice)
			wantError := tc.want != InvoiceMissing && (tc.err != nil || tc.invoice == nil)
			if state != tc.want || (err != nil) != wantError {
				t.Fatalf("state=%v error=%v", state, err)
			}
		})
	}
}
