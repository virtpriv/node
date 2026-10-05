package app

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/helper"
	"github.com/virtpriv/node/internal/p2p"
	"github.com/virtpriv/node/internal/system"
)

type P2PConnection interface{ ReconnectContext(context.Context) }

type HelperWorkflowKind int

const (
	SyncthingInstall HelperWorkflowKind = iota
	P2PUpgrade
	SelfUpdate
)

// HelperWorkflowResult distinguishes successful mutation from a failed local
// configuration read. An error never establishes that earlier changes were undone.
type HelperWorkflowResult struct {
	HelperSucceeded bool
	Config          *config.AppConfig
	Err             error
}

// HelperProgress reports one completed step, or the terminal result at Index.
// The terminal event occupies the next index and is emitted exactly once.
type HelperProgress struct {
	Index  int
	Result *HelperWorkflowResult
}

type HelperOperation struct {
	kind   HelperWorkflowKind
	names  []string
	events chan HelperProgress
}

func (o *HelperOperation) Kind() HelperWorkflowKind      { return o.kind }
func (o *HelperOperation) Steps() []string               { return append([]string(nil), o.names...) }
func (o *HelperOperation) Events() <-chan HelperProgress { return o.events }

type helperSession interface {
	WaitStep(int) error
	Wait(any) error
	Close()
}

// HelperWorkflows owns this client's streaming operations. Close cancels local
// observation and waits for its readers to release their connections. The helper
// retains authority to finish accepted operations, including after this TUI exits.
type HelperWorkflows struct {
	syncthingVersion string
	ctx              context.Context
	cancel           context.CancelFunc
	mu               sync.Mutex
	workers          sync.WaitGroup
	start            func(context.Context, string, any) (helperSession, error)
	loadConfig       func() (*config.AppConfig, error)
	address          func(context.Context) (string, error)
}

func NewHelperWorkflows(syncthingVersion string) *HelperWorkflows {
	ctx, cancel := context.WithCancel(context.Background())
	return &HelperWorkflows{
		ctx: ctx, cancel: cancel, syncthingVersion: syncthingVersion,
		start: func(ctx context.Context, verb string, params any) (helperSession, error) {
			return helper.StartContext(ctx, verb, params)
		},
		loadConfig: config.Load,
		address:    system.ReadPublicIPv4,
	}
}

func (w *HelperWorkflows) Close() {
	w.mu.Lock()
	w.cancel()
	w.mu.Unlock()
	w.workers.Wait()
}

func (w *HelperWorkflows) InstallSyncthing() *HelperOperation {
	return w.begin(SyncthingInstall, helper.VerbSyncthingInstall, nil,
		helper.SyncthingInstallStepNames(w.syncthingVersion), nil)
}

func (w *HelperWorkflows) UpgradeP2P(request p2p.UpgradeRequest, client P2PConnection) *HelperOperation {
	return w.begin(P2PUpgrade, helper.VerbUpgradeP2PToHybrid,
		helper.UpgradeP2PParams{ExpectedIPv4: request.Address()},
		helper.UpgradeP2PToHybridStepNames(), client)
}

// ReadP2PAddress makes a fresh, bounded observation for one confirmation screen.
// Both screen cancellation and terminal shutdown release the read.
func (w *HelperWorkflows) ReadP2PAddress(parent context.Context) (p2p.UpgradeRequest, error) {
	w.mu.Lock()
	if err := w.ctx.Err(); err != nil {
		w.mu.Unlock()
		return p2p.UpgradeRequest{}, err
	}
	w.workers.Add(1)
	w.mu.Unlock()
	defer w.workers.Done()
	ctx, cancel := context.WithTimeout(w.ctx, 3*time.Second)
	defer cancel()
	stop := context.AfterFunc(parent, cancel)
	defer stop()
	if err := parent.Err(); err != nil {
		return p2p.UpgradeRequest{}, err
	}
	address, err := w.address(ctx)
	if err := ctx.Err(); err != nil {
		return p2p.UpgradeRequest{}, err
	}
	if err != nil {
		return p2p.UpgradeRequest{}, err
	}
	return p2p.NewUpgradeRequest(address)
}

func (w *HelperWorkflows) UpdateSelf(version string) *HelperOperation {
	return w.begin(SelfUpdate, helper.VerbSelfUpdate,
		helper.SelfUpdateParams{Version: version}, helper.SelfUpdateStepNames(version), nil)
}

func (w *HelperWorkflows) begin(kind HelperWorkflowKind, verb string, params any, names []string, client P2PConnection) *HelperOperation {
	rootSteps := len(names)
	if kind != SelfUpdate {
		names = append(names, "Reloading node configuration")
	}
	// Every possible event fits even if the consumer disappears. Rendering never
	// controls helper execution, and a stopped TUI cannot strand the reader.
	op := &HelperOperation{kind: kind, names: names, events: make(chan HelperProgress, len(names)+1)}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.ctx.Err(); err != nil {
		op.events <- HelperProgress{Result: &HelperWorkflowResult{Err: err}}
		close(op.events)
		return op
	}
	w.workers.Go(func() {
		defer close(op.events)
		result, index := w.run(op, verb, params, rootSteps)
		if result.Err == nil && client != nil && w.ctx.Err() == nil {
			client.ReconnectContext(w.ctx)
		}
		op.events <- HelperProgress{Index: index, Result: &result}
	})
	return op
}

func (w *HelperWorkflows) run(op *HelperOperation, verb string, params any, rootSteps int) (HelperWorkflowResult, int) {
	var result HelperWorkflowResult
	if err := w.ctx.Err(); err != nil {
		result.Err = err
		return result, 0
	}
	session, err := w.start(w.ctx, verb, params)
	if err != nil {
		result.Err = err
		return result, 0
	}
	defer session.Close()
	for i := range rootSteps {
		if err := session.WaitStep(i); err != nil {
			result.Err = err
			return result, i
		}
		// A final step event is not the operation's success terminator.
		if i == rootSteps-1 {
			if err := session.Wait(nil); err != nil {
				result.Err = err
				return result, i
			}
			result.HelperSucceeded = true
		}
		op.events <- HelperProgress{Index: i}
	}
	if op.kind != SelfUpdate {
		if err := w.ctx.Err(); err != nil {
			result.Err = err
			return result, rootSteps
		}
		fresh, err := w.loadConfig()
		if err != nil {
			result.Err = fmt.Errorf("reload node configuration: %w", err)
			return result, rootSteps
		}
		result.Config = fresh
		op.events <- HelperProgress{Index: rootSteps}
	}
	return result, len(op.names)
}
