// internal/installer/preflight_test.go

package installer

import (
	"errors"
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
		{"a 4 GB plan with 90 GB", Hardware{RAMMB: 3824, DiskTotalGB: 89}, "", ""},
		{"at both limits", Hardware{RAMMB: 3400, DiskTotalGB: 80}, "", ""},
		{"just below both limits", Hardware{RAMMB: 3399, DiskTotalGB: 79}, "3.3 GB", "79 GB"},
		{"a 2 GB plan with 60 GB", Hardware{RAMMB: 1948, DiskTotalGB: 60}, "1.9 GB", "60 GB"},
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
