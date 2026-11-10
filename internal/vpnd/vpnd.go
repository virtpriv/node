// Package vpnd is the root side of the node's privilege boundary, `vpn
// vpnd`, a socket-activated service that serves the vpn API to the admin
// user.
//
// The trust model in one breath: systemd owns the unix socket at
// /run/vpnd.sock and creates its node root:vpn 0660 before this process ever
// runs, so only root and the admin user can connect. The socket's
// permissions are the authentication. On top of that, every connection is
// identified again with SO_PEERCRED (a kernel statement about the connecting
// process, not a client claim) and refused unless the peer is the admin user
// or root. What a connection can then do is bounded by the request menu. No
// request runs a caller-supplied command line or returns a caller-chosen
// file.
//
// Lifecycle: systemd starts this process when a connection arrives and
// passes it the listening socket. vpnd answers connections in parallel, so a
// read never waits behind another request. A change never runs here: vpnd
// records it as a job and starts it in its own systemd service, which runs
// on when vpnd exits. After 120 seconds with no connection open and none
// arriving, vpnd exits. The socket stays, and the next connection starts a
// fresh vpnd. 120 seconds is an engineering constant, not a security
// control. It sits far above systemd's default start rate limit (5 starts
// per 10 seconds), while a quiet node still runs no resident root process.
//
// Every request and outcome is logged to standard output, which journald
// keeps under the name vpnd. The admin user can read that record
// (systemd-journal group) but cannot rewrite it. Parameters are never
// logged, because some carry secrets.
package vpnd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/user"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/virtpriv/node/internal/api"
	"github.com/virtpriv/node/internal/host"
	"github.com/virtpriv/node/internal/jobs"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/update"
)

const (
	// idleExit is how long vpnd waits with no connection before it exits.
	idleExit = 120 * time.Second

	// headerTimeout bounds reading the request line, so a client that
	// connects and stalls holds nothing for long.
	headerTimeout = 5 * time.Second

	// maxRequestBytes bounds the request line.
	maxRequestBytes = 1 << 20

	// maxOpen bounds the connections answered at once. More are closed at
	// once, so stalled clients cannot hold root memory.
	maxOpen = 64

	// listenFdsStart is where systemd-passed file descriptors begin (the
	// sd_listen_fds(3) protocol).
	listenFdsStart = 3
)

// Serve is the `vpn vpnd` entry point. version is the running program's
// version.
func Serve(version string) error {
	if os.Geteuid() != 0 {
		return errors.New(
			"vpn vpnd runs as root through its systemd unit " +
				"and is not meant to be started by hand")
	}
	ln, err := activationListener()
	if err != nil {
		return err
	}
	defer ln.Close()
	allowed, err := resolveAllowedUIDs()
	if err != nil {
		// A missing admin account means the install is broken, not that
		// everyone is welcome.
		return err
	}
	// A password left by a vpnd that died between writing it and removing
	// it. /run is memory only, so none survives a restart.
	if err := clearCredentials(); err != nil {
		return err
	}
	audit(`{"event":"start","version":%q,"api":[%d,%d]}`, version, api.MinVersion, api.Version)
	s := &server{version: version, allowed: allowed, menu: menu()}
	s.serve(ln)
	return nil
}

// server carries per-process state into connection handling. The menu and
// the allowed accounts are read only after start, so parallel answers share
// no mutable state but the counters below.
type server struct {
	version string
	allowed map[uint32]bool
	menu    map[string]request

	open     atomic.Int32
	exitNext atomic.Bool
	ln       *net.UnixListener
	wg       sync.WaitGroup
}

// serve accepts connections until vpnd is idle or asked to exit after an
// update, then lets open answers finish.
func (s *server) serve(ln *net.UnixListener) {
	s.ln = ln
	defer s.wg.Wait()
	for {
		if s.exitNext.Load() {
			audit(`{"event":"exit-after-update"}`)
			return
		}
		if err := ln.SetDeadline(time.Now().Add(idleExit)); err != nil {
			auditErr(`{"event":"error","detail":"arm idle timer: %v"}`, err)
			return
		}
		conn, err := ln.Accept()
		if errors.Is(err, os.ErrDeadlineExceeded) {
			if s.exitNext.Load() {
				continue
			}
			if s.open.Load() == 0 {
				audit(`{"event":"idle-exit"}`)
				return
			}
			continue
		}
		if err != nil {
			auditErr(`{"event":"error","detail":"accept: %v"}`, err)
			return
		}
		uc, ok := conn.(*net.UnixConn)
		if !ok { // cannot happen on a unix listener
			conn.Close()
			continue
		}
		if s.open.Add(1) > maxOpen {
			s.open.Add(-1)
			auditErr(`{"event":"reject","reason":"too many open connections"}`)
			uc.Close()
			continue
		}
		s.wg.Go(func() {
			defer s.open.Add(-1)
			s.handle(uc)
		})
	}
}

// exitAfterUpdate makes vpnd stop accepting, finish its open answers and
// exit, so the next connection starts the newly installed program.
func (s *server) exitAfterUpdate() {
	s.exitNext.Store(true)
	if s.ln != nil {
		s.ln.SetDeadline(time.Now())
	}
}

// activationListener returns the single listening unix socket passed by
// systemd socket activation. This is the only place the LISTEN_* protocol
// is read.
func activationListener() (*net.UnixListener, error) {
	// Whatever the outcome, the programs vpnd runs never see LISTEN_*, or a
	// child Go program could think it was activated.
	defer func() {
		os.Unsetenv("LISTEN_PID")
		os.Unsetenv("LISTEN_FDS")
		os.Unsetenv("LISTEN_FDNAMES")
	}()
	pidStr := os.Getenv("LISTEN_PID")
	if pidStr == "" {
		return nil, errors.New(
			"not socket-activated (LISTEN_PID unset), start " +
				"vpnd.socket and connect to it instead of " +
				"running vpnd directly")
	}
	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		return nil, fmt.Errorf("bad LISTEN_PID %q: %v", pidStr, err)
	}
	if pid != os.Getpid() {
		// Addressed to another process. The descriptors are not ours.
		return nil, fmt.Errorf("LISTEN_PID %d is not this process (%d)", pid, os.Getpid())
	}
	nfds, err := strconv.Atoi(os.Getenv("LISTEN_FDS"))
	if err != nil {
		return nil, fmt.Errorf("bad LISTEN_FDS: %v", err)
	}
	if nfds != 1 {
		return nil, fmt.Errorf("want exactly 1 activated socket, got %d", nfds)
	}
	// systemd passes descriptors without close-on-exec. Set it before
	// anything can start a program, or a child could hold the listener open
	// past vpnd's exit.
	syscall.CloseOnExec(listenFdsStart)
	f := os.NewFile(uintptr(listenFdsStart), "vpnd.socket")
	if f == nil {
		return nil, errors.New("activated fd 3 is not valid")
	}
	// net.FileListener duplicates the descriptor. Close the original after.
	defer f.Close()
	ln, err := net.FileListener(f)
	if err != nil {
		return nil, fmt.Errorf("wrap activated socket: %w", err)
	}
	ul, ok := ln.(*net.UnixListener)
	if !ok {
		ln.Close()
		return nil, fmt.Errorf("activated socket is %T, want unix", ln)
	}
	return ul, nil
}

// resolveAllowedUIDs resolves the accounts allowed to make requests, root
// and the admin user, once at start. Resolving per connection would turn a
// passwd corruption into an ambiguity that fails open. Once at start, it is
// a loud start error.
func resolveAllowedUIDs() (map[uint32]bool, error) {
	u, err := user.Lookup(paths.AdminUser)
	if err != nil {
		return nil, fmt.Errorf("resolve admin user %q: %w", paths.AdminUser, err)
	}
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("admin uid %q not numeric: %v", u.Uid, err)
	}
	return map[uint32]bool{0: true, uint32(uid): true}, nil
}

// handle serves exactly one request on one connection.
func (s *server) handle(c *net.UnixConn) {
	defer c.Close()
	cred, err := peerCred(c)
	if err != nil {
		auditErr(`{"event":"reject","reason":"peercred failed: %v"}`, err)
		return
	}
	if !s.allowed[cred.Uid] {
		// Unauthorized peers get silence, not a protocol conversation.
		auditErr(`{"event":"reject","uid":%d,"gid":%d,"pid":%d}`, cred.Uid, cred.Gid, cred.Pid)
		return
	}
	if err := c.SetReadDeadline(time.Now().Add(headerTimeout)); err != nil {
		auditErr(`{"event":"error","detail":"arm header deadline: %v"}`, err)
		return
	}
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 0, 64*1024), maxRequestBytes)
	if !sc.Scan() {
		auditErr(`{"event":"error","uid":%d,"detail":"request read: %v"}`, cred.Uid, sc.Err())
		return
	}
	// Requests are decoded strictly: a field vpnd does not know is refused,
	// so a client asking for more than this vpnd serves learns it at once.
	var req api.Request
	dec := json.NewDecoder(bytes.NewReader(sc.Bytes()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		answer(c, refusal(api.CodeNothingChanged, "malformed request: "+err.Error()))
		auditErr(`{"event":"error","uid":%d,"detail":"malformed request"}`, cred.Uid)
		return
	}
	if req.API < api.MinVersion || req.API > api.Version {
		answer(c, refusal(api.CodeUnsupportedAPI, "VPN was updated. Quit and run vpn to open it again."))
		auditErr(`{"event":"reject","uid":%d,"verb":%q,"api":%d,"reason":"unsupported api"}`, cred.Uid, req.Verb, req.API)
		return
	}
	def, ok := s.menu[req.Verb]
	if !ok {
		answer(c, refusal(api.CodeNothingChanged, fmt.Sprintf("unknown request %q", req.Verb)))
		auditErr(`{"event":"error","uid":%d,"verb":%q,"detail":"unknown request"}`, cred.Uid, req.Verb)
		return
	}
	// One deadline bounds the rest of this connection's socket I/O. A job's
	// work never runs here, so no request needs long.
	if err := c.SetDeadline(time.Now().Add(def.deadline)); err != nil {
		auditErr(`{"event":"error","detail":"arm request deadline: %v"}`, err)
		return
	}
	// A vpnd left in memory after an update must not apply older rules.
	if err := update.InstalledVersion(s.version); err != nil {
		answer(c, refusal(api.CodeNothingChanged, err.Error()))
		s.exitAfterUpdate()
		return
	}
	start := time.Now()
	result, err := def.handle(&call{server: s, uid: cred.Uid}, req.Params)
	ms := time.Since(start).Milliseconds()
	if err != nil {
		answer(c, failure(err))
		auditErr(`{"event":"outcome","verb":%q,"uid":%d,"outcome":"error","ms":%d,"detail":%q}`,
			req.Verb, cred.Uid, ms, err.Error())
		return
	}
	a := &api.Answer{OK: true}
	if result != nil {
		raw, err := json.Marshal(result)
		if err != nil {
			answer(c, failure(err))
			return
		}
		a.Result = raw
	}
	answer(c, a)
	if !def.quiet {
		audit(`{"event":"outcome","verb":%q,"uid":%d,"outcome":"ok","ms":%d}`, req.Verb, cred.Uid, ms)
	}
}

func answer(c *net.UnixConn, a *api.Answer) {
	raw, err := json.Marshal(a)
	if err != nil {
		return
	}
	if _, err := c.Write(append(raw, '\n')); err != nil {
		auditErr(`{"event":"error","detail":"client write failed: %v"}`, err)
	}
}

// refusal answers a request refused before it ran, so nothing was changed.
func refusal(code, message string) *api.Answer {
	return &api.Answer{Error: message, Code: code}
}

// failure answers a request that failed. It says that nothing was changed
// only when that is certain.
func failure(err error) *api.Answer {
	a := &api.Answer{Error: err.Error()}
	if busy, ok := errors.AsType[*jobs.BusyError](err); ok {
		a.Code, a.Job = api.CodeBusy, &busy.Job
	} else if errors.Is(err, host.ErrNothingChanged) || errors.Is(err, errNothingChanged) {
		a.Code = api.CodeNothingChanged
	}
	return a
}

// ── Audit output ─────────────────────────────────────────
//
// One line per record on standard output. journald stamps each with
// trusted metadata (_UID=0, the unit) that no client can forge. The "<3>"
// prefix marks error records (journald reads and strips it).

func audit(format string, args ...any) {
	fmt.Fprintf(os.Stdout, format+"\n", args...)
}

func auditErr(format string, args ...any) {
	fmt.Fprintf(os.Stdout, "<3>"+format+"\n", args...)
}
