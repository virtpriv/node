// internal/helperd/helperd.go

// Package helperd is the root side of the node's privilege
// boundary: `vpn helperd`, a socket-activated service that
// executes a fixed menu of privileged operations ("verbs") for
// the unprivileged admin user.
//
// The trust model, in one breath: systemd owns the unix socket
// at /run/vpn-helperd.sock and creates its node root:vpn 0660
// before this process ever runs, so only root and the admin
// user can connect — the socket's permissions are the
// authentication. On top of that, every accepted connection is
// re-identified with SO_PEERCRED (a kernel statement about the
// connecting process, not a client claim) and refused unless
// the peer uid is the admin user or root. What a connection can
// then do is bounded by the verb menu: there is no verb that
// runs a caller-supplied command line and no verb that returns
// a caller-chosen file.
//
// Lifecycle: systemd starts this process when a connection
// arrives and passes it the listening socket. Connections are
// served ONE AT A TIME — privileged mutations are serialized by
// construction, and a second client simply waits in the
// kernel's queue. After 120 seconds with no connection the
// process exits; the socket stays, and the next connection
// starts a fresh helper. The timer only ever runs between
// connections, so no in-flight operation can be cut short by
// it. 120 seconds is an engineering constant, not a security
// control: it sits far above systemd's default start-rate
// limit (5 starts per 10 seconds) so that normal use can never
// trip rate limiting — at worst the helper is started once per
// idle period — while a quiet box still runs no resident root
// process. See the unit files the installer writes for the
// systemd half of this arrangement.
//
// Every verb call and outcome is logged to stdout, which
// journald captures under the vpn-helperd unit. The admin user
// can read that record (systemd-journal group) but cannot
// rewrite it — the audit trail of privileged operations
// survives a compromise of the admin account. Parameters are
// never logged: some carry secrets.
package helperd

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/user"
	"strconv"
	"syscall"
	"time"

	"github.com/virtpriv/node/internal/helper"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/update"
)

const (
	// idleExit is how long the helper waits for a connection
	// before exiting. Chosen against systemd's start-rate
	// limit (see the package comment); not a security dial.
	idleExit = 120 * time.Second

	// headerTimeout bounds reading the request line, so a
	// client that connects and stalls cannot wedge the
	// serialized helper.
	headerTimeout = 5 * time.Second

	// maxRequestBytes bounds the request line.
	maxRequestBytes = 1 << 20

	// listenFdsStart is where systemd-passed file descriptors
	// begin (the sd_listen_fds(3) protocol).
	listenFdsStart = 3
)

// Serve is the `vpn helperd` entry point. version is the
// running binary's version. Self-update independently checks newer ordering
// and major-version compatibility against it.
func Serve(version string) error {
	if os.Geteuid() != 0 {
		return errors.New(
			"vpn helperd runs as root via its systemd unit — " +
				"it is not meant to be started by hand")
	}
	// Non-interactive package operations for apt-running verbs
	// (same environment the installer uses).
	os.Setenv("DEBIAN_FRONTEND", "noninteractive")
	os.Setenv("NEEDRESTART_MODE", "a")

	ln, err := activationListener()
	if err != nil {
		return err
	}
	defer ln.Close()

	allowed, err := resolveAllowedUIDs()
	if err != nil {
		// Fail-noisy: a missing admin account means the install
		// is broken, not that everyone is welcome.
		return err
	}

	audit("start", `{"event":"start","version":%q}`, version)
	srv := &server{version: version, allowed: allowed}
	for {
		if err := ln.SetDeadline(time.Now().Add(idleExit)); err != nil {
			return fmt.Errorf("arm idle timer: %w", err)
		}
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				audit("idle", `{"event":"idle-exit"}`)
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		}
		uc, ok := conn.(*net.UnixConn)
		if !ok { // cannot happen on a unix listener
			conn.Close()
			continue
		}
		exit := srv.handleConn(uc)
		if exit {
			// A verb (self-update) asked the process to exit so
			// the next activation runs the new binary. Clients
			// lose nothing: connections queue on systemd's
			// socket while no helper runs.
			audit("exit", `{"event":"exit-for-update"}`)
			return nil
		}
	}
}

// activationListener returns the single listening unix socket
// passed by systemd socket activation. This is the only place
// the LISTEN_* protocol is read.
func activationListener() (*net.UnixListener, error) {
	// Unset-env hygiene regardless of outcome: children this
	// process execs (systemctl, apt-get, chpasswd) must never
	// see LISTEN_* or a child Go binary could mis-detect
	// activation.
	defer func() {
		os.Unsetenv("LISTEN_PID")
		os.Unsetenv("LISTEN_FDS")
		os.Unsetenv("LISTEN_FDNAMES")
	}()

	pidStr := os.Getenv("LISTEN_PID")
	if pidStr == "" {
		return nil, errors.New(
			"not socket-activated (LISTEN_PID unset) — start " +
				"vpn-helperd.socket and connect to it instead of " +
				"running helperd directly")
	}
	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		return nil, fmt.Errorf("bad LISTEN_PID %q: %v", pidStr, err)
	}
	if pid != os.Getpid() {
		// Environment addressed to another process; the fds are
		// not ours to consume.
		return nil, fmt.Errorf(
			"LISTEN_PID %d is not this process (%d)",
			pid, os.Getpid())
	}
	nfds, err := strconv.Atoi(os.Getenv("LISTEN_FDS"))
	if err != nil {
		return nil, fmt.Errorf("bad LISTEN_FDS: %v", err)
	}
	if nfds != 1 {
		return nil, fmt.Errorf(
			"want exactly 1 activated socket, got %d", nfds)
	}

	// systemd passes fds WITHOUT close-on-exec; set it before
	// anything can fork+exec, or a child could hold the
	// listener open past this helper's exit.
	syscall.CloseOnExec(listenFdsStart)

	f := os.NewFile(uintptr(listenFdsStart), "vpn-helperd.socket")
	if f == nil {
		return nil, errors.New("activated fd 3 is not valid")
	}
	// net.FileListener duplicates the fd; close our original
	// after the wrap.
	defer f.Close()

	ln, err := net.FileListener(f)
	if err != nil {
		return nil, fmt.Errorf("wrap activated socket: %w", err)
	}
	ul, ok := ln.(*net.UnixListener)
	if !ok {
		ln.Close()
		return nil, fmt.Errorf(
			"activated socket is %T, want unix", ln)
	}
	return ul, nil
}

// resolveAllowedUIDs resolves the peer uids allowed to issue
// verbs: root and the admin user. Resolved once at startup —
// resolving per connection would turn a passwd corruption into
// a fail-open ambiguity; once turns it into a loud start error.
func resolveAllowedUIDs() (map[uint32]bool, error) {
	u, err := user.Lookup(paths.AdminUser)
	if err != nil {
		return nil, fmt.Errorf(
			"resolve admin user %q: %w", paths.AdminUser, err)
	}
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return nil, fmt.Errorf(
			"admin uid %q not numeric: %v", u.Uid, err)
	}
	return map[uint32]bool{0: true, uint32(uid): true}, nil
}

// server carries per-process state into connection handling.
type server struct {
	version string
	allowed map[uint32]bool
}

// handleConn serves exactly one request on one connection.
// Returns true if the process should exit after this
// connection (self-update).
func (s *server) handleConn(c *net.UnixConn) (exitAfter bool) {
	defer c.Close()

	cred, err := peerCred(c)
	if err != nil {
		auditErr("peercred", `{"event":"reject","reason":"peercred failed: %v"}`, err)
		return false
	}
	if !s.allowed[cred.Uid] {
		// Unauthorized peers get silence, not a protocol
		// conversation.
		auditErr("reject", `{"event":"reject","uid":%d,"gid":%d,"pid":%d}`,
			cred.Uid, cred.Gid, cred.Pid)
		return false
	}

	if err := c.SetReadDeadline(
		time.Now().Add(headerTimeout)); err != nil {
		auditErr("deadline", `{"event":"error","detail":"arm header deadline: %v"}`, err)
		return false
	}
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 0, 64*1024), maxRequestBytes)
	if !sc.Scan() {
		auditErr("request", `{"event":"error","uid":%d,"detail":"request read: %v"}`,
			cred.Uid, sc.Err())
		return false
	}
	var req helper.Request
	if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
		writeEnd(c, &helper.Event{Event: "end",
			Error: "malformed request: " + err.Error()})
		auditErr("request", `{"event":"error","uid":%d,"detail":"malformed request"}`,
			cred.Uid)
		return false
	}

	def, ok := verbs[req.Verb]
	if !ok {
		writeEnd(c, &helper.Event{Event: "end",
			Error: fmt.Sprintf("unknown verb %q", req.Verb)})
		auditErr(req.Verb, `{"event":"error","uid":%d,"verb":%q,"detail":"unknown verb"}`,
			cred.Uid, req.Verb)
		return false
	}

	// One absolute deadline bounds this connection's SOCKET
	// I/O — every remaining read and every response write. It
	// does NOT reach into the verb's subprocesses: nothing is
	// waiting on the socket while a privileged command runs, so
	// a wedged subprocess is bounded by its own limits — the
	// download timeouts and retry caps in system.doDownload,
	// apt's Acquire timeout configuration, systemd's per-unit
	// stop timeouts — not by this deadline. Deadlines are per
	// verb (a package upgrade legitimately needs half an hour;
	// a password change does not).
	if err := c.SetDeadline(time.Now().Add(def.deadline)); err != nil {
		auditErr(req.Verb, `{"event":"error","detail":"arm verb deadline: %v"}`, err)
		return false
	}

	audit(req.Verb, `{"event":"verb","verb":%q,"uid":%d,"pid":%d}`,
		req.Verb, cred.Uid, cred.Pid)
	start := time.Now()

	ctx := &verbCtx{conn: c, version: s.version}
	if err := update.InstalledVersion(s.version); err != nil {
		writeEnd(c, &helper.Event{Event: "end", Error: err.Error()})
		return true
	}
	unlock, err := guardHelperOperation(req.Verb)
	if err != nil {
		writeEnd(c, &helper.Event{Event: "end", Error: err.Error()})
		return false
	}
	defer unlock()
	result, err := def.handler(ctx, req.Params)
	ms := time.Since(start).Milliseconds()
	if err != nil {
		writeEnd(c, &helper.Event{Event: "end", Error: err.Error()})
		auditErr(req.Verb, `{"event":"outcome","verb":%q,"uid":%d,"outcome":"error","ms":%d,"detail":%q}`,
			req.Verb, cred.Uid, ms, err.Error())
		return false
	}
	end := &helper.Event{Event: "end", OK: true}
	if result != nil {
		if raw, mErr := json.Marshal(result); mErr == nil {
			end.Result = raw
		}
	}
	writeEnd(c, end)
	audit(req.Verb, `{"event":"outcome","verb":%q,"uid":%d,"outcome":"ok","ms":%d}`,
		req.Verb, cred.Uid, ms)

	return ctx.exitAfterEnd
}

// verbCtx is what a verb handler gets to work with.
type verbCtx struct {
	conn    *net.UnixConn
	version string

	// exitAfterEnd makes the process exit after this
	// connection — the self-update contract: the next
	// activation runs the freshly installed binary.
	exitAfterEnd bool
}

// emitStep reports step i of a streaming verb as complete.
// Write failures are deliberately not fatal to the OPERATION:
// a client that vanished mid-stream does not get to abort a
// privileged mutation halfway — the operation finishes and the
// journal keeps the record; only the progress reporting is
// lost.
func (ctx *verbCtx) emitStep(i int) {
	if ctx.conn == nil {
		return
	}
	writeEvent(ctx.conn, &helper.Event{Event: "step", Index: i})
}

func writeEvent(c *net.UnixConn, ev *helper.Event) {
	raw, err := json.Marshal(ev)
	if err != nil {
		return
	}
	if _, err := c.Write(append(raw, '\n')); err != nil {
		auditErr("write", `{"event":"error","detail":"client write failed (operation continues): %v"}`, err)
	}
}

func writeEnd(c *net.UnixConn, ev *helper.Event) {
	writeEvent(c, ev)
}

// ── Audit output ─────────────────────────────────────────
//
// One line per record to stdout; journald stamps each with
// trusted metadata (_UID=0, _SYSTEMD_UNIT=vpn-helperd.service)
// that no client-side writer can forge. The "<3>" prefix marks
// error-priority records (journald parses and strips it).
// The tag argument exists only to keep call sites readable.

func audit(_ string, format string, args ...any) {
	fmt.Fprintf(os.Stdout, format+"\n", args...)
}

func auditErr(_ string, format string, args ...any) {
	fmt.Fprintf(os.Stdout, "<3>"+format+"\n", args...)
}
