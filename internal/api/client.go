package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/virtpriv/node/internal/paths"
)

// ── The TUI's client ─────────────────────────────────────
//
// One request is one connection: dial vpnd's socket, write one request line,
// half-close, read one answer line. The socket's ownership (root:vpn 0660)
// authorizes the connection, and vpnd checks the peer's uid on every
// connection. There is no password and no token.
//
// vpnd answers connections in parallel, so a read never waits behind a job.
// A change answers once its job is accepted and started, and the client
// then reads the job's record. Cancellation stops local I/O only. An
// accepted job runs on.

// dialTimeout bounds connecting and writing the request.
const dialTimeout = 5 * time.Second

// answerTimeout bounds reading the answer when the caller set no deadline.
// The slowest answer is a credential copy waiting for LND's files.
const answerTimeout = 5 * time.Minute

// socket is vpnd's address. Tests point it elsewhere.
var socket = paths.VPNDSocket

// maxAnswerBytes bounds one answer line.
const maxAnswerBytes = 1 << 20

// unreachable is for a request that never reached vpnd. Nothing was
// started, so the repair is safe to suggest. A failed vpnd start with a
// request waiting leaves the socket unit failed and its node removed, and
// only a start of the socket brings it back.
func unreachable(verb string, err error) error {
	return notSent{fmt.Errorf(
		"%s: cannot reach vpnd. To repair, quit with ctrl+c and run: "+
			"sudo systemctl restart vpnd.socket, or sudo reboot. Cause: %v", verb, err)}
}

// noAnswer is for every other failure. The request may have been sent and a
// job may run, so this points at state and logs, never at a restart.
func noAnswer(verb string, err error) error {
	return fmt.Errorf(
		"%s: no answer from vpnd (%v). Check: systemctl status vpnd.socket, "+
			"then journalctl -t vpnd", verb, err)
}

// Call sends one request and decodes the answer's result into result, which
// may be nil. The context's deadline bounds the whole request.
func Call(ctx context.Context, verb string, params, result any) error {
	req := Request{API: Version, Verb: verb}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("%s: encode params: %w", verb, err)
		}
		req.Params = raw
	}
	line, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("%s: encode request: %w", verb, err)
	}
	d := net.Dialer{Timeout: dialTimeout}
	c, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		if ctx.Err() != nil {
			// The caller gave up, which says nothing about vpnd.
			return noAnswer(verb, err)
		}
		return unreachable(verb, err)
	}
	conn, ok := c.(*net.UnixConn)
	if !ok { // cannot happen for a unix dial, a safety check only
		c.Close()
		return noAnswer(verb, errors.New("not a unix socket connection"))
	}
	return exchange(ctx, conn, verb, line, result)
}

// exchange writes the request on an open connection and reads its answer.
func exchange(ctx context.Context, conn net.Conn, verb string, line []byte, result any) error {
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(answerTimeout)
	}
	if err := conn.SetWriteDeadline(time.Now().Add(dialTimeout)); err != nil {
		return noAnswer(verb, err)
	}
	if _, err := conn.Write(append(line, '\n')); err != nil {
		return noAnswer(verb, err)
	}
	// Half-close the sending side, so vpnd reads a clean end after the
	// request line.
	if uc, ok := conn.(*net.UnixConn); ok {
		if err := uc.CloseWrite(); err != nil {
			return noAnswer(verb, err)
		}
	}
	if err := conn.SetReadDeadline(deadline); err != nil {
		return noAnswer(verb, err)
	}
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64*1024), maxAnswerBytes)
	if !sc.Scan() {
		err := sc.Err()
		if err == nil {
			err = errors.New("connection closed before an answer")
		}
		return noAnswer(verb, err)
	}
	var a Answer
	if err := json.Unmarshal(sc.Bytes(), &a); err != nil {
		return fmt.Errorf("%s: malformed answer from vpnd: %w", verb, err)
	}
	if !a.OK {
		return &AnswerError{Message: a.Error, Code: a.Code, Job: a.Job}
	}
	if result != nil && len(a.Result) > 0 {
		if err := json.Unmarshal(a.Result, result); err != nil {
			return fmt.Errorf("%s: decode answer from vpnd: %w", verb, err)
		}
	}
	return nil
}

// AnswerError is a refusal or failure vpnd reported. Its text is vpnd's own
// words. Job names the job in the way of a busy refusal.
type AnswerError struct {
	Message string
	Code    string
	Job     *Job
}

func (e *AnswerError) Error() string { return e.Message }

// notSent marks a request that never reached vpnd.
type notSent struct{ err error }

func (e notSent) Error() string { return e.err.Error() }
func (e notSent) Unwrap() error { return e.err }

// NothingChanged reports whether err says for certain that the request
// changed nothing, because vpnd said so or the request never reached it. A
// lost answer says nothing either way.
func NothingChanged(err error) bool {
	if e, ok := errors.AsType[*AnswerError](err); ok {
		return e.Code == CodeNothingChanged || e.Code == CodeBusy || e.Code == CodeUnsupportedAPI
	}
	_, ok := errors.AsType[notSent](err)
	return ok
}
