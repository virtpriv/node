package app

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/virtpriv/node/internal/helper"
	"github.com/virtpriv/node/internal/lndrpc"
)

type WalletObservation struct {
	Presence WalletPresence
	Err      error
}

type WalletClientResult struct {
	Client *lndrpc.Client
	Err    error
}

// WalletRuntime owns local presence reads and unpublished clients for one TUI.
// Claim transfers a client to the caller. Close cancels and joins local work,
// and closes any client whose completion was never claimed by the event loop.
// Cancellation cannot undo credential staging already accepted by the helper.
type WalletRuntime struct {
	ctx         context.Context
	cancel      context.CancelFunc
	mu          sync.Mutex
	calls       sync.WaitGroup
	clients     map[*lndrpc.Client]struct{}
	read        func(context.Context, any) error
	open        func(context.Context, bool) (*lndrpc.Client, error)
	closeClient func(*lndrpc.Client)
}

func NewWalletRuntime() *WalletRuntime {
	ctx, cancel := context.WithCancel(context.Background())
	return &WalletRuntime{ctx: ctx, cancel: cancel, clients: make(map[*lndrpc.Client]struct{}),
		read: func(ctx context.Context, result any) error {
			session, err := helper.StartContext(ctx, helper.VerbReadWalletState, nil)
			if err != nil {
				return err
			}
			return session.Wait(result)
		},
		open: func(ctx context.Context, stagedOnly bool) (*lndrpc.Client, error) {
			if stagedOnly {
				return lndrpc.NewStagedClient()
			}
			return lndrpc.NewContext(ctx)
		},
		closeClient: (*lndrpc.Client).Close,
	}
}

func (r *WalletRuntime) begin(caller context.Context, timeout time.Duration) (context.Context, func(), error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ctx.Err(); err != nil {
		return nil, nil, err
	}
	if err := caller.Err(); err != nil {
		return nil, nil, err
	}
	r.calls.Add(1)
	ctx, cancel := context.WithTimeout(r.ctx, timeout)
	stop := context.AfterFunc(caller, cancel)
	return ctx, func() { stop(); cancel(); r.calls.Done() }, nil
}

func (r *WalletRuntime) Read(caller context.Context) WalletObservation {
	ctx, done, err := r.begin(caller, 30*time.Second)
	if err != nil {
		return WalletObservation{Err: err}
	}
	defer done()
	// A missing field, empty body or null must not become confirmed absence.
	var reply struct {
		Exists *bool `json:"wallet_exists"`
	}
	err = r.read(ctx, &reply)
	if canceled := errors.Join(ctx.Err(), caller.Err()); canceled != nil {
		err = canceled
	}
	if err != nil {
		return WalletObservation{Err: err}
	}
	if reply.Exists == nil {
		return WalletObservation{Err: errors.New("incomplete wallet state reply")}
	}
	if *reply.Exists {
		return WalletObservation{Presence: WalletPresent}
	}
	return WalletObservation{Presence: WalletAbsent}
}

// Open keeps ordinary startup's probe/repair separate from the staged-only
// post-creation path. The deadline includes any helper queue and retry waits.
// Ordinary file reads must finish before cancellation can join the worker.
func (r *WalletRuntime) Open(caller context.Context, stagedOnly bool) WalletClientResult {
	ctx, done, err := r.begin(caller, 75*time.Second)
	if err != nil {
		return WalletClientResult{Err: err}
	}
	defer done()
	client, err := r.open(ctx, stagedOnly)
	r.mu.Lock()
	if canceled := errors.Join(ctx.Err(), caller.Err()); canceled != nil {
		err = canceled
	}
	if err == nil && client == nil {
		err = errors.New("wallet client initialization returned no client")
	}
	if err == nil {
		r.clients[client] = struct{}{}
	}
	r.mu.Unlock()
	if err != nil {
		if client != nil {
			r.closeClient(client)
		}
		return WalletClientResult{Err: err}
	}
	return WalletClientResult{Client: client}
}

func (r *WalletRuntime) Claim(client *lndrpc.Client) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.clients[client]; !ok || r.ctx.Err() != nil {
		return false
	}
	delete(r.clients, client)
	return true
}

// Discard only closes a still-unclaimed client. Duplicate or obsolete messages
// cannot close a client already published to the rest of the application.
func (r *WalletRuntime) Discard(client *lndrpc.Client) {
	r.mu.Lock()
	_, owned := r.clients[client]
	delete(r.clients, client)
	r.mu.Unlock()
	if owned {
		r.closeClient(client)
	}
}

func (r *WalletRuntime) Close() {
	r.mu.Lock()
	r.cancel()
	r.mu.Unlock()
	r.calls.Wait()
	r.mu.Lock()
	clients := r.clients
	r.clients = nil
	r.mu.Unlock()
	for client := range clients {
		r.closeClient(client)
	}
}
