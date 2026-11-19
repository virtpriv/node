package vpnd

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/virtpriv/node/internal/api"
	"github.com/virtpriv/node/internal/host"
	"github.com/virtpriv/node/internal/jobs"
)

// activationListener must refuse to run outside socket activation, and must
// refuse environment addressed to another process: consuming another
// process's descriptors would be wrong in both directions.
func TestActivationListenerRefusals(t *testing.T) {
	unset := func() {
		os.Unsetenv("LISTEN_PID")
		os.Unsetenv("LISTEN_FDS")
		os.Unsetenv("LISTEN_FDNAMES")
	}
	unset()
	t.Cleanup(unset)

	if _, err := activationListener(); err == nil {
		t.Error("no LISTEN_PID: expected error")
	}
	os.Setenv("LISTEN_PID", "1") // not us
	os.Setenv("LISTEN_FDS", "1")
	if _, err := activationListener(); err == nil {
		t.Error("foreign LISTEN_PID: expected error")
	}
	// The refusal must still have cleared the environment, so no child of
	// ours can think it was activated.
	if os.Getenv("LISTEN_PID") != "" {
		t.Error("LISTEN_PID not cleared")
	}
	os.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid()))
	os.Setenv("LISTEN_FDS", "2") // wrong count
	if _, err := activationListener(); err == nil {
		t.Error("two fds: expected error")
	}
	os.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid()))
	os.Setenv("LISTEN_FDS", "junk")
	if _, err := activationListener(); err == nil {
		t.Error("junk LISTEN_FDS: expected error")
	}
}

// The menu is closed: exactly the ruled requests, nothing else. Adding one
// must be a deliberate act that updates this list too.
func TestMenuIsExactlyTheRuledSet(t *testing.T) {
	want := []string{
		api.VerbReadAccounts, api.VerbReadAccount, api.VerbReadSSHAuth,
		api.VerbJobStatus, api.VerbStartJob,
		api.VerbStartUpdate, api.VerbResumeUpdate, api.VerbCancelUpdate,
	}
	m := menu()
	if len(m) != len(want) {
		t.Errorf("menu has %d entries, want %d", len(m), len(want))
	}
	for _, v := range want {
		def, ok := m[v]
		if !ok {
			t.Errorf("menu is missing %s", v)
		} else if def.deadline <= 0 || def.handle == nil {
			t.Errorf("%s has no deadline or handler", v)
		}
	}
}

// A failure tells the client that nothing changed only when that is
// certain. vpnd's words reach the client unchanged either way.
func TestFailureCarriesTheCodeOnlyWhenNothingChanged(t *testing.T) {
	marked := fmt.Errorf("the change was refused: %w", host.ErrNothingChanged)
	plain := errors.New("a step failed")
	busy := &jobs.BusyError{Job: api.Job{ID: "20261109-100000-aaaaaa", Title: "Reboot"}}
	for _, tc := range []struct {
		err  error
		code string
	}{{marked, api.CodeNothingChanged}, {refuse(plain), api.CodeNothingChanged}, {plain, ""}, {busy, api.CodeBusy}} {
		a := failure(tc.err)
		if a.OK || a.Error != tc.err.Error() || a.Code != tc.code {
			t.Fatalf("answer %+v for %v", a, tc.err)
		}
	}
}

// pair is a connected socket pair whose server end carries kernel peer
// credentials, without a socket file.
func pair(t *testing.T) (client net.Conn, server *net.UnixConn) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("kernel peer credentials require Linux")
	}
	dir, err := os.MkdirTemp("", "vd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() { c, _ := ln.Accept(); accepted <- c }()
	client, err = net.Dial("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	server = (<-accepted).(*net.UnixConn)
	t.Cleanup(func() { client.Close(); server.Close() })
	client.SetDeadline(time.Now().Add(5 * time.Second))
	return client, server
}

func ask(t *testing.T, client net.Conn, line string) api.Answer {
	t.Helper()
	if _, err := client.Write([]byte(line + "\n")); err != nil {
		t.Fatal(err)
	}
	var a api.Answer
	if err := json.NewDecoder(client).Decode(&a); err != nil {
		t.Fatal(err)
	}
	return a
}

// A request vpnd cannot read, an unknown request and an API number outside
// vpnd's range are refused before anything runs, and say nothing changed.
// That holds for what the Bitcoin and Lightning node's TUI could ask: its API
// number, its requests, its job kinds and its services.
func TestRefusalsBeforeRunning(t *testing.T) {
	job := func(kind, params string) string {
		return `{"api":2,"verb":"start-job","params":{"kind":"` + kind + `","params":` + params + `}}`
	}
	for _, tc := range []struct{ line, code, words string }{
		{"{not json", api.CodeNothingChanged, "malformed request"},
		{`{"api":2,"verb":"set-user-password","params":{"user":"vpn"}}`, api.CodeNothingChanged, "unknown request"},
		{`{"api":0,"verb":"job-status"}`, api.CodeUnsupportedAPI, "VPN was updated"},
		{`{"api":999,"verb":"job-status"}`, api.CodeUnsupportedAPI, "VPN was updated"},
		{`{"api":1,"verb":"job-status"}`, api.CodeUnsupportedAPI, "VPN was updated"},
		{`{"api":1,"verb":"read-wallet-state"}`, api.CodeUnsupportedAPI, "VPN was updated"},
		{`{"api":2,"verb":"read-wallet-state"}`, api.CodeNothingChanged, "unknown request"},
		{`{"api":2,"verb":"read-node-addresses"}`, api.CodeNothingChanged, "unknown request"},
		{`{"api":2,"verb":"dir-size","params":{"which":"lnd"}}`, api.CodeNothingChanged, "unknown request"},
		{`{"api":2,"verb":"stage-lnd-credentials"}`, api.CodeNothingChanged, "unknown request"},
		{`{"api":2,"verb":"stage-lnd-macaroon"}`, api.CodeNothingChanged, "unknown request"},
		{job("auto-unlock-off", "null"), api.CodeNothingChanged, "unknown job kind"},
		{job("auto-unlock-on", `{"password":"x"}`), api.CodeNothingChanged, "unknown job kind"},
		{job("syncthing-install", "null"), api.CodeNothingChanged, "unknown job kind"},
		{job("p2p-upgrade", `{"expected_ipv4":"203.0.113.7"}`), api.CodeNothingChanged, "unknown job kind"},
		{job("service-action", `{"unit":"lnd","action":"stop"}`), api.CodeNothingChanged, "not managed here"},
		{job("service-action", `{"unit":"bitcoind","action":"restart"}`), api.CodeNothingChanged, "not managed here"},
		{job("service-action", `{"unit":"syncthing","action":"start"}`), api.CodeNothingChanged, "not managed here"},
	} {
		client, conn := pair(t)
		s := &server{version: "0.7.0", allowed: map[uint32]bool{uint32(os.Getuid()): true}, menu: menu()}
		done := make(chan struct{})
		go func() { s.handle(conn); close(done) }()
		a := ask(t, client, tc.line)
		if a.OK || a.Code != tc.code || !strings.Contains(a.Error, tc.words) {
			t.Fatalf("%s: answer %+v", tc.line, a)
		}
		<-done
	}
}

// Tor is the one service the owner starts, stops and restarts.
func TestServiceActionTakesTor(t *testing.T) {
	title, _, _, err := jobs.Validate(api.KindServiceAction, json.RawMessage(`{"unit":"tor","action":"restart"}`))
	if err != nil || title != "Restart tor" {
		t.Fatalf("a restart of Tor: %q, %v", title, err)
	}
}

// vpnd answers connections in parallel. Reads answer while another
// request is still being served, and the race detector watches the shared
// counters.
func TestReadsAnswerBesideASlowRequest(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("kernel peer credentials require Linux")
	}
	dir, err := os.MkdirTemp("", "vd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "s")
	addr, _ := net.ResolveUnixAddr("unix", path)
	ln, err := net.ListenUnix("unix", addr)
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	s := &server{version: "0.7.0", allowed: map[uint32]bool{uint32(os.Getuid()): true}, menu: map[string]request{
		"slow": {deadline: time.Minute, handle: func(*call, json.RawMessage) (any, error) { <-release; return nil, nil }},
		"read": {deadline: time.Minute, handle: func(*call, json.RawMessage) (any, error) { return "fact", nil }},
	}}
	served := make(chan struct{})
	go func() { s.serve(ln); close(served) }()
	dial := func() net.Conn {
		c, err := net.Dial("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		c.SetDeadline(time.Now().Add(5 * time.Second))
		return c
	}
	slow := dial()
	if _, err := slow.Write([]byte(`{"api":2,"verb":"slow"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			c := dial()
			defer c.Close()
			if a := ask(t, c, `{"api":2,"verb":"read"}`); !a.OK || string(a.Result) != `"fact"` {
				t.Errorf("read beside a slow request: %+v", a)
			}
		})
	}
	wg.Wait()
	close(release)
	var a api.Answer
	if err := json.NewDecoder(slow).Decode(&a); err != nil || !a.OK {
		t.Fatalf("slow request: %+v %v", a, err)
	}
	slow.Close()
	s.exitAfterUpdate()
	<-served
}
