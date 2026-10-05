package app

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/virtpriv/node/internal/helper"
	"github.com/virtpriv/node/internal/system"
)

// SSHVerification is one completed first-login observation. Address is only a
// display hint; failure to observe it must not change the verification fact.
type SSHVerification struct {
	Pending bool
	Address string
	Err     error
}

// SSHLoginVerifier asks the helper to verify first-login evidence and may clear
// the pending marker. Close cancels and joins local waits, not accepted root work.
type SSHLoginVerifier struct {
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	calls   sync.WaitGroup
	call    func(context.Context, string, any, any) error
	address func(context.Context) (string, error)
}

func NewSSHLoginVerifier() *SSHLoginVerifier {
	ctx, cancel := context.WithCancel(context.Background())
	return &SSHLoginVerifier{ctx: ctx, cancel: cancel, address: system.ReadPublicIPv4,
		call: func(ctx context.Context, verb string, params, result any) error {
			session, err := helper.StartContext(ctx, verb, params)
			if err != nil {
				return err
			}
			return session.Wait(result)
		},
	}
}

func (r *SSHLoginVerifier) Close() {
	r.mu.Lock()
	r.cancel()
	r.mu.Unlock()
	r.calls.Wait()
}

// Verify may clear the root-owned pending marker when sshd evidence verifies
// the login. An already-clear marker also succeeds. Address is only a hint.
func (r *SSHLoginVerifier) Verify() SSHVerification {
	r.mu.Lock()
	if err := r.ctx.Err(); err != nil {
		r.mu.Unlock()
		return SSHVerification{Err: err}
	}
	r.calls.Add(1)
	r.mu.Unlock()
	defer r.calls.Done()
	ctx, cancel := context.WithTimeout(r.ctx, 30*time.Second)
	defer cancel()
	// Pointer fields distinguish a complete false result from an absent body or
	// missing fields. An incomplete success must never dismiss the warning.
	var reply struct {
		Pending  *bool `json:"pending"`
		Verified *bool `json:"verified"`
	}
	err := r.call(ctx, helper.VerbVerifyAdminLogin, nil, &reply)
	if err := ctx.Err(); err != nil {
		return SSHVerification{Err: err}
	}
	if err != nil {
		return SSHVerification{Err: err}
	}
	if reply.Pending == nil || reply.Verified == nil || (*reply.Pending && *reply.Verified) {
		return SSHVerification{Err: errors.New("incomplete or inconsistent SSH verification reply")}
	}
	result := SSHVerification{Pending: *reply.Pending}
	if result.Pending {
		if address, err := r.address(ctx); err == nil {
			result.Address = address
		}
	}
	if err := ctx.Err(); err != nil {
		return SSHVerification{Err: err}
	}
	return result
}
