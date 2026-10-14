package host

import "errors"

// ErrNothingChanged marks a failure that came before an operation changed
// anything on the node. The helper passes it on, so the screen can say that
// nothing changed instead of warning that changes may have been applied.
var ErrNothingChanged = errors.New("nothing was changed")

// unchangedError keeps a failure's words and adds ErrNothingChanged.
type unchangedError struct{ err error }

func (e unchangedError) Error() string   { return e.err.Error() }
func (e unchangedError) Unwrap() []error { return []error{e.err, ErrNothingChanged} }

// nothingChanged marks err as a failure before any change.
func nothingChanged(err error) error { return unchangedError{err} }
