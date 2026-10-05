package app

import (
	"encoding/hex"
	"errors"

	"github.com/virtpriv/node/internal/lndrpc"
)

type LightningInvoiceClient interface {
	AddInvoice(int64, string, bool) (*lndrpc.Invoice, error)
	LookupInvoice([]byte) (*lndrpc.Invoice, error)
}

type InvoiceRequest struct {
	AmountSats int64
	Memo       string
	Blinded    bool
}

// LightningInvoice binds the displayed invoice to the hash used to check it.
type LightningInvoice struct {
	paymentRequest string
	paymentHash    [32]byte
	amountSats     int64
}

func (i LightningInvoice) PaymentRequest() string { return i.paymentRequest }
func (i LightningInvoice) AmountSats() int64      { return i.amountSats }

type InvoiceState int

const (
	InvoicePending InvoiceState = iota
	InvoicePaid
	InvoiceExpired
	InvoiceCanceled
	InvoiceMissing
)

func CreateLightningInvoice(client LightningInvoiceClient, request InvoiceRequest) (LightningInvoice, error) {
	if request.AmountSats < 1 {
		return LightningInvoice{}, errors.New("Minimum 1 sat")
	}
	if client == nil {
		return LightningInvoice{}, errors.New("LND not connected")
	}
	invoice, err := client.AddInvoice(request.AmountSats, request.Memo, request.Blinded)
	if err != nil {
		return LightningInvoice{}, err
	}
	if invoice == nil || invoice.PaymentRequest == "" {
		return LightningInvoice{}, errors.New("LND returned no invoice; check Payment History before retrying")
	}
	hash, err := hex.DecodeString(invoice.PaymentHash)
	if err != nil || len(hash) != 32 {
		return LightningInvoice{}, errors.New("LND returned an invalid invoice hash; check Payment History before retrying")
	}
	return LightningInvoice{
		paymentRequest: invoice.PaymentRequest,
		paymentHash:    [32]byte(hash),
		amountSats:     invoice.AmountSats,
	}, nil
}

// CheckLightningInvoice distinguishes a missing record from a failed lookup.
// Neither establishes whether payment succeeded.
func CheckLightningInvoice(client LightningInvoiceClient, invoice LightningInvoice) (InvoiceState, error) {
	if invoice.paymentRequest == "" {
		return InvoicePending, errors.New("Create an invoice before checking payment")
	}
	if client == nil {
		return InvoicePending, errors.New("LND not connected")
	}
	status, err := client.LookupInvoice(invoice.paymentHash[:])
	if errors.Is(err, lndrpc.ErrInvoiceNotFound) {
		return InvoiceMissing, nil
	}
	if err != nil {
		return InvoicePending, err
	}
	if status == nil {
		return InvoicePending, errors.New("LND returned no invoice status")
	}
	if status.Settled {
		return InvoicePaid, nil
	}
	if status.Canceled {
		return InvoiceCanceled, nil
	}
	if status.IsExpired {
		return InvoiceExpired, nil
	}
	return InvoicePending, nil
}
