package protocol

// WorkerUnit is the systemd unit that runs an update. Its journal holds the
// full text of a worker failure.
const WorkerUnit = "vpn-update.service"

type Review struct {
	Token    string   `json:"token"`
	Digest   string   `json:"digest"`
	Manifest Manifest `json:"manifest"`
	Source   Versions `json:"source"`
	Network  string   `json:"network"`
	// UpdateFirst is guidance instead of a review: the selected release does
	// not admit this node, and its plan names this release to install first.
	// Such an answer has no token and cannot be approved.
	UpdateFirst string `json:"update_first,omitempty"`
	// KeyServerUnreachable tells the operator that the release key was
	// checked without the key server, so a revocation published only there
	// was not seen. It belongs to the review and is not kept in a job.
	KeyServerUnreachable bool `json:"key_server_unreachable,omitempty"`
}

// Status deliberately excludes local paths, credentials and raw command output.
type Status struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Phase   string `json:"phase"`
	Step    string `json:"step"`
	// Steps and StepNumber are the worker's list and the step in progress,
	// counted from 1. Both are empty when the record holds no usable list.
	Steps         []string `json:"steps,omitempty"`
	StepNumber    int      `json:"step_number,omitempty"`
	Error         string   `json:"error,omitempty"`
	Failure       *Failure `json:"failure,omitempty"`
	Active        bool     `json:"active"`
	Running       bool     `json:"running"`
	Cancellable   bool     `json:"cancellable"`
	WalletNetwork string   `json:"wallet_network,omitempty"`
}

// Failure names an update failure that Node Updates knows how to explain.
// The worker saves it beside the raw error. The installed screen maps a code
// it knows to the exact next step and gives its general advice for any other,
// so a later release may add codes. A code keeps its meaning in every release.
// A new meaning gets a new code.
type Failure struct {
	Code    FailureCode `json:"code"`
	Service Component   `json:"service,omitempty"`
}

// FailureCode is the short name of a known failure.
type FailureCode string

// The codes this release saves. Service names the service each one is about.
const (
	// FailureStopFailed means the service did not stop cleanly, and no new
	// files were installed.
	FailureStopFailed FailureCode = "stop_failed"
	// FailureStartInterrupted means a start was cut off, for example by a
	// restart, and the service is not running its new program.
	FailureStartInterrupted FailureCode = "start_interrupted"
	// FailureServiceNotRunning means the service was not active at the node
	// check.
	FailureServiceNotRunning FailureCode = "service_not_running"
	// FailureServiceChanged means the service was running a different
	// program than the installed one at the node check.
	FailureServiceChanged FailureCode = "service_changed"
)

// Readable reports whether f has the shape an installed reader accepts, which
// is lowercase names of at most 32 characters. Only the shape is checked,
// because a later worker may save codes and services this release does not
// know.
func (f Failure) Readable() bool {
	return shortName(string(f.Code), '_') && (f.Service == "" || shortName(string(f.Service), '-'))
}

func shortName(s string, sep byte) bool {
	if len(s) == 0 || len(s) > 32 || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := range len(s) {
		if c := s[i]; (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != sep {
			return false
		}
	}
	return true
}
