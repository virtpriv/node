package vpnd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/virtpriv/node/internal/accountaccess"
	"github.com/virtpriv/node/internal/api"
	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/host"
	"github.com/virtpriv/node/internal/jobs"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/system"
	"github.com/virtpriv/node/internal/update"
	"github.com/virtpriv/node/internal/update/files"
	"github.com/virtpriv/node/internal/update/protocol"
)

// ── The request menu ─────────────────────────────────────
//
// Each request: a typed parameter struct decoded strictly, validation
// against closed sets, then a read, a credential copy, a Node Update control
// or the start of a job. Deadlines bound the connection's socket I/O. A job's
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
		api.VerbReadAccounts:      {deadline: time.Minute, handle: readAccounts},
		api.VerbReadAccount:       {deadline: time.Minute, handle: readAccount},
		api.VerbReadNodeAddresses: {deadline: time.Minute, handle: readNodeAddresses},
		api.VerbReadSSHAuth:       {deadline: time.Minute, handle: readSSHAuth},
		api.VerbReadWalletState:   {deadline: time.Minute, handle: readWalletState},
		api.VerbDirSize:           {deadline: 2 * time.Minute, handle: dirSize},
		api.VerbJobStatus:         {deadline: 30 * time.Second, quiet: true, handle: jobStatus},
		api.VerbVersion:           {deadline: 10 * time.Second, handle: version},
		// The certificate copy waits for LND's files to settle.
		api.VerbStageLNDCredentials: {deadline: 3 * time.Minute, handle: stageLNDCredentials},
		api.VerbStageLNDMacaroon:    {deadline: time.Minute, handle: stageLNDMacaroon},
		api.VerbStartJob:            {deadline: time.Minute, handle: startJob},
		api.VerbStartUpdate:         {deadline: time.Minute, handle: startUpdate},
		api.VerbResumeUpdate:        {deadline: time.Minute, handle: resumeUpdate},
		api.VerbCancelUpdate:        {deadline: time.Minute, handle: cancelUpdate},
	}
}

// liveReadFacts pins the fourth freshness story (internal/jobs/matrix.go),
// facts with no board copy, served live by a read. The value is the request
// that serves the fact. vpnd_test.go checks each is on the menu, so retiring
// a read without a replacement story fails a test.
var liveReadFacts = map[string]string{
	"local-accounts":       api.VerbReadAccounts,
	"local-account-access": api.VerbReadAccount,
	"onion-addresses":      api.VerbReadNodeAddresses,
	"syncthing-device-id":  api.VerbReadNodeAddresses,
	"ssh-password-auth":    api.VerbReadSSHAuth,
	"wallet-existence":     api.VerbReadWalletState,
}

var (
	readLocalAccounts = host.ReadLocalAccounts
	readLocalAccount  = host.ReadLocalAccount
	walletExists      = host.WalletExists
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

func readNodeAddresses(_ *call, params json.RawMessage) (any, error) {
	if err := noParams(params); err != nil {
		return nil, err
	}
	onions := host.ReadOnionAddresses()
	return api.NodeAddressesResult{
		LNDGRPCOnion: onions.LNDGRPC, LNDRESTOnion: onions.LNDREST, SyncthingOnion: onions.Syncthing,
		// The certificate's identity is read even when the daemon is stopped.
		SyncthingDeviceID: host.SyncthingDeviceID(),
	}, nil
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

func readWalletState(_ *call, params json.RawMessage) (any, error) {
	if err := noParams(params); err != nil {
		return nil, err
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("read node config: %v, the node may not be installed", err)
	}
	wallet, err := walletExists(cfg.Network)
	if err != nil {
		return nil, err
	}
	return api.WalletStateResult{WalletExists: wallet}, nil
}

func dirSize(_ *call, params json.RawMessage) (any, error) {
	var p api.DirSizeParams
	if err := decode(params, &p); err != nil {
		return nil, err
	}
	// A closed set, not a path. Bitcoin Core reports its own size over RPC.
	if p.Which != "lnd" {
		return nil, refuse(fmt.Errorf("unknown folder %q", p.Which))
	}
	bytes, err := system.DirSize(paths.LNDDataDir)
	if err != nil {
		return nil, fmt.Errorf("could not measure the LND data folder: %w", err)
	}
	return api.DirSizeResult{Bytes: bytes}, nil
}

func version(c *call, params json.RawMessage) (any, error) {
	if err := noParams(params); err != nil {
		return nil, err
	}
	return api.VersionResult{Version: c.server.version, MinAPI: api.MinVersion, MaxAPI: api.Version}, nil
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
	title, keep, secret, rule, err := jobs.Validate(p.Kind, p.Params)
	if err != nil {
		return nil, refuse(err)
	}
	job, err := store().Accept(p.Kind, title, keep, c.uid, rule, func(id string) error {
		credential := ""
		if secret != "" {
			path, err := writeCredential(id, secret)
			if err != nil {
				return err
			}
			// systemd has made the job's own copy once systemd-run returns.
			defer os.Remove(path)
			credential = path
		}
		return startUnit(id, title, credential)
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

// writeCredential writes the auto-unlock password root only, for systemd's
// credential copy into the job's service. It never reaches a record, a
// command line or the journal.
func writeCredential(id, secret string) (string, error) {
	if err := files.Directory(paths.VPNDRuntimeDir, 0700); err != nil {
		return "", err
	}
	path := filepath.Join(paths.VPNDRuntimeDir, id)
	return path, files.Write(path, []byte(secret), 0600)
}

// clearCredentials removes passwords a vpnd that died left behind.
func clearCredentials() error {
	entries, err := os.ReadDir(paths.VPNDRuntimeDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.Remove(filepath.Join(paths.VPNDRuntimeDir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// ── Credential copies ────────────────────────────────────
//
// Copies of LND's own files to the staging board. Each write is a rename, so
// two at once leave one whole copy. They keep no record and are never
// refused as busy.

func stageLNDCredentials(_ *call, params json.RawMessage) (any, error) {
	if err := noParams(params); err != nil {
		return nil, err
	}
	return nil, jobs.Restage(api.VerbStageLNDCredentials)
}

// Wallet creation mints only the admin macaroon. The broader copy stays the
// repair path for an LND client that cannot know which file went stale.
func stageLNDMacaroon(_ *call, params json.RawMessage) (any, error) {
	if err := noParams(params); err != nil {
		return nil, err
	}
	return nil, jobs.Restage(api.VerbStageLNDMacaroon)
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
