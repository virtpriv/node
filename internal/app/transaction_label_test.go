package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/virtpriv/node/internal/lndrpc"
)

type labelClient struct {
	requests []lndrpc.TransactionLabelRequest
	result   lndrpc.TransactionLabelResult
}

func (c *labelClient) SetTransactionLabel(request lndrpc.TransactionLabelRequest) lndrpc.TransactionLabelResult {
	c.requests = append(c.requests, request)
	return c.result
}

func TestTransactionLabelValidationBeforeSubmission(t *testing.T) {
	txid := strings.Repeat("ab", 32)
	for _, tc := range []struct {
		name, txid, label string
	}{
		{"empty ID", "", "label"},
		{"short ID", "ab", "label"},
		{"long ID", txid + "ab", "label"},
		{"nonhex ID", strings.Repeat("z", 64), "label"},
		{"empty label", txid, ""},
		{"501 bytes", txid, strings.Repeat("a", 501)},
		{"multibyte over limit", txid, strings.Repeat("é", 251)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := PrepareTransactionLabel(tc.txid, tc.label)
			if err == nil {
				t.Fatal("invalid request prepared")
			}
		})
	}
	// LND counts bytes, not runes. Exact whitespace is intentional label text.
	for _, label := range []string{" label ", " ", strings.Repeat("é", 250)} {
		prepared, err := PrepareTransactionLabel(strings.ToUpper(txid), label)
		if err != nil {
			t.Fatal(err)
		}
		client := &labelClient{result: lndrpc.TransactionLabelResult{Submitted: true}}
		result := SaveTransactionLabel(client, prepared)
		if result.Outcome != TransactionLabelSaved || result.Err != nil || len(client.requests) != 1 ||
			client.requests[0].Txid.String() != txid || client.requests[0].Label != label {
			t.Fatalf("saved request differs from validated intent: %+v", client.requests)
		}
	}
}

func TestTransactionLabelOutcomesNeverRetry(t *testing.T) {
	client := &labelClient{}
	if result := SaveTransactionLabel(client, PreparedTransactionLabel{}); result.Outcome != TransactionLabelNotSaved || result.Err == nil || len(client.requests) != 0 {
		t.Fatal("unprepared request reached the client or implied success")
	}
	failure := errors.New("request failed")
	prepared, err := PrepareTransactionLabel(strings.Repeat("12", 32), "reviewed")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		wire lndrpc.TransactionLabelResult
		want TransactionLabelOutcome
	}{
		{"not attempted", lndrpc.TransactionLabelResult{Err: failure}, TransactionLabelNotSaved},
		{"lost response", lndrpc.TransactionLabelResult{Submitted: true, Err: failure}, TransactionLabelUnknown},
		{"acknowledged", lndrpc.TransactionLabelResult{Submitted: true}, TransactionLabelSaved},
		{"missing adapter result", lndrpc.TransactionLabelResult{}, TransactionLabelNotSaved},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &labelClient{result: tc.wire}
			result := SaveTransactionLabel(client, prepared)
			if result.Outcome != tc.want || len(client.requests) != 1 {
				t.Fatalf("result=%+v calls=%d", result, len(client.requests))
			}
			if tc.wire.Err != nil && !errors.Is(result.Err, failure) {
				t.Fatal("underlying failure lost")
			}
			if tc.want != TransactionLabelSaved && result.Err == nil {
				t.Fatal("unsuccessful result has no error")
			}
		})
	}
	if result := SaveTransactionLabel(nil, prepared); result.Outcome != TransactionLabelNotSaved || result.Err == nil {
		t.Fatal("missing client did not refuse")
	}
}
