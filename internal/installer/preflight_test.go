// internal/installer/preflight_test.go

package installer

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── checkOSRelease ───────────────────────────────────────

func TestCheckOSReleaseAccepts(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"debian 13 quoted",
			"PRETTY_NAME=\"Debian GNU/Linux 13 (trixie)\"\n" +
				"NAME=\"Debian GNU/Linux\"\n" +
				"VERSION_ID=\"13\"\n" +
				"VERSION=\"13 (trixie)\"\n" +
				"VERSION_CODENAME=trixie\n" +
				"ID=debian\n"},
		{"debian 13 unquoted",
			"ID=debian\nVERSION_ID=13\n"},
		{"leading whitespace tolerated",
			"  ID=debian\n  VERSION_ID=\"13\"\n"},
	}
	for _, tt := range cases {
		if err := checkOSRelease(tt.content); err != nil {
			t.Errorf("%s: unexpected refusal: %v", tt.name, err)
		}
	}
}

func TestCheckOSReleaseRefuses(t *testing.T) {
	cases := []struct {
		name    string
		content string
		wantIn  string
	}{
		{"debian 12",
			"ID=debian\nVERSION_ID=\"12\"\n",
			"Debian 13 only"},
		{"debian 14",
			"ID=debian\nVERSION_ID=\"14\"\n",
			"Debian 13 only"},
		{"ubuntu with ID_LIKE=debian",
			"ID=ubuntu\nID_LIKE=debian\nVERSION_ID=\"24.04\"\n",
			"Debian only"},
		{"debian sid, no VERSION_ID",
			"ID=debian\nVERSION_CODENAME=sid\n",
			"Debian 13 only"},
		{"empty content", "", "Debian only"},
	}
	for _, tt := range cases {
		err := checkOSRelease(tt.content)
		if err == nil {
			t.Errorf("%s: accepted, want refusal", tt.name)
			continue
		}
		if !strings.Contains(err.Error(), tt.wantIn) {
			t.Errorf("%s: error %q does not mention %q",
				tt.name, err, tt.wantIn)
		}
	}
}

// The old checkOS accepted 13 or newer by matching substrings. This guards
// the two deliberate tightenings, exactly 13 and ID parsed from the start of
// a line, against regression.
func TestCheckOSReleaseExactlyThirteen(t *testing.T) {
	for _, ver := range []string{"14", "15", "12", "11"} {
		content := "ID=debian\nVERSION_ID=\"" + ver + "\"\n"
		if err := checkOSRelease(content); err == nil {
			t.Errorf("VERSION_ID=%s accepted, want refusal", ver)
		}
	}
}

func TestCheckArchitecture(t *testing.T) {
	if err := checkArchitecture("amd64"); err != nil {
		t.Fatalf("amd64 refused: %v", err)
	}
	for _, arch := range []string{"arm64", "386", "riscv64", ""} {
		if err := checkArchitecture(arch); err == nil {
			t.Errorf("unsupported architecture %q accepted", arch)
		}
	}
}

// ── sudoersFileIncluded ──────────────────────────────────

func TestSudoersFileIncluded(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"vpn", true},
		{"README", true},
		{"99-custom", true},
		{"backup.bak", false},    // contains '.'
		{"50-cloud.conf", false}, // contains '.'
		{"vpn~", false},          // editor backup
	}
	for _, tt := range cases {
		if got := sudoersFileIncluded(tt.name); got != tt.want {
			t.Errorf("sudoersFileIncluded(%q) = %v, want %v",
				tt.name, got, tt.want)
		}
	}
}

// ── sudoersEnablesIOLogging ──────────────────────────────

func TestSudoersIOLoggingDetected(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"plain log_input", "Defaults log_input\n"},
		{"plain log_output", "Defaults log_output\n"},
		{"user-scoped", "Defaults:vpn log_input\n"},
		{"comma list", "Defaults env_reset, log_input\n"},
		{"double negation enables", "Defaults !!log_input\n"},
		{"tab separated", "Defaults\tlog_output\n"},
		{"line continuation",
			"Defaults env_reset, \\\n    log_input\n"},
		{"LOG_INPUT tag",
			"vpn ALL=(ALL) LOG_INPUT: /usr/bin/foo\n"},
		{"LOG_OUTPUT tag",
			"vpn ALL=(ALL) NOPASSWD: LOG_OUTPUT: ALL\n"},
		{"buried among safe lines",
			"# comment\nDefaults env_reset\n" +
				"vpn ALL=(ALL) NOPASSWD:ALL\n" +
				"Defaults log_input\n"},
	}
	for _, tt := range cases {
		if _, found := sudoersEnablesIOLogging(tt.content); !found {
			t.Errorf("%s: not detected", tt.name)
		}
	}
}

func TestSudoersIOLoggingClean(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"empty", ""},
		{"bootstrap rule only",
			"vpn ALL=(ALL) NOPASSWD:ALL\n"},
		{"stock debian",
			"Defaults env_reset\n" +
				"Defaults mail_badpass\n" +
				"Defaults use_pty\n" + // no disk sink by itself
				"root ALL=(ALL:ALL) ALL\n" +
				"@includedir /etc/sudoers.d\n"},
		{"commented out", "#Defaults log_input\n"},
		{"commented with space", "# Defaults log_input\n"},
		{"explicitly negated", "Defaults !log_input\n"},
		{"negated both",
			"Defaults !log_input\nDefaults !log_output\n"},
		{"iolog_dir alone does not enable",
			"Defaults iolog_dir=/var/log/sudo-io\n"},
		{"NOLOG tags disable",
			"vpn ALL=(ALL) NOLOG_INPUT: NOLOG_OUTPUT: ALL\n"},
		{"similar option names",
			"Defaults log_year\nDefaults logfile=/var/log/sudo\n"},
	}
	for _, tt := range cases {
		if d, found := sudoersEnablesIOLogging(tt.content); found {
			t.Errorf("%s: false positive on %q", tt.name, d)
		}
	}
}

// ── checkSudoersIOLogging ────────────────────────────────

// A machine without sudo has no /etc/sudoers. Nothing can record through
// sudo then, and the install adds Debian's sudo, whose settings record
// nothing. Only Debian's package records decide that sudo is absent. A file
// already in the folder still counts, because it takes effect once sudo is
// installed.
func TestSudoersCheckWithoutSudo(t *testing.T) {
	t.Run("no sudo at all passes", func(t *testing.T) {
		root := t.TempDir()
		err := checkSudoersIOLogging(filepath.Join(root, "sudoers"), filepath.Join(root, "sudoers.d"), true)
		if err != nil {
			t.Errorf("got %v, want a pass", err)
		}
	})
	t.Run("a recording file waiting for sudo is refused", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, "sudoers.d")
		waiting := filepath.Join(dir, "recording")
		if err := os.Mkdir(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(waiting, []byte("Defaults log_input\n"), 0o440); err != nil {
			t.Fatal(err)
		}
		err := checkSudoersIOLogging(filepath.Join(root, "sudoers"), dir, true)
		if err == nil || !strings.Contains(err.Error(), waiting) {
			t.Errorf("got %v, want a refusal naming %s", err, waiting)
		}
	})
	t.Run("a settings file that cannot be read is refused", func(t *testing.T) {
		root := t.TempDir()
		sudoers := filepath.Join(root, "sudoers")
		if err := os.Mkdir(sudoers, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := checkSudoersIOLogging(sudoers, filepath.Join(root, "sudoers.d"), true); err == nil {
			t.Error("an unreadable settings file passed")
		}
	})
	t.Run("sudo installed with its settings file deleted is refused", func(t *testing.T) {
		root := t.TempDir()
		if err := checkSudoersIOLogging(filepath.Join(root, "sudoers"), filepath.Join(root, "sudoers.d"), false); err == nil {
			t.Error("a missing settings file passed while sudo is installed")
		}
	})
}

// sudo counts as absent only when Debian's package records say
// "not-installed", as on a cloud image without sudo, and no sudo program is
// on the machine. Anything else, an error included, counts as installed.
func TestSudoAbsent(t *testing.T) {
	cases := []struct {
		name    string
		status  string
		exit    int
		program bool
		want    bool
	}{
		{"never installed, no program", "not-installed", 0, false, true},
		{"never installed, a sudo program anyway", "not-installed", 0, true, false},
		{"no record at all", "", 1, false, false},
		{"installed", "installed", 0, true, false},
		{"removed, settings kept", "config-files", 0, false, false},
		{"half installed", "half-installed", 0, false, false},
		{"dpkg failed", "", 2, false, false},
	}
	for _, tt := range cases {
		if got := sudoAbsent(tt.status, tt.exit, tt.program); got != tt.want {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.want)
		}
	}
}

// ── dpkgAuditVerdict ─────────────────────────────────────

func TestDpkgAuditVerdict(t *testing.T) {
	if err := dpkgAuditVerdict("", nil); err != nil {
		t.Errorf("clean audit refused: %v", err)
	}
	if err := dpkgAuditVerdict("  \n ", nil); err != nil {
		t.Errorf("whitespace-only output refused: %v", err)
	}
	if err := dpkgAuditVerdict(
		"The following packages are only half configured:\n"+
			" somepkg\n", nil); err == nil {
		t.Error("broken-state output accepted")
	}
	if err := dpkgAuditVerdict("", errors.New("exit status 2")); err == nil {
		t.Error("nonzero exit accepted")
	}
}

// ── ufwCandidateVerdict ──────────────────────────────────

func TestUfwCandidateVerdict(t *testing.T) {
	good := "ufw:\n" +
		"  Installed: (none)\n" +
		"  Candidate: 0.36.2-9\n" +
		"  Version table:\n" +
		"     0.36.2-9 500\n"
	if err := ufwCandidateVerdict(good); err != nil {
		t.Errorf("real candidate refused: %v", err)
	}

	none := "ufw:\n  Installed: (none)\n  Candidate: (none)\n"
	if err := ufwCandidateVerdict(none); err == nil {
		t.Error("Candidate (none) accepted")
	}

	if err := ufwCandidateVerdict(""); err == nil {
		t.Error("empty output accepted")
	}
}

// ── machineChecks ────────────────────────────────────────

// A machine below either limit is refused, and the refusal says what was
// found. At the limit it passes. A value that could not be measured (zero) is
// not grounds for refusal.
func TestMachineChecks(t *testing.T) {
	cases := []struct {
		name        string
		hw          Hardware
		memoryFound string // empty means the memory check passes
		diskFound   string // empty means the disk check passes
	}{
		{"a 4 GB plan with 90 GB", Hardware{MemoryBytes: 4_071_620_608, DiskBytes: 94_300_000_000}, "", ""},
		{"at both limits", Hardware{MemoryBytes: 3_400_000_000, DiskBytes: 80_000_000_000}, "", ""},
		{"just below both limits", Hardware{MemoryBytes: 3_399_999_999, DiskBytes: 79_999_999_999}, "3.3 GB", "79.9 GB"},
		{"a 2 GB plan with 60 GB", Hardware{MemoryBytes: 2_042_626_048, DiskBytes: 62_900_000_000}, "2.0 GB", "62.9 GB"},
		{"nothing measured", Hardware{}, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			results := machineChecks(tc.hw)
			if len(results) != 2 || results[0].Name != checkNameMemory || results[1].Name != checkNameDisk {
				t.Fatalf("checks %+v, want memory then disk", results)
			}
			for i, found := range []string{tc.memoryFound, tc.diskFound} {
				err := results[i].Err
				if (err == nil) != (found == "") {
					t.Fatalf("%s: error %v, want refusal %v", results[i].Name, err, found != "")
				}
				if err != nil && !strings.Contains(err.Error(), found) {
					t.Fatalf("%s: refusal %q does not say it found %s", results[i].Name, err, found)
				}
			}
		})
	}
}

// A container plan shares its host's kernel, so the firewall, IPv6 and swap
// steps cannot work there. systemd-detect-virt --container prints the kind
// of container, or "none" with a nonzero exit outside one. An answer that
// cannot be read passes, as for the other checks.
func TestCheckVirtualMachine(t *testing.T) {
	exitOne := errors.New("exit status 1")
	for _, tc := range []struct {
		out    string
		err    error
		refuse bool
	}{
		{"none", exitOne, false},
		{"lxc", nil, true},
		{"openvz", nil, true},
		{"docker", nil, true},
		{"", errors.New("executable file not found"), false},
	} {
		if err := checkVirtualMachine(tc.out, tc.err); (err != nil) != tc.refuse {
			t.Errorf("%q, %v: refused %v, want %v", tc.out, tc.err, err != nil, tc.refuse)
		}
	}
}
