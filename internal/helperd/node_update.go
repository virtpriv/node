package helperd

import (
	"encoding/json"
	"errors"

	"github.com/virtpriv/node/internal/helper"
	"github.com/virtpriv/node/internal/update"
	"github.com/virtpriv/node/internal/update/files"
)

func verbPrepareUpdate(ctx *verbCtx, raw json.RawMessage) (any, error) {
	var p helper.PrepareUpdateParams
	if err := decode(raw, &p); err != nil {
		return nil, err
	}
	return update.Prepare(ctx.version, p.Version)
}
func verbStartUpdate(ctx *verbCtx, raw json.RawMessage) (any, error) {
	var p helper.UpdateApproval
	if err := decode(raw, &p); err != nil {
		return nil, err
	}
	return update.Start(ctx.version, p.Token)
}
func verbUpdateStatus(ctx *verbCtx, raw json.RawMessage) (any, error) {
	if err := rejectParams(raw); err != nil {
		return nil, err
	}
	s, err := update.Status()
	if err == nil && s.Phase == "complete" && s.Version != ctx.version {
		ctx.exitAfterEnd = true
	}
	return s, err
}
func verbResumeUpdate(_ *verbCtx, raw json.RawMessage) (any, error) {
	var p helper.UpdateResumeParams
	if err := decode(raw, &p); err != nil {
		return nil, err
	}
	return update.Resume(p.ID)
}

func verbCancelUpdate(_ *verbCtx, raw json.RawMessage) (any, error) {
	var p helper.UpdateResumeParams
	if err := decode(raw, &p); err != nil {
		return nil, err
	}
	return update.Cancel(p.ID)
}

// Read-only observations remain available during a long worker operation.
// New verbs default to requiring the shared mutation lock. Node update
// admission takes the same lock internally so approval checks stay atomic.
func guardHelperOperation(verb string) (func(), error) {
	switch verb {
	case helper.VerbReadAccounts, helper.VerbReadAccount, helper.VerbDirSize,
		helper.VerbReadNodeAddresses, helper.VerbReadSSHAuth, helper.VerbReadWalletState,
		helper.VerbReadKeyVerificationState, helper.VerbUpdateStatus,
		helper.VerbPrepareUpdate, helper.VerbStartUpdate, helper.VerbResumeUpdate, helper.VerbCancelUpdate:
		return func() {}, nil
	case helper.VerbSelfUpdate:
		return nil, errors.New("this update method was replaced, reopen the TUI and use Node Updates")
	}
	unlock, err := files.Lock(update.LockPath)
	if err != nil {
		return nil, err
	}
	if err := update.MutationGuard(); err != nil {
		unlock()
		return nil, err
	}
	return unlock, nil
}
