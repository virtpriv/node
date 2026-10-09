// internal/helper/client.go

package helper

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/virtpriv/node/internal/paths"
)

// ── Helper client ────────────────────────────────────────
//
// One privileged operation = one connection: dial the helper's
// socket, write one request line, half-close, then read
// progress events until the ok/error terminator. The socket's
// ownership (root:vpn 0660) is what authorizes the connection.
// There is no password and no token: being the admin user IS
// the credential, and the helper re-checks the peer's uid on
// every connection.
//
// The helper serializes execution: while it runs one operation,
// a second connection waits its turn in the kernel's queue.
// Root-side deadlines bound socket I/O, not synchronous handler completion.
// Caller cancellation stops local I/O. Accepted mutations can continue.

const dialTimeout = 5 * time.Second

// helperSocket is the helper's address. Tests point it elsewhere.
var helperSocket = paths.HelperSocket

// maxEventBytes bounds one response line. Events are small, the
// bound exists so a defect can't balloon client memory.
const maxEventBytes = 1 << 20

// helperClosed is for a request that never reached the helper. Nothing was
// started, so the repair is safe to suggest. A failed helper start with a
// request waiting leaves the socket unit failed and its node removed, and only
// a start of the socket brings it back.
func helperClosed(op string, err error) error {
	return fmt.Errorf(
		"%s: cannot reach the node's helper. To repair, quit with ctrl+c "+
			"and run: sudo systemctl restart vpn-helperd.socket, or sudo reboot. "+
			"Cause: %v", op, err)
}

// helperDown is for every other failure. The request may have been sent and
// the helper may still be working on it, so this points at state and logs,
// never at a restart or a reboot.
func helperDown(op string, err error) error {
	return fmt.Errorf(
		"%s: cannot reach the node's helper service (%v). "+
			"Check: systemctl status vpn-helperd.socket, then "+
			"journalctl -u vpn-helperd", op, err)
}

// Session is one in-flight helper request. Callers either
// drain it with Wait (simple verbs) or step through it with
// WaitStep + Wait (streaming verbs feeding a step renderer).
type Session struct {
	conn    net.Conn
	scanner *bufio.Scanner
	verb    string

	end        *Event // terminator, once seen
	stepOK     map[int]bool
	err        error // sticky transport/protocol error
	closeOnce  sync.Once
	stopCancel func() bool
	cancelDone chan struct{}
}

// Start dials the helper and sends the request. It returns
// immediately after the request is written. The operation runs
// (and possibly queues) on the root side.
func Start(verb string, params any) (*Session, error) {
	return StartContext(context.Background(), verb, params)
}

// StartContext owns the connection until Wait or Close. Cancellation stops local
// I/O, including a blocked progress read. It cannot undo an accepted mutation.
func StartContext(ctx context.Context, verb string, params any) (*Session, error) {
	d := net.Dialer{Timeout: dialTimeout}
	c, err := d.DialContext(ctx, "unix", helperSocket)
	if err != nil {
		if ctx.Err() != nil {
			// The caller gave up, which says nothing about the helper.
			return nil, helperDown(verb, err)
		}
		return nil, helperClosed(verb, err)
	}
	conn, ok := c.(*net.UnixConn)
	if !ok { // cannot happen for a unix dial, a safety check only
		c.Close()
		return nil, helperDown(verb,
			errors.New("not a unix socket connection"))
	}

	s := newSession(ctx, conn, verb)
	success := false
	defer func() {
		if !success {
			s.Close()
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	req := Request{Verb: verb}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("%s: encode params: %w", verb, err)
		}
		req.Params = raw
	}
	line, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("%s: encode request: %w", verb, err)
	}
	if err := conn.SetWriteDeadline(
		time.Now().Add(dialTimeout)); err != nil {
		return nil, helperDown(verb, err)
	}
	defer conn.SetWriteDeadline(time.Time{})
	if _, err := conn.Write(append(line, '\n')); err != nil {
		return nil, helperDown(verb, err)
	}
	// Half-close our sending side: the helper reads a clean EOF
	// after the request line even if a future client forgets
	// the trailing newline.
	if err := conn.CloseWrite(); err != nil {
		return nil, helperDown(verb, err)
	}

	success = true
	return s, nil
}

func newSession(ctx context.Context, conn net.Conn, verb string) *Session {
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64*1024), maxEventBytes)
	s := &Session{conn: conn, scanner: sc, verb: verb, stepOK: map[int]bool{}, cancelDone: make(chan struct{})}
	s.stopCancel = context.AfterFunc(ctx, func() {
		conn.Close()
		close(s.cancelDone)
	})
	return s
}

// readEvent reads the next response line. Returns nil at
// end-of-stream (which is only legitimate after a terminator).
func (s *Session) readEvent() (*Event, error) {
	if !s.scanner.Scan() {
		if err := s.scanner.Err(); err != nil {
			return nil, helperDown(s.verb, err)
		}
		return nil, nil // EOF
	}
	var ev Event
	if err := json.Unmarshal(s.scanner.Bytes(), &ev); err != nil {
		return nil, fmt.Errorf(
			"%s: malformed helper response: %w", s.verb, err)
	}
	return &ev, nil
}

// advance consumes events until the wanted step index has
// completed or the stream terminates.
func (s *Session) advance(untilStep int) error {
	if s.err != nil {
		return s.err
	}
	for {
		if untilStep >= 0 && s.stepOK[untilStep] {
			return nil
		}
		if s.end != nil {
			return s.endErr(untilStep)
		}
		ev, err := s.readEvent()
		if err != nil {
			s.err = err
			return err
		}
		if ev == nil {
			// Stream ended without a terminator: the helper died
			// mid-operation or its deadline expired.
			s.err = helperDown(s.verb, errors.New(
				"connection closed before the operation finished"))
			return s.err
		}
		switch ev.Event {
		case "step":
			if ev.Err != "" {
				s.err = fmt.Errorf("%s", ev.Err)
				return s.err
			}
			s.stepOK[ev.Index] = true
		case "end":
			s.end = ev
			if untilStep < 0 {
				return s.endErr(untilStep)
			}
		default:
			// Unknown event kinds are ignored: additive protocol
			// growth must not break older clients.
		}
	}
}

// endErr folds the terminator into an error (or nil).
func (s *Session) endErr(untilStep int) error {
	if s.end.OK {
		if untilStep >= 0 && !s.stepOK[untilStep] {
			// Terminated OK without reporting the step the caller
			// is waiting on, a protocol drift bug.
			s.err = fmt.Errorf(
				"%s: helper finished without reporting step %d",
				s.verb, untilStep+1)
			return s.err
		}
		return nil
	}
	s.err = errors.New(s.end.Error)
	return s.err
}

// WaitStep blocks until step i (0-based) completes on the root
// side, a step fails, or the stream ends in an error.
func (s *Session) WaitStep(i int) error {
	return s.advance(i)
}

// Wait blocks until the terminator, decoding its result payload
// into result (which may be nil), and closes the connection.
func (s *Session) Wait(result any) error {
	defer s.Close()
	if err := s.advance(-1); err != nil {
		return err
	}
	if result != nil && len(s.end.Result) > 0 {
		if err := json.Unmarshal(s.end.Result, result); err != nil {
			return fmt.Errorf(
				"%s: decode helper result: %w", s.verb, err)
		}
	}
	return nil
}

// Close stops observation and releases the connection. The helper deliberately
// continues an accepted mutation after disconnection. Close is not rollback.
// It may run concurrently with WaitStep or Wait. Reads must have one owner.
func (s *Session) Close() {
	s.closeOnce.Do(func() {
		if s.stopCancel != nil && !s.stopCancel() {
			<-s.cancelDone
		}
		s.conn.Close()
	})
}

// Call performs a whole verb in one blocking call: start, drain
// any progress events, decode the terminator. For streaming
// verbs it simply ignores per-step progress.
func Call(verb string, params, result any) error {
	s, err := Start(verb, params)
	if err != nil {
		return err
	}
	return s.Wait(result)
}
