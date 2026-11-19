package vpnd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/virtpriv/node/internal/accountaccess"
	"github.com/virtpriv/node/internal/api"
	"github.com/virtpriv/node/internal/host"
	"github.com/virtpriv/node/internal/jobs"
	"github.com/virtpriv/node/internal/update"
	"github.com/virtpriv/node/internal/update/protocol"
)

// ── The request menu ─────────────────────────────────────
//
// Each request: a typed parameter struct decoded strictly, validation
// against closed sets, then a read, a Node Update control or the start of a
// job. Deadlines bound the connection's socket I/O. A job's
// work runs in its own service, so none needs long.

type request struct {
	deadline time.Duration
	// quiet requests are polled every second while a screen watches a job,
	// so their successful answers are not logged.
	quiet  bool
	handle func(c *call, params json.RawMessage) (any, error)
}

// call is one connection's request.
type call struct {
	server *server
	uid    uint32
}

// errNothingChanged marks vpnd's own refusals of a request's parameters.
var errNothingChanged = errors.New("nothing was changed")

type unchanged struct{ err error }

func (e unchanged) Error() string   { return e.err.Error() }
func (e unchanged) Unwrap() []error { return []error{e.err, errNothingChanged} }

func refuse(err error) error { return unchanged{err} }

func menu() map[string]request {
	return map[string]request{
		api.VerbReadAccounts: {deadline: time.Minute, handle: readAccounts},
		api.VerbReadAccount:  {deadline: time.Minute, handle: readAccount},
		api.VerbReadSSHAuth:  {deadline: time.Minute, handle: readSSHAuth},
		api.VerbJobStatus:    {deadline: 30 * time.Second, quiet: true, handle: jobStatus},
		api.VerbStartJob:     {deadline: time.Minute, handle: startJob},
		api.VerbStartUpdate:  {deadline: time.Minute, handle: startUpdate},
		api.VerbResumeUpdate: {deadline: time.Minute, handle: resumeUpdate},
		api.VerbCancelUpdate: {deadline: time.Minute, handle: cancelUpdate},
	}
}

var (
	readLocalAccounts = host.ReadLocalAccounts
	readLocalAccount  = host.ReadLocalAccount
	store             = jobs.Default
	startUnit         = jobs.StartUnit
)

// decode unmarshals parameters strictly: an unknown field is an error, so a
// client and server drift surfaces as a refusal instead of an ignored option.
func decode(params json.RawMessage, into any) error {
	dec := json.NewDecoder(bytes.NewReader(params))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return refuse(fmt.Errorf("invalid parameters: %v", err))
	}
	return nil
}

func noParams(params json.RawMessage) error {
	if err := jobs.RejectParams(params); err != nil {
		return refuse(err)
	}
	return nil
}

// ── Reads ────────────────────────────────────────────────
//
// Display facts the TUI needs at human cadence, read from their source at
// the moment of the question, because they can change with no change of
// ours involved. They answer at once, also while a job runs, and get the
// same peer check and journal record as every other request.

func readAccounts(_ *call, params json.RawMessage) (any, error) {
	if err := noParams(params); err != nil {
		return nil, err
	}
	return readLocalAccounts()
}

func readAccount(_ *call, params json.RawMessage) (any, error) {
	var ref accountaccess.Ref
	if err := decode(params, &ref); err != nil {
		return nil, err
	}
	if err := ref.Validate(); err != nil {
		return nil, refuse(err)
	}
	return readLocalAccount(ref)
}

func readSSHAuth(_ *call, params json.RawMessage) (any, error) {
	if err := noParams(params); err != nil {
		return nil, err
	}
	// The operator configuration sampled at localhost. Errors stay unknown,
	// and source-specific Match rules may differ on a real connection.
	enabled, err := host.EffectiveSSHPasswordAuth()
	if err != nil {
		return nil, err
	}
	return api.SSHAuthResult{PasswordAuthEnabled: enabled}, nil
}

// ── Jobs ─────────────────────────────────────────────────

func jobStatus(c *call, params json.RawMessage) (any, error) {
	var p api.JobStatusParams
	if err := decode(params, &p); err != nil {
		return nil, err
	}
	switch {
	case p.Kind == api.KindNodeUpdate && p.ID == "":
		return nodeUpdateStatus(c)
	case p.ID != "" && p.Kind == "":
		job, err := store().Get(p.ID)
		if err != nil {
			return nil, err
		}
		return api.JobStatus{Found: true, Job: expand(job)}, nil
	case p.Kind != "" && p.ID == "":
		job, found, err := store().Latest(p.Kind)
		if err != nil || !found {
			return api.JobStatus{}, err
		}
		return api.JobStatus{Found: true, Job: expand(job)}, nil
	}
	return nil, refuse(errors.New("name one job ID or one kind"))
}

// expand gives a finished Prepare Update its review, kept in the update
// folder so the record stays small.
func expand(job api.Job) api.Job {
	if job.Kind != api.KindPrepareUpdate || job.State != api.JobDone {
		return job
	}
	var p jobs.PrepareResult
	if err := json.Unmarshal(job.Result, &p); err != nil {
		return job
	}
	review := protocol.Review{UpdateFirst: p.UpdateFirst}
	if p.Token != "" {
		saved, err := update.ReadReview(p.Token)
		if err != nil {
			job.State, job.Error = api.JobFailed, "the saved review cannot be read: "+err.Error()
			return job
		}
		review = saved
	}
	if raw, err := json.Marshal(review); err == nil {
		job.Result = raw
	}
	return job
}

// nodeUpdateStatus is the Node Update seen as a job. Its result is the
// update record's status, which Node Updates draws.
func nodeUpdateStatus(c *call) (any, error) {
	s, err := update.Status()
	if err != nil || s.ID == "" {
		return api.JobStatus{}, err
	}
	job := api.Job{ID: s.JobID, Kind: api.KindNodeUpdate, Title: "Node Update to v" + s.Version, State: api.JobStarted}
	switch {
	case s.Phase == "complete":
		job.State = api.JobDone
	case s.Phase == "canceled":
		job.State, job.NothingChanged = api.JobFailed, true
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	job.Result = raw
	// Once the worker has installed another program, this vpnd answers and
	// then exits, so the next connection runs the new one.
	if s.Phase == "complete" && s.Version != c.server.version {
		c.server.exitAfterUpdate()
	}
	return api.JobStatus{Found: true, Job: job}, nil
}

func startJob(c *call, params json.RawMessage) (any, error) {
	var p api.StartJobParams
	if err := decode(params, &p); err != nil {
		return nil, err
	}
	title, keep, rule, err := jobs.Validate(p.Kind, p.Params)
	if err != nil {
		return nil, refuse(err)
	}
	job, err := store().Accept(p.Kind, title, keep, c.uid, rule, func(id string) error {
		return startUnit(id, title)
	})
	if err != nil {
		if _, busy := errors.AsType[*jobs.BusyError](err); busy {
			return nil, err
		}
		// Every refusal of Accept comes before the job could change anything.
		return nil, refuse(err)
	}
	audit(`{"event":"job-accepted","id":%q,"kind":%q,"uid":%d}`, job.ID, job.Kind, c.uid)
	return api.StartedJob{ID: job.ID, Title: job.Title}, nil
}

// ── Node Update controls ─────────────────────────────────

func startUpdate(c *call, params json.RawMessage) (any, error) {
	var p api.UpdateApproval
	if err := decode(params, &p); err != nil {
		return nil, err
	}
	id, err := jobs.NewID()
	if err != nil {
		return nil, err
	}
	s := store()
	return update.Start(c.server.version, p.Token, id, func() error { return s.CheckBusy(jobs.RuleJobs) })
}

func resumeUpdate(_ *call, params json.RawMessage) (any, error) {
	var p api.UpdateResumeParams
	if err := decode(params, &p); err != nil {
		return nil, err
	}
	s := store()
	return update.Resume(p.ID, func() error { return s.CheckBusy(jobs.RuleJobs) })
}

func cancelUpdate(_ *call, params json.RawMessage) (any, error) {
	var p api.UpdateResumeParams
	if err := decode(params, &p); err != nil {
		return nil, err
	}
	return update.Cancel(p.ID)
}
