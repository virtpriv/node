package app

import (
	"errors"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/virtpriv/node/internal/lndrpc"
)

type TransactionLabelClient interface {
	SetTransactionLabel(lndrpc.TransactionLabelRequest) lndrpc.TransactionLabelResult
}

// PreparedTransactionLabel keeps the transaction and text fixed after Save.
// Labels belong to a transaction, including all of its wallet outputs.
type PreparedTransactionLabel struct {
	txid  *chainhash.Hash
	label string
}

func PrepareTransactionLabel(txid, label string) (PreparedTransactionLabel, error) {
	// NewHashFromStr permits short, zero-padded hashes. User requests must name
	// the complete transaction instead.
	if len(txid) != 64 {
		return PreparedTransactionLabel{}, errors.New("invalid transaction ID")
	}
	hash, err := chainhash.NewHashFromStr(txid)
	if err != nil {
		return PreparedTransactionLabel{}, errors.New("invalid transaction ID")
	}
	if len(label) == 0 || len(label) > 500 {
		return PreparedTransactionLabel{}, errors.New("enter a transaction label between 1 and 500 bytes")
	}
	return PreparedTransactionLabel{txid: hash, label: label}, nil
}

type TransactionLabelOutcome int

const (
	TransactionLabelUnknown TransactionLabelOutcome = iota
	TransactionLabelNotSaved
	TransactionLabelSaved
)

type TransactionLabelResult struct {
	Outcome TransactionLabelOutcome
	Err     error
}

// SaveTransactionLabel makes one request. An attempted RPC with a lost response
// does not establish whether LND saved the label, and is never retried here.
func SaveTransactionLabel(client TransactionLabelClient, prepared PreparedTransactionLabel) TransactionLabelResult {
	if prepared.txid == nil || client == nil {
		return TransactionLabelResult{Outcome: TransactionLabelNotSaved, Err: errors.New("transaction label is not ready to save")}
	}
	result := client.SetTransactionLabel(lndrpc.TransactionLabelRequest{Txid: *prepared.txid, Label: prepared.label})
	if !result.Submitted {
		if result.Err == nil {
			result.Err = errors.New("transaction label request was not submitted")
		}
		return TransactionLabelResult{Outcome: TransactionLabelNotSaved, Err: result.Err}
	}
	if result.Err != nil {
		return TransactionLabelResult{Outcome: TransactionLabelUnknown, Err: result.Err}
	}
	return TransactionLabelResult{Outcome: TransactionLabelSaved}
}
