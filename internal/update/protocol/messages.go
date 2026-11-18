package protocol

import (
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
	// UpdateFirst is guidance instead of a review: the selected release does
	// not admit this node, and its plan names this release to install first.
	// Such an answer has no token and cannot be approved.
	UpdateFirst string `json:"update_first,omitempty"`
	// KeyServerUnreachable tells the operator that the release key was
	// checked without the key server, so a revocation published only there
	// was not seen. It belongs to the review and is not kept in a job.
	KeyServerUnreachable bool `json:"key_server_unreachable,omitempty"`
	// FromFolder tells the operator that the release files came from the
	// releases folder, not a download.
	FromFolder bool `json:"from_folder,omitempty"`
}

// Status deliberately excludes local paths, credentials and raw command output.
type Status struct {
	ID string `json:"id"`
	// JobID is the Node Update's job ID in the vpn API.
	JobID   string `json:"job_id,omitempty"`
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
	// Next and Advice are the worker's next step and words for a failure. An
	// installed screen uses them before its own, so a later release can lead
	// a failure this release does not know.
	Next        Next   `json:"next,omitempty"`
	Advice      string `json:"advice,omitempty"`
	Active      bool   `json:"active"`
	Running     bool   `json:"running"`
	Cancellable bool   `json:"cancellable"`
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

// Advise gives a failure's next step and the words Node Updates shows for
// it. The worker saves both with the failure, and an installed screen falls
// back to its own release's Advise when the worker sent none. The words say
// whether the update had begun changing the machine: cancellable is true
// before it had. The sentence that names the button follows an empty line.
func Advise(cancellable bool) (Next, string) {
	if cancellable {
		return NextRetry, "Nothing was changed.\n\nSelect Retry, or Cancel update to leave the node as it was."
	}
	return NextRetry, "The update may have changed this machine.\n\nSelect Retry to try again."
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
