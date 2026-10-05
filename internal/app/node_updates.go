package app

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/virtpriv/node/internal/helper"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/update/protocol"
)

// NodeUpdates owns observation lifetimes. Canceling a client never cancels
// the root-owned job; another terminal can read and resume that same job.
type NodeUpdates struct {
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	workers sync.WaitGroup
}

func NewNodeUpdates() *NodeUpdates {
	ctx, cancel := context.WithCancel(context.Background())
	return &NodeUpdates{ctx: ctx, cancel: cancel}
}
func (w *NodeUpdates) Close() { w.mu.Lock(); w.cancel(); w.mu.Unlock(); w.workers.Wait() }
func (w *NodeUpdates) call(verb string, p, result any) error {
	w.mu.Lock()
	if err := w.ctx.Err(); err != nil {
		w.mu.Unlock()
		return err
	}
	w.workers.Add(1)
	w.mu.Unlock()
	defer w.workers.Done()
	duration := time.Minute
	if verb == helper.VerbPrepareUpdate {
		duration = 45 * time.Minute
	}
	ctx, cancel := context.WithTimeout(w.ctx, duration)
	defer cancel()
	s, err := helper.StartContext(ctx, verb, p)
	if err != nil {
		return err
	}
	defer s.Close()
	return s.Wait(result)
}
func (w *NodeUpdates) Prepare(version string) (protocol.Review, error) {
	var r protocol.Review
	err := w.call(helper.VerbPrepareUpdate, helper.PrepareUpdateParams{Version: version}, &r)
	return r, err
}
func (w *NodeUpdates) Start(token string) (protocol.Status, error) {
	var s protocol.Status
	err := w.call(helper.VerbStartUpdate, helper.UpdateApproval{Token: token}, &s)
	return s, err
}
func (w *NodeUpdates) Status() (protocol.Status, error) {
	var s protocol.Status
	err := w.call(helper.VerbUpdateStatus, nil, &s)
	return s, err
}
func (w *NodeUpdates) Resume(id string) (protocol.Status, error) {
	var s protocol.Status
	err := w.call(helper.VerbResumeUpdate, helper.UpdateResumeParams{ID: id}, &s)
	return s, err
}

func (w *NodeUpdates) Cancel(id string) (protocol.Status, error) {
	var s protocol.Status
	err := w.call(helper.VerbCancelUpdate, helper.UpdateResumeParams{ID: id}, &s)
	return s, err
}

// UpdateUnlockArguments selects LND's native password prompt. VPN never stores
// the entered secret.
func UpdateUnlockArguments(s protocol.Status) ([]string, error) {
	if !s.Active || s.Phase != "waiting-unlock" || !s.Running {
		return nil, errors.New("the update is not waiting for a wallet unlock")
	}
	if s.WalletNetwork != "mainnet" && s.WalletNetwork != "testnet4" && s.WalletNetwork != "signet" {
		return nil, errors.New("invalid wallet network")
	}
	return []string{"--rpcserver=" + paths.LNDGRPCEndpoint, "--tlscertpath=" + paths.StateLNDTLSCert, "--network=" + s.WalletNetwork, "unlock"}, nil
}
