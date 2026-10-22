package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

// ── journalShowsAdminLogin ───────────────────────────────

func TestJournalShowsAdminLogin(t *testing.T) {
	accepted := []struct {
		name, journal string
	}{
		{"publickey",
			"Accepted publickey for vpn from 203.0.113.7 " +
				"port 51234 ssh2: ED25519 SHA256:abc\n"},
		{"password",
			"Accepted password for vpn from 203.0.113.7 " +
				"port 51234 ssh2\n"},
		{"method with a submethod",
			"Accepted keyboard-interactive/pam for vpn from 203.0.113.7 " +
				"port 51234 ssh2\n"},
		{"buried among noise",
			"Connection closed by 198.51.100.9\n" +
				"Failed password for vpn from 198.51.100.9 port 1\n" +
				"Accepted publickey for vpn from 203.0.113.7 " +
				"port 2 ssh2: ED25519 SHA256:abc\n"},
	}
	for _, tt := range accepted {
		if !journalShowsAdminLogin(tt.journal) {
			t.Errorf("%s: not detected", tt.name)
		}
	}

	rejected := []struct {
		name, journal string
	}{
		{"empty", ""},
		{"failed attempt only",
			"Failed password for vpn from 198.51.100.9 port 1\n"},
		{"different user",
			"Accepted publickey for root from 203.0.113.7 " +
				"port 2 ssh2\n"},
		{"prefix user must not match",
			"Accepted publickey for vpnadmin from 203.0.113.7 " +
				"port 2 ssh2\n"},
		{"invalid user probe",
			"Invalid user vpn from 198.51.100.9 port 1\n"},
		// sshd logs the user name a client sends with its spaces kept, so a
		// stranger can put the accepted wording inside a name.
		{"name carrying the accepted wording",
			"Invalid user Accepted password for vpn from 203.0.113.7 " +
				"from 198.51.100.9 port 4444\n"},
		{"failed attempt naming the accepted wording",
			"Failed password for invalid user Accepted publickey for vpn " +
				"from 203.0.113.7 from 198.51.100.9 port 4444 ssh2\n"},
		{"closed connection naming the accepted wording",
			"Connection closed by invalid user Accepted password for vpn " +
				"from 203.0.113.7 198.51.100.9 port 4444 [preauth]\n"},
	}
	for _, tt := range rejected {
		if journalShowsAdminLogin(tt.journal) {
			t.Errorf("%s: false positive", tt.name)
		}
	}
}

func TestLoginEvidenceAcrossReadBoundaries(t *testing.T) {
	var journal loginEvidenceWriter
	for _, record := range []struct {
		line     string
		accepted bool
	}{
		{"Accepted publickey for vpnadmin from 203.0.113.7 port 22 ssh2\n", false},
		{"Accepted password for vpn from 203.0.113.7 port 22 ssh2\n", true},
	} {
		// Single-byte writes force splits inside the method, username and newline.
		for i := range len(record.line) {
			if n, err := journal.Write([]byte{record.line[i]}); err != nil || n != 1 {
				t.Fatalf("stream write: n=%d err=%v", n, err)
			}
		}
		if journal.found != record.accepted {
			t.Fatalf("split record %q: accepted=%v", record.line, journal.found)
		}
	}
}

// Run real child processes through the production command/wait path without
// reading the test machine's journal or invoking SSH.
func TestAdminLoginCommandEvidenceFailureAndBound(t *testing.T) {
	for _, mode := range []string{"publickey", "password", "empty", "other-user", "partial-failure", "failure", "oversize", "block"} {
		t.Run(mode, func(t *testing.T) {
			timeout := 5 * time.Second
			if mode == "block" {
				timeout = 200 * time.Millisecond
			}
			var child *exec.Cmd
			// The install's connect dialog only counts logins since its run began.
			since := time.Unix(1760000000, 0)
			observed, err := observeAdminLogin(timeout, since, func(ctx context.Context, name string, args ...string) *exec.Cmd {
				if name != "/usr/bin/journalctl" || !slices.Equal(args, []string{"-u", "ssh", "--since=@1760000000", "--no-pager", "-o", "cat", "--quiet"}) {
					t.Fatalf("unexpected journal command: %s %v", name, args)
				}
				child = exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAdminLoginProcess$")
				child.Env = append(os.Environ(), "VPN_LOGIN_EVIDENCE_TEST="+mode)
				return child
			})
			wantObserved := mode == "publickey" || mode == "password"
			wantError := mode == "partial-failure" || mode == "failure" || mode == "oversize" || mode == "block"
			if observed != wantObserved || (err != nil) != wantError {
				t.Fatalf("observed=%v error=%v", observed, err)
			}
			if child.ProcessState == nil {
				t.Fatal("journal process not reaped")
			}
			if mode == "block" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("deadline lost: %v", err)
			}
		})
	}
}

func TestAdminLoginProcess(t *testing.T) {
	mode := os.Getenv("VPN_LOGIN_EVIDENCE_TEST")
	if mode == "" {
		return
	}
	switch mode {
	case "publickey", "password":
		fmt.Fprint(os.Stdout, strings.Repeat("noise\n", 20000))
		fmt.Fprintf(os.Stdout, "Accepted %s for vpn from 203.0.113.7 port 22 ssh2", mode)
	case "other-user":
		fmt.Fprintln(os.Stdout, "Accepted publickey for root from 203.0.113.7 port 22 ssh2")
	case "partial-failure":
		fmt.Fprintln(os.Stdout, "Accepted publickey for vpn from 203.0.113.7 port 22 ssh2")
		os.Exit(1)
	case "failure":
		os.Exit(1)
	case "oversize":
		fmt.Fprint(os.Stdout, strings.Repeat("x", (1<<20)+1))
	case "block":
		time.Sleep(time.Hour)
	}
	os.Exit(0)
}
