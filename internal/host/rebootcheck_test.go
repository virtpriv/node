package host

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The node checks show the reboot check reading a real machine. These cases
// are what a node cannot show on demand: kernel versions, kinds of replaced
// files, the flag's form and the hook never failing apt.

// compareByOrder stands in for dpkg --compare-versions a lt b, by the
// versions' order in the list, oldest first.
func compareByOrder(t *testing.T, order ...string) func(a, op, b string) (bool, error) {
	return func(a, op, b string) (bool, error) {
		ia, ib := slices.Index(order, a), slices.Index(order, b)
		if op != "lt" || ia < 0 || ib < 0 {
			t.Fatalf("compare %s %s %s", a, op, b)
		}
		return ia < ib, nil
	}
}

func TestNewerKernel(t *testing.T) {
	compare := compareByOrder(t, "6.12.111-1", "6.12.113-1", "6.12.113-2", "6.12.115-2", "6.12.120-1")
	const cloud115 = "installed linux-image-6.12.115+deb13-cloud-amd64 6.12.115-2\ninstalled linux-image-cloud-amd64 6.12.115-2\n"
	for _, tc := range []struct {
		name, running, packages, want string
	}{
		{name: "the newest kernel runs", running: "6.12.115-2", packages: cloud115},
		{name: "a newer kernel is installed", running: "6.12.111-1", packages: cloud115,
			want: "linux-image-6.12.115+deb13-cloud-amd64"},
		{name: "the running kernel is newer than any installed", running: "6.12.120-1", packages: cloud115},
		{name: "removed kernels are not counted", running: "6.12.115-2", packages: cloud115 +
			"config-files linux-image-6.12.120+deb13-cloud-amd64 6.12.120-1\n" +
			"not-installed linux-image-6.12.120+deb13-amd64 6.12.120-1\n" +
			"half-configured linux-image-6.12.120+deb13-rt-amd64 6.12.120-1\n"},
		{name: "a new build of the running kernel's package counts", running: "6.12.113-1",
			packages: "installed linux-image-6.12.113+deb13-amd64 6.12.113-2\n",
			want:     "linux-image-6.12.113+deb13-amd64"},
		{name: "the newest of several is named", running: "6.12.111-1", packages: cloud115 +
			"installed linux-image-6.12.113+deb13-amd64 6.12.113-1\n" +
			"installed linux-image-6.12.111+deb13-amd64 6.12.111-1\n",
			want: "linux-image-6.12.115+deb13-cloud-amd64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := newerKernel("#1 SMP PREEMPT_DYNAMIC Debian "+tc.running+" (2026-10-28)\n", tc.packages, compare)
			if err != nil {
				t.Fatal(err)
			}
			if got := ""; r != nil {
				got = r.name
				if got != tc.want || !strings.Contains(r.text, tc.running) {
					t.Fatalf("reason %q (%s), want %q", got, r.text, tc.want)
				}
			} else if tc.want != "" {
				t.Fatalf("no reason, want %q", tc.want)
			}
		})
	}
}

// A kernel that does not say which Debian version it is cannot be compared.
// That is never a reason to restart. The caller logs the error.
func TestNewerKernelNotDebians(t *testing.T) {
	for _, version := range []string{"#1 SMP PREEMPT_DYNAMIC Thu Oct  2 10:00:00 UTC 2026\n", "#1 SMP Debian\n", ""} {
		r, err := newerKernel(version, "installed linux-image-6.12.115+deb13-amd64 6.12.115-2\n", compareByOrder(t))
		if err == nil || r != nil {
			t.Fatalf("kernel %q: reason %v, error %v", version, r, err)
		}
	}
}

func TestReplacedFile(t *testing.T) {
	const (
		tor      = "55d0c0a00000-55d0c0b00000 r-xp 00028000 08:01 393220 /usr/bin/tor"
		crypto   = "7f2a10200000-7f2a10500000 r-xp 000a0000 08:01 394001 /usr/lib/x86_64-linux-gnu/libcrypto.so.3"
		cryptoRO = "7f2a10100000-7f2a10200000 r--p 00000000 08:01 394001 /usr/lib/x86_64-linux-gnu/libcrypto.so.3"
		heap     = "55d0c1000000-55d0c1200000 rw-p 00000000 00:00 0 [heap]"
		locales  = "7f2a0f000000-7f2a0f400000 r--p 00000000 08:01 400100 /usr/lib/locale/locale-archive (deleted)"
		memfd    = "7f2a0e000000-7f2a0e001000 r-xp 00000000 00:01 2051 /memfd:libffi (deleted)"
		temp     = "7f2a0c001000-7f2a0c002000 r-xs 00000000 00:1f 77 /tmp/ffiXyZ12 (deleted)"
		bitcoind = "55d0c0a00000-55d0c0b00000 r-xp 00028000 08:01 393500 /usr/local/bin/bitcoind (deleted)"
	)
	for _, tc := range []struct {
		name string
		maps []string
		want string
	}{
		{name: "current files", maps: []string{tor, cryptoRO, crypto, heap}},
		{name: "a replaced library", maps: []string{tor, cryptoRO + " (deleted)", crypto + " (deleted)"},
			want: "/usr/lib/x86_64-linux-gnu/libcrypto.so.3"},
		{name: "a replaced program file", maps: []string{tor + " (deleted)", crypto}, want: "/usr/bin/tor"},
		{name: "a replaced data file", maps: []string{tor, locales}},
		{name: "a memory file", maps: []string{tor, memfd}},
		{name: "a temporary file", maps: []string{tor, temp}},
		{name: "vpn's own program, replaced by a node update", maps: []string{bitcoind}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := replacedFile(strings.Join(tc.maps, "\n") + "\n"); got != tc.want {
				t.Fatalf("replaced file %q, want %q", got, tc.want)
			}
		})
	}
}

// A stopped service has no process to read. A process that ended before its
// list was read is an error, never a reason, and the other services are
// still checked.
func TestRebootCheckProgramsStoppedAndEnded(t *testing.T) {
	root := t.TempDir()
	maps := filepath.Join(root, "/proc/700/maps")
	if err := os.MkdirAll(filepath.Dir(maps), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(maps, []byte("7f2a10200000-7f2a10500000 r-xp 000a0000 08:01 394001 /usr/lib/x86_64-linux-gnu/libcrypto.so.3 (deleted)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := rebootHost{root: root, run: func(string, ...string) (string, error) {
		return "Id=tor@default.service\nMainPID=4242\n\nId=ssh.service\nMainPID=700\n\n" +
			"Id=bitcoind.service\nMainPID=0\n\nId=fail2ban.service\nMainPID=0", nil
	}}
	reasons, err := h.programs()
	if err == nil || !strings.Contains(err.Error(), "tor@default.service") || strings.Contains(err.Error(), "bitcoind") {
		t.Fatalf("error %v, want one naming only tor@default.service", err)
	}
	if len(reasons) != 1 || reasons[0].name != "ssh.service" {
		t.Fatalf("reasons %v, want only ssh.service", reasons)
	}
}

// The flag has Debian's form: an empty reboot-required file and one name per
// line in reboot-required.pkgs, each name once. Debian's own kernel hook may
// have written them first.
func TestRebootFlagKeepsDebiansFormAndNamesEachReasonOnce(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "/var/run"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, rebootRequiredPkgs), []byte("linux-image-6.12.115+deb13-cloud-amd64\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := rebootHost{root: root}
	reasons := []rebootReason{{name: "linux-image-6.12.115+deb13-cloud-amd64"}, {name: "ssh.service"}}

	raised, err := h.raise(reasons)
	if err != nil || len(raised) != 1 || raised[0].name != "ssh.service" {
		t.Fatalf("raised %v, error %v, want only the new ssh.service", raised, err)
	}
	if again, err := h.raise(reasons); err != nil || len(again) != 0 {
		t.Fatalf("a second raise added %v, error %v", again, err)
	}
	data, _ := os.ReadFile(filepath.Join(root, rebootRequiredPkgs))
	if got := strings.Fields(string(data)); !slices.Equal(got, []string{"linux-image-6.12.115+deb13-cloud-amd64", "ssh.service"}) {
		t.Fatalf("reboot-required.pkgs holds %q", got)
	}
	if flag, err := os.ReadFile(filepath.Join(root, rebootRequiredFlag)); err != nil || len(flag) != 0 {
		t.Fatalf("reboot-required holds %q, error %v, want it empty as Debian leaves it", flag, err)
	}
}

// apt runs the hook after every package change. It must never make apt fail,
// so whatever goes wrong is one line on stderr and nothing else.
func TestRebootCheckHookNeverFails(t *testing.T) {
	h := rebootHost{root: t.TempDir(), run: func(string, ...string) (string, error) {
		return "", errors.New("systemctl: Failed to connect to bus")
	}}
	var out, errOut bytes.Buffer
	rebootCheckHook(h, &out, &errOut)
	if out.Len() != 0 || strings.Count(errOut.String(), "\n") != 1 {
		t.Fatalf("stdout %q, stderr %q, want one stderr line", out.String(), errOut.String())
	}
}
