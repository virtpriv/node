package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/helper"
	"github.com/virtpriv/node/internal/lndrpc"
)

type WalletPresence int

const (
	WalletUnknown WalletPresence = iota
	WalletAbsent
	WalletPresent
)

// WalletExecution records process evidence separately from terminal restoration.
// A failed command does not establish that InitWallet was rejected by LND.
type WalletExecution struct {
	Created          bool
	SeedAcknowledged bool
	Err              error
}

type WalletCreationResult struct {
	Presence          WalletPresence
	CredentialsStaged bool
	SeedAcknowledged  bool
	Err               error
}

func (r WalletCreationResult) CanRetryFinalization() bool {
	return r.Presence == WalletUnknown || (r.Presence == WalletPresent && !r.CredentialsStaged)
}

// WalletCreation owns bounded readiness and finalization calls for one TUI.
// Interactive password and seed input stays with the terminal adapter. Close
// cancels local observation, not an accepted LND or helper mutation.
type WalletCreation struct {
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	workers sync.WaitGroup
	read    func(context.Context) (lndrpc.WalletState, error)
	stage   func(context.Context) error
}

func NewWalletCreation() *WalletCreation {
	ctx, cancel := context.WithCancel(context.Background())
	return &WalletCreation{ctx: ctx, cancel: cancel,
		read: lndrpc.ReadWalletSetupState,
		stage: func(ctx context.Context) error {
			session, err := helper.StartContext(ctx, helper.VerbStageLNDMacaroon, nil)
			if err != nil {
				return err
			}
			defer session.Close()
			return session.Wait(nil)
		},
	}
}

func (w *WalletCreation) Close() {
	w.mu.Lock()
	w.cancel()
	w.mu.Unlock()
	w.workers.Wait()
}

func (w *WalletCreation) begin(timeout time.Duration) (context.Context, func(), error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.ctx.Err(); err != nil {
		return nil, nil, err
	}
	w.workers.Add(1)
	ctx, cancel := context.WithTimeout(w.ctx, timeout)
	return ctx, func() { cancel(); w.workers.Done() }, nil
}

// Prepare returns the validated lncli network only after a fresh NON_EXISTING
// observation. The deadline includes RPC duration and retry waits. Another
// process may still create a wallet afterward; LND remains the final authority.
func (w *WalletCreation) Prepare(network string) (string, error) {
	profile, err := config.NetworkConfigFromName(network)
	if err != nil {
		return "", err
	}
	ctx, done, err := w.begin(120 * time.Second)
	if err != nil {
		return "", err
	}
	defer done()
	for {
		state, readErr := w.read(ctx)
		if ctx.Err() != nil {
			return "", fmt.Errorf("waiting for wallet creation readiness: %w", ctx.Err())
		}
		if readErr == nil {
			switch walletPresence(state) {
			case WalletAbsent:
				return profile.LNDNetwork, nil
			case WalletPresent:
				return "", errors.New("an LND wallet already exists; creation was not started")
			default:
				if state != "WAITING_TO_START" {
					return "", fmt.Errorf("unrecognized LND wallet state %q; creation was not started", state)
				}
			}
		}
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", fmt.Errorf("waiting for wallet creation readiness: %w", errors.Join(ctx.Err(), readErr))
		case <-timer.C:
		}
	}
}

// Finalize can be called again to reobserve and stage credentials. It never
// invokes wallet creation or infers seed acknowledgement from wallet existence.
func (w *WalletCreation) Finalize(execution WalletExecution) WalletCreationResult {
	result := WalletCreationResult{SeedAcknowledged: execution.Created && execution.SeedAcknowledged}
	ctx, done, err := w.begin(75 * time.Second)
	if err != nil {
		result.Err = err
		return result
	}
	defer done()
	state, readErr := w.read(ctx)
	if readErr == nil {
		result.Presence = walletPresence(state)
	}
	// A successful lncli InitWallet remains creation evidence if a later read
	// is unavailable. An explicit conflicting absence requires investigation.
	if execution.Created && result.Presence == WalletUnknown {
		result.Presence = WalletPresent
	}
	if execution.Created && result.Presence == WalletAbsent {
		result.Presence = WalletUnknown
		result.Err = errors.New("LND reports no wallet after successful creation; wallet state needs investigation")
		return result
	}
	switch result.Presence {
	case WalletUnknown:
		result.Err = fmt.Errorf("wallet state is unknown; do not repeat creation: %w",
			errors.Join(execution.Err, readErr, errors.New("no conclusive wallet state")))
		return result
	case WalletAbsent:
		result.Err = errors.Join(errors.New("LND reports that no wallet exists"), execution.Err)
		return result
	}
	if err := ctx.Err(); err != nil {
		result.Err = err
		return result
	}
	if err := w.stage(ctx); err != nil {
		result.Err = fmt.Errorf("the wallet exists, but its credentials could not be staged: %w", errors.Join(execution.Err, err))
		return result
	}
	result.CredentialsStaged = true
	result.Err = execution.Err
	if !result.SeedAcknowledged {
		result.Err = errors.Join(result.Err, errors.New("the wallet exists, but seed acknowledgement was not confirmed; preserve any seed you recorded and do not recreate the wallet"))
	}
	return result
}

func walletPresence(state lndrpc.WalletState) WalletPresence {
	switch state {
	case "NON_EXISTING":
		return WalletAbsent
	case "LOCKED", "UNLOCKED", "RPC_ACTIVE", "SERVER_ACTIVE":
		return WalletPresent
	default:
		return WalletUnknown
	}
}
