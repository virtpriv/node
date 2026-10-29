package protocol

import (
	"fmt"
	"time"
	"unicode"
	"unicode/utf8"
)

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
	// StepStarts and StepEnds hold when each listed step began and when it
	// finished or failed, in the list's order, the zero time when it has not.
	Steps      []string    `json:"steps,omitempty"`
	StepNumber int         `json:"step_number,omitempty"`
	StepStarts []time.Time `json:"step_starts,omitempty"`
	StepEnds   []time.Time `json:"step_ends,omitempty"`
	Error      string      `json:"error,omitempty"`
	Failure    *Failure    `json:"failure,omitempty"`
	// Next and Advice are the worker's next step and words for a failure. An
	// installed screen uses them before its own, so a later release can lead
	// a failure this release does not know.
	Next          Next   `json:"next,omitempty"`
	Advice        string `json:"advice,omitempty"`
	Active        bool   `json:"active"`
	Running       bool   `json:"running"`
	Cancellable   bool   `json:"cancellable"`
	WalletNetwork string `json:"wallet_network,omitempty"`
}

// Next names the button a failure's advice leads to. A value keeps its
// meaning in every release. A worker sends a next step and its advice
// together, and the advice names only the button its next step leads to.
// An installed screen uses them only as a pair it can follow, else its own.
type Next string

const (
	// NextContinue leads to Continue, for a stop of normal use. The screen
	// draws it without an error.
	NextContinue Next = "continue"
	// NextRetry leads to Retry, for a failure.
	NextRetry Next = "retry"
	// NextCancel leads to Cancel update, for a failure before host changes
	// that the operator fixes outside the update first. Never after host
	// changes began: Cancel is no longer offered then.
	NextCancel Next = "cancel"
)

// Readable reports whether n has the shape an installed reader accepts. A
// later worker may send a value this release does not know, and the screen
// ignores it.
func (n Next) Readable() bool { return shortName(string(n), '_') }

// MaxAdvice is the most characters of advice an installed reader accepts.
const MaxAdvice = 400

// ReadableAdvice reports whether advice is plain text an installed screen
// may show: printable characters and line breaks, at most MaxAdvice.
func ReadableAdvice(advice string) bool {
	if advice == "" || !utf8.ValidString(advice) || utf8.RuneCountInString(advice) > MaxAdvice {
		return false
	}
	for _, r := range advice {
		if r != '\n' && (unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp)) {
			return false
		}
	}
	return true
}

// ServiceName is the name the System tab gives an updated service, and
// whether the System tab shows it.
func ServiceName(c Component) (string, bool) {
	switch c {
	case Bitcoin:
		return "bitcoind", true
	case LND, Syncthing:
		return string(c), true
	}
	return "", false
}

// Advise gives a failure's next step and the words Node Updates shows for
// it. The worker saves both with the failure, and an installed screen falls
// back to its own release's Advise when the worker sent none. A known code
// gets its exact next step, anything else whether the update had begun
// changing services: cancellable is true before host changes began. The
// sentence that names the button follows an empty line.
func Advise(f *Failure, cancellable bool) (Next, string) {
	if f != nil {
		name, onSystemTab := ServiceName(f.Service)
		switch f.Code {
		case FailureStopFailed:
			if f.Service == LND {
				return NextContinue, "This happens when the LND wallet is locked or not created yet.\n\nSelect Continue."
			}
		case FailureStartInterrupted:
			return NextContinue, "The update was interrupted, for example by a restart.\n\nSelect Continue."
		case FailureServiceNotRunning:
			if onSystemTab {
				return NextCancel, fmt.Sprintf("%s is not running.\n\nSelect Cancel update, start %s on the System tab, then review again.", name, name)
			}
		case FailureServiceChanged:
			if onSystemTab {
				return NextCancel, fmt.Sprintf("%s is not running the installed version.\n\nSelect Cancel update, restart %s on the System tab, then review again.", name, name)
			}
		}
	}
	if cancellable {
		return NextRetry, "No services were changed.\n\nSelect Retry, or Cancel update to leave the node as it was."
	}
	return NextRetry, "Services may have been changed.\n\nSelect Retry to try again."
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
