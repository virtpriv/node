package app

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/virtpriv/node/internal/api"
	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/p2p"
	"github.com/virtpriv/node/internal/system"
)

// P2PConnection is the TUI's LND link, connected again after a P2P switch.
type P2PConnection interface{ ReconnectContext(context.Context) }

// WorkflowKind is a change drawn on the shared progress screen.
type WorkflowKind int

const (
	SyncthingInstall WorkflowKind = iota
	P2PUpgrade
)

// WorkflowResult is a workflow's end. JobDone says the job completed, even
// when reading the node's new configuration afterwards failed. An error
// never says that earlier changes were undone.
type WorkflowResult struct {
	JobDone bool
	Outcome JobOutcome
	Config  *config.AppConfig
	Err     error
}

// WorkflowProgress is one look at the job, or the end once Result is set.
type WorkflowProgress struct {
	Job    api.Job
	Result *WorkflowResult
}

// Operation is one workflow being watched.
type Operation struct {
	kind   WorkflowKind
	names  []string
	events chan WorkflowProgress
}

func (o *Operation) Kind() WorkflowKind              { return o.kind }
func (o *Operation) Steps() []string                 { return append([]string(nil), o.names...) }
func (o *Operation) Events() <-chan WorkflowProgress { return o.events }

func (w *Workflows) operation(kind WorkflowKind, names []string) *Operation {
	return &Operation{kind: kind, names: append(names, localStep), events: make(chan WorkflowProgress, 1)}
}

// localStep is the TUI's own last step after the job, reading the node's
// new configuration.
const localStep = "Reloading node configuration"

// Workflows owns this TUI's Syncthing install and P2P switch watches. Close
// stops watching and waits for its readers. vpnd's jobs run on, also after
// this TUI exits.
type Workflows struct {
	syncthingVersion string
	ctx              context.Context
	cancel           context.CancelFunc
	mu               sync.Mutex
	workers          sync.WaitGroup
	jobs             *Jobs
	loadConfig       func() (*config.AppConfig, error)
	address          func(context.Context) (string, error)
}

func NewWorkflows(syncthingVersion string) *Workflows {
	ctx, cancel := context.WithCancel(context.Background())
	return &Workflows{
		ctx: ctx, cancel: cancel, syncthingVersion: syncthingVersion,
		jobs: NewJobs(), loadConfig: config.Load, address: system.ReadPublicIPv4,
	}
}

func (w *Workflows) Close() {
	w.mu.Lock()
	w.cancel()
	w.mu.Unlock()
	w.jobs.Close()
	w.workers.Wait()
}

// InstallSyncthing starts the Syncthing install job.
func (w *Workflows) InstallSyncthing() *Operation {
	op := w.operation(SyncthingInstall, api.SyncthingInstallStepNames(w.syncthingVersion))
	return w.follow(op, w.jobs.Start(api.KindSyncthingInstall, nil), nil)
}

// UpgradeP2P starts the P2P switch job. client is connected again once the
// job completes.
func (w *Workflows) UpgradeP2P(request p2p.UpgradeRequest, client P2PConnection) *Operation {
	op := w.operation(P2PUpgrade, api.UpgradeP2PToHybridStepNames())
	return w.follow(op, w.jobs.Start(api.KindP2PUpgrade, api.UpgradeP2PParams{ExpectedIPv4: request.Address()}), client)
}

// Attach watches a running workflow job, started earlier or by another TUI.
func (w *Workflows) Attach(job api.Job, client P2PConnection) *Operation {
	kind, names := SyncthingInstall, api.SyncthingInstallStepNames(w.syncthingVersion)
	if job.Kind == api.KindP2PUpgrade {
		kind, names = P2PUpgrade, api.UpgradeP2PToHybridStepNames()
	}
	if len(job.Steps) > 0 {
		names = job.Steps
	}
	return w.follow(w.operation(kind, names), w.jobs.Watch(job.ID), client)
}

// Latest reads the newest job of a workflow kind.
func (w *Workflows) Latest(kind WorkflowKind) (api.Job, bool, error) {
	name := api.KindSyncthingInstall
	if kind == P2PUpgrade {
		name = api.KindP2PUpgrade
	}
	return w.jobs.Latest(name)
}

func (w *Workflows) follow(op *Operation, updates <-chan JobUpdate, client P2PConnection) *Operation {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.ctx.Err(); err != nil {
		op.events <- WorkflowProgress{Result: &WorkflowResult{Outcome: JobNotStarted, Err: err}}
		close(op.events)
		return op
	}
	w.workers.Go(func() {
		defer close(op.events)
		send := func(p WorkflowProgress) bool {
			if p.Result == nil {
				select {
				case op.events <- p:
				default:
				}
				return true
			}
			select {
			case op.events <- p:
				return true
			case <-w.ctx.Done():
				return false
			}
		}
		var last JobUpdate
		for u := range updates {
			last = u
			if !u.Final {
				send(WorkflowProgress{Job: u.Job})
			}
		}
		result := WorkflowResult{Outcome: last.Outcome}
		if !last.Final {
			// The TUI is closing. No outcome is claimed.
			return
		}
		if last.Outcome != JobCompleted {
			result.Err = last.Error()
			send(WorkflowProgress{Job: last.Job, Result: &result})
			return
		}
		result.JobDone = true
		// The TUI that watched the switch connects its LND link again. Any
		// other TUI does so when its link reports LND unavailable.
		if client != nil && w.ctx.Err() == nil {
			client.ReconnectContext(w.ctx)
		}
		fresh, err := w.loadConfig()
		if err != nil {
			result.Err = fmt.Errorf("reload node configuration: %w", err)
		}
		result.Config = fresh
		send(WorkflowProgress{Job: last.Job, Result: &result})
	})
	return op
}

// ReadP2PAddress makes a fresh, bounded observation for one confirmation
// screen. Both screen cancellation and terminal shutdown release the read.
func (w *Workflows) ReadP2PAddress(parent context.Context) (p2p.UpgradeRequest, error) {
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
