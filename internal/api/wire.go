// Package api is the vpn API: the requests vpnd accepts on its unix socket,
// the answers it gives, and the TUI's client for them.
//
// A connection carries one request line and one answer line. Reads answer at
// once. A change is a job: vpnd records it, starts it in its own systemd
// service and answers with its ID, and the client then reads the job's record
// until it ends. Every request names the API number its client speaks. Once
// a release carries a number, the API only grows within it: new requests,
// answer fields, job kinds and codes. Nothing is removed or changes meaning.
package api

import (
	"encoding/json"
	"time"
)

// Version is the API number this release speaks. MinVersion is the oldest
// number its vpnd still serves, so a TUI left open across an update keeps
// working. Number 1 was the API of the Bitcoin and Lightning node. Requests
// were removed after it, so it is not served.
const (
	Version    = 2
	MinVersion = 2
)

// Requests. vpnd serves this fixed menu and nothing else. No request runs a
// caller-supplied command or returns a caller-chosen file.
const (
	// Reads, answered at once, also while a job runs.
	VerbReadAccounts = "read-accounts"
	VerbReadAccount  = "read-account"
	VerbReadSSHAuth  = "read-ssh-auth"
	VerbJobStatus    = "job-status"

	// VerbStartJob starts a change. The answer names the job.
	VerbStartJob = "start-job"

	// Node Update controls, quick requests on the Node Update job.
	VerbStartUpdate  = "start-update"
	VerbResumeUpdate = "resume-update"
	VerbCancelUpdate = "cancel-update"
)

// Job kinds. Each runs in its own systemd service.
const (
	KindPackageUpdate    = "package-update"
	KindPrepareUpdate    = "prepare-update"
	KindServiceAction    = "service-action"
	KindReboot           = "reboot"
	KindSSHPasswordLogin = "ssh-password-login"

	// KindNodeUpdate is a Node Update seen from outside. Its work keeps its
	// own service and record in the update folder.
	KindNodeUpdate = "node-update"
)

// Request is the single line a client writes after connecting.
type Request struct {
	API    int             `json:"api"`
	Verb   string          `json:"verb"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Answer is vpnd's single line back. Code is set on a refusal or failure
// that changed nothing. Job names the job in the way of a busy refusal.
type Answer struct {
	OK     bool            `json:"ok"`
	Error  string          `json:"error,omitempty"`
	Code   string          `json:"code,omitempty"`
	Job    *Job            `json:"job,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
}

// Codes on an answer that is not OK. Each says nothing was changed.
const (
	CodeNothingChanged = "nothing_changed"
	// CodeBusy refuses a change while a job is unfinished.
	CodeBusy = "busy"
	// CodeUnsupportedAPI refuses a request whose API number vpnd does not
	// serve.
	CodeUnsupportedAPI = "unsupported_api"
)

// JobState is where a job is. Accepted and Started are unfinished.
type JobState string

const (
	JobAccepted JobState = "accepted"
	JobStarted  JobState = "started"
	JobDone     JobState = "done"
	JobFailed   JobState = "failed"
	// JobUnknown is a job that ended without a result, for example killed or
	// cut by a power cut. Its outcome is unknown and it is never retried.
	JobUnknown JobState = "unknown"
)

// Job is a job as its record tells it. Times are UTC.
type Job struct {
	ID    string   `json:"id"`
	Kind  string   `json:"kind"`
	Title string   `json:"title"`
	State JobState `json:"state"`
	// NothingChanged is set on a failed job only when nothing was changed for
	// certain.
	NothingChanged bool `json:"nothing_changed,omitempty"`
	// Restarted is set on a Reboot whose record was left unfinished by the
	// restart it asked for.
	Restarted bool      `json:"restarted,omitempty"`
	Accepted  time.Time `json:"accepted"`
	Started   time.Time `json:"started,omitzero"`
	Ended     time.Time `json:"ended,omitzero"`
	// Steps and StepNumber are the kind's steps and the one in progress,
	// counted from 1. StepStarts and StepEnds hold when each began and ended.
	Steps      []string    `json:"steps,omitempty"`
	StepNumber int         `json:"step_number,omitempty"`
	StepStarts []time.Time `json:"step_starts,omitempty"`
	StepEnds   []time.Time `json:"step_ends,omitempty"`
	Error      string      `json:"error,omitempty"`
	// Result is the kind's typed result, when it has one.
	Result json.RawMessage `json:"result,omitempty"`
}

// Finished reports whether the job has ended, with or without a result.
func (j Job) Finished() bool {
	return j.State == JobDone || j.State == JobFailed || j.State == JobUnknown
}

// ── Typed parameters and results ─────────────────────────
//
// Every value that crosses the privilege boundary is a field in one of
// these structs. vpnd decodes them strictly and validates each against a
// closed set.

// StartJobParams names the kind of change and its own parameters.
type StartJobParams struct {
	Kind   string          `json:"kind"`
	Params json.RawMessage `json:"params,omitempty"`
}

// StartedJob is the answer to a started job.
type StartedJob struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// JobStatusParams selects one job by ID, or the newest job of a kind.
type JobStatusParams struct {
	ID   string `json:"id,omitempty"`
	Kind string `json:"kind,omitempty"`
}

// JobStatus answers a job status read. Found is false when no job of the
// kind exists.
type JobStatus struct {
	Found bool `json:"found"`
	Job   Job  `json:"job"`
}

// ServiceActionParams: systemctl <action> <unit>, both from closed sets.
type ServiceActionParams struct {
	Unit   string `json:"unit"`
	Action string `json:"action"`
}

// RebootResult confirms systemd accepted the request, not that the host
// restarted.
type RebootResult struct {
	Accepted bool `json:"accepted"`
}

// SSHPasswordLoginParams: rewrite the SSH hardening drop-in. The template
// lives on the root side. The only caller-chosen value is this flag.
type SSHPasswordLoginParams struct {
	PasswordAuthDisabled bool `json:"password_auth_disabled"`
}

// PrepareUpdateParams selects a release for verification and review.
type PrepareUpdateParams struct {
	Version string `json:"version"`
}

// UpdateApproval binds Start to root-verified release bytes, not a mutable
// latest version hint or a caller-provided plan.
type UpdateApproval struct {
	Token string `json:"token"`
}

// UpdateResumeParams names the Node Update to retry or cancel.
type UpdateResumeParams struct {
	ID string `json:"id"`
}

// SSHAuthResult is whether sshd's effective configuration permits password
// login for the admin user, asked of sshd itself. It gates removing the last
// SSH key, so callers treat an error as unavailable and refuse that action.
type SSHAuthResult struct {
	PasswordAuthEnabled bool `json:"password_auth_enabled"`
}

// ── Step lists ───────────────────────────────────────────
//
// A kind's step names are fixed and known to both sides. The job writes the
// list into its record and marks each step as it goes.

// PackageUpdateStepNames are Update Packages' steps.
func PackageUpdateStepNames() []string {
	return []string{
		"Refreshing package lists",
		"Upgrading packages",
	}
}
