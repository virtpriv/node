package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/virtpriv/node/internal/api"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/update/protocol"
)

// NodeUpdates is the TUI's side of Node Updates. Closing it never stops a
// Node Update or a Prepare job. Another TUI can read and resume the same job.
type NodeUpdates struct {
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	workers sync.WaitGroup
	jobs    *Jobs
	call    func(context.Context, string, any, any) error
}

func NewNodeUpdates() *NodeUpdates {
	ctx, cancel := context.WithCancel(context.Background())
	return &NodeUpdates{ctx: ctx, cancel: cancel, jobs: NewJobs(), call: api.Call}
}

func (w *NodeUpdates) Close() {
	w.mu.Lock()
	w.cancel()
	w.mu.Unlock()
	w.jobs.Close()
	w.workers.Wait()
}

func (w *NodeUpdates) do(verb string, p, result any) error {
	w.mu.Lock()
	if err := w.ctx.Err(); err != nil {
		w.mu.Unlock()
		return err
	}
	w.workers.Add(1)
	w.mu.Unlock()
	defer w.workers.Done()
	ctx, cancel := context.WithTimeout(w.ctx, time.Minute)
	defer cancel()
	return w.call(ctx, verb, p, result)
}

// Prepare starts the Prepare Update job for version and waits for its
// review. Leaving the screen does not stop the job.
func (w *NodeUpdates) Prepare(version string) (protocol.Review, error) {
	return reviewOf(last(w.jobs.Start(api.KindPrepareUpdate, api.PrepareUpdateParams{Version: version})))
}

// PrepareJob is the newest Prepare Update job as a screen opening later sees
// it.
type PrepareJob struct {
	ID      string
	Version string
	Running bool
	// Review and Err are its end, when it has ended.
	Review *protocol.Review
	Err    error
	Ended  time.Time
}

// LastPrepare reads the newest Prepare Update job.
func (w *NodeUpdates) LastPrepare() (PrepareJob, bool, error) {
	job, found, err := w.jobs.Latest(api.KindPrepareUpdate)
	if err != nil || !found {
		return PrepareJob{}, false, err
	}
	p := PrepareJob{ID: job.ID, Version: strings.TrimPrefix(job.Title, "Prepare update to v"), Ended: job.Ended}
	outcome := Outcome(job)
	if outcome == JobRunning {
		p.Running = true
		return p, true, nil
	}
	review, err := reviewOf(JobUpdate{Job: job, Outcome: outcome, Final: true})
	if err != nil {
		p.Err = err
	} else {
		p.Review = &review
	}
	return p, true, nil
}

// AwaitPrepare waits for a Prepare Update job started earlier.
func (w *NodeUpdates) AwaitPrepare(id string) (protocol.Review, error) {
	return reviewOf(last(w.jobs.Watch(id)))
}

func last(updates <-chan JobUpdate) JobUpdate {
	var u JobUpdate
	for u = range updates {
	}
	return u
}

func reviewOf(u JobUpdate) (protocol.Review, error) {
	var review protocol.Review
	if u.Outcome != JobCompleted {
		if !u.Final {
			return review, errors.New("the TUI closed while the release was checked")
		}
		return review, u.Error()
	}
	if err := json.Unmarshal(u.Job.Result, &review); err != nil {
		return review, errors.New("vpnd returned an unreadable review")
	}
	return review, nil
}

func (w *NodeUpdates) Start(token string) (protocol.Status, error) {
	var s protocol.Status
	err := w.do(api.VerbStartUpdate, api.UpdateApproval{Token: token}, &s)
	return s, err
}

// Status reads the Node Update as a job. No Node Update yet is the zero
// status.
func (w *NodeUpdates) Status() (protocol.Status, error) {
	var s protocol.Status
	var status api.JobStatus
	if err := w.do(api.VerbJobStatus, api.JobStatusParams{Kind: api.KindNodeUpdate}, &status); err != nil || !status.Found {
		return s, err
	}
	if err := json.Unmarshal(status.Job.Result, &s); err != nil {
		return s, errors.New("vpnd returned an unreadable update status")
	}
	return s, nil
}

func (w *NodeUpdates) Resume(id string) (protocol.Status, error) {
	var s protocol.Status
	err := w.do(api.VerbResumeUpdate, api.UpdateResumeParams{ID: id}, &s)
	return s, err
}

func (w *NodeUpdates) Cancel(id string) (protocol.Status, error) {
	var s protocol.Status
	err := w.do(api.VerbCancelUpdate, api.UpdateResumeParams{ID: id}, &s)
	return s, err
}

// UpdateUnlockArguments selects LND's own password prompt. VPN never stores
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
