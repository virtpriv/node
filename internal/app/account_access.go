package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/virtpriv/node/internal/accountaccess"
	"github.com/virtpriv/node/internal/helper"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/sshkeys"
)

type AccountDetails struct {
	accountaccess.Detail
	Authorized       map[string]bool
	OwnerKeysProblem string
}

// AccountKeyImport freezes the reviewed source identity and exact public key.
type AccountKeyImport struct {
	Account accountaccess.Account
	Key     sshkeys.Key
}

// AccountAccess owns bounded helper observations and owner-side key imports.
// Close cancels helper waits and joins admitted work, including a local write
// already in progress. The helper never writes an owner's home for this flow.
type AccountAccess struct {
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	workers sync.WaitGroup
	ssh     *SSHAccess
	call    func(context.Context, string, any, any) error
}

func NewAccountAccess(ssh *SSHAccess) *AccountAccess {
	ctx, cancel := context.WithCancel(context.Background())
	return &AccountAccess{ctx: ctx, cancel: cancel, ssh: ssh,
		call: func(ctx context.Context, verb string, params, result any) error {
			session, err := helper.StartContext(ctx, verb, params)
			if err != nil {
				return err
			}
			return session.Wait(result)
		},
	}
}

func (a *AccountAccess) begin() (context.Context, func(), error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.ctx.Err(); err != nil {
		return nil, nil, err
	}
	a.workers.Add(1)
	ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
	return ctx, func() { cancel(); a.workers.Done() }, nil
}

func (a *AccountAccess) Close() {
	a.mu.Lock()
	a.cancel()
	a.mu.Unlock()
	a.workers.Wait()
}

func (a *AccountAccess) List() (accountaccess.Inventory, error) {
	ctx, done, err := a.begin()
	if err != nil {
		return accountaccess.Inventory{}, err
	}
	defer done()
	var reply accountaccess.Inventory
	if err := a.call(ctx, helper.VerbReadAccounts, nil, &reply); err != nil {
		return accountaccess.Inventory{}, err
	}
	if err := ctx.Err(); err != nil {
		return accountaccess.Inventory{}, err
	}
	if reply.Accounts == nil {
		return accountaccess.Inventory{}, errors.New("helper returned an incomplete account inventory")
	}
	return reply, nil
}

func (a *AccountAccess) readDetail(ctx context.Context, ref accountaccess.Ref) (accountaccess.Detail, error) {
	if err := ref.Validate(); err != nil {
		return accountaccess.Detail{}, err
	}
	var reply accountaccess.Detail
	if err := a.call(ctx, helper.VerbReadAccount, ref, &reply); err != nil {
		return reply, err
	}
	if err := ctx.Err(); err != nil {
		return reply, err
	}
	if reply.Account.Ref() != ref || reply.Source.User != ref.Name {
		return reply, errors.New("helper returned an incomplete or different account observation")
	}
	return reply, nil
}

func (a *AccountAccess) Inspect(ref accountaccess.Ref) (AccountDetails, error) {
	ctx, done, err := a.begin()
	if err != nil {
		return AccountDetails{}, err
	}
	defer done()
	detail, err := a.readDetail(ctx, ref)
	if err != nil {
		return AccountDetails{}, err
	}
	result := AccountDetails{Detail: detail, Authorized: make(map[string]bool)}
	keys, err := a.ssh.ListKeys()
	if err != nil {
		result.OwnerKeysProblem = err.Error()
	} else {
		for _, key := range keys {
			result.Authorized[key.Fingerprint] = true
		}
	}
	return result, nil
}

func (a *AccountAccess) Import(review AccountKeyImport) error {
	ctx, done, err := a.begin()
	if err != nil {
		return err
	}
	defer done()
	if review.Account.Name == paths.AdminUser || !review.Account.KeyDiscoverySupported() {
		return errors.New("choose a supported source account other than vpn")
	}
	key, err := sshkeys.Parse(review.Key.RawLine)
	if err != nil || key != review.Key {
		return errors.New("invalid reviewed public key")
	}
	detail, err := a.readDetail(ctx, review.Account.Ref())
	if err != nil {
		return fmt.Errorf("recheck public-key source: %w", err)
	}
	if detail.Account != review.Account || detail.Source.Problem != "" {
		return errors.New("source account changed or could not be fully read; refresh and review again")
	}
	found := false
	for _, current := range detail.Source.Keys {
		if current == key {
			found = true
			break
		}
	}
	if !found {
		return errors.New("reviewed public key changed or disappeared; refresh and review again")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// AddKey rechecks the current destination under its stable directory lock,
	// preserving unmanaged lines and rejecting duplicate credentials.
	return a.ssh.AddKey(key.RawLine)
}
