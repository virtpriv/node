// internal/system/info_test.go

package system

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseSourceIPStandardVPS(t *testing.T) {
	output := "1.1.1.1 via 10.0.0.1 dev eth0 src 203.0.113.50 uid 0"
	got := ParseSourceIP(output)
	if got != "203.0.113.50" {
		t.Errorf("got %q, want 203.0.113.50", got)
	}
}

func TestParseSourceIPDirectRoute(t *testing.T) {
	output := "1.1.1.1 dev eth0 src 198.51.100.25 uid 0"
	got := ParseSourceIP(output)
	if got != "198.51.100.25" {
		t.Errorf("got %q, want 198.51.100.25", got)
	}
}

func TestParseSourceIPPrivateIP(t *testing.T) {
	output := "1.1.1.1 via 10.0.0.1 dev eth0 src 10.0.0.5 uid 0"
	got := ParseSourceIP(output)
	if got != "" {
		t.Errorf("private IP should return empty, got %q", got)
	}
}

func TestParseSourceIPPrivate172(t *testing.T) {
	output := "1.1.1.1 via 172.16.0.1 dev eth0 src 172.16.0.5 uid 0"
	got := ParseSourceIP(output)
	if got != "" {
		t.Errorf("172.16 private IP should return empty, got %q", got)
	}
}

func TestParseSourceIPPrivate192(t *testing.T) {
	output := "1.1.1.1 via 192.168.1.1 dev eth0 src 192.168.1.50 uid 0"
	got := ParseSourceIP(output)
	if got != "" {
		t.Errorf("192.168 private IP should return empty, got %q", got)
	}
}

func TestParseSourceIPLoopback(t *testing.T) {
	output := "1.1.1.1 dev lo src 127.0.0.1 uid 0"
	got := ParseSourceIP(output)
	if got != "" {
		t.Errorf("loopback should return empty, got %q", got)
	}
}

func TestParseSourceIPEmpty(t *testing.T) {
	got := ParseSourceIP("")
	if got != "" {
		t.Errorf("empty should return empty, got %q", got)
	}
}

func TestParseSourceIPMalformed(t *testing.T) {
	got := ParseSourceIP("RTNETLINK answers: Network is unreachable")
	if got != "" {
		t.Errorf("malformed should return empty, got %q", got)
	}
}

func TestParseSourceIPNoSrc(t *testing.T) {
	got := ParseSourceIP("1.1.1.1 via 10.0.0.1 dev eth0")
	if got != "" {
		t.Errorf("no src field should return empty, got %q", got)
	}
}

func TestParseSourceIPMultipleSpaces(t *testing.T) {
	output := "1.1.1.1 via 10.0.0.1 dev eth0  src  203.0.113.50  uid 0"
	got := ParseSourceIP(output)
	if got != "203.0.113.50" {
		t.Errorf("got %q, want 203.0.113.50", got)
	}
}

func TestMemoryUnavailableIsNotFullUsage(t *testing.T) {
	for _, raw := range []string{"", "MemTotal: 1000 kB", "MemTotal: 1000 kB\nMemAvailable: 2000 kB", "MemTotal: 1000 kB\nMemAvailable: broken"} {
		if _, err := parseMemory(raw); err == nil {
			t.Fatalf("invalid memory observation accepted: %q", raw)
		}
	}
	info, err := parseMemory("MemTotal: 1000 kB\nMemAvailable: 0 kB")
	if err != nil || info.Percent != "100%" {
		t.Fatal("genuine zero available memory rejected")
	}
}

func TestServiceReadDistinguishesInactiveAndFailedQuery(t *testing.T) {
	dir := t.TempDir()
	// Deliberately return a plausible state with a failed process exit as well
	// as successful states, proving the adapter preserves the process boundary.
	script := "#!/bin/sh\nprintf '%s\\n' \"$STATUS_TEST_OUTPUT\"\nexit \"$STATUS_TEST_EXIT\"\n"
	if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	for _, tc := range []struct {
		output, exit   string
		active, failed bool
	}{
		{"active", "0", true, false}, {"inactive", "0", false, false},
		{"failed", "0", false, false}, {"active", "1", false, true}, {"", "0", false, true},
	} {
		t.Setenv("STATUS_TEST_OUTPUT", tc.output)
		t.Setenv("STATUS_TEST_EXIT", tc.exit)
		active, err := ReadServiceActive(t.Context(), "lnd")
		if active != tc.active || (err != nil) != tc.failed {
			t.Fatalf("output=%q exit=%s: active=%v err=%v", tc.output, tc.exit, active, err)
		}
	}
}

// Debian writes each stamp only when its step succeeds. A run counts only when
// both the package list refresh and the upgrade succeeded, so the older stamp
// is the last full run. Without a full run, the install time of the settings is
// the starting point.
func TestSecurityUpdatesUseTheOlderStampOrTheSettingsTime(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "20auto-upgrades")
	configured := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	older := time.Date(2026, 10, 7, 6, 30, 0, 0, time.UTC)
	newer := time.Date(2026, 10, 8, 8, 40, 0, 0, time.UTC)
	touch := func(path string, at time.Time) {
		t.Helper()
		if err := os.WriteFile(path, nil, 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	touch(config, configured)
	touch(filepath.Join(dir, "update-stamp"), newer)

	got, err := readSecurityUpdates(dir, config)
	if err != nil || !got.LastRun.IsZero() || !got.Configured.Equal(configured) {
		t.Fatalf("one stamp missing: %+v, %v", got, err)
	}
	touch(filepath.Join(dir, "upgrade-stamp"), older)
	got, err = readSecurityUpdates(dir, config)
	if err != nil || !got.LastRun.Equal(older) {
		t.Fatalf("both stamps: %+v, %v", got, err)
	}
}

// A stamp that cannot be read is unknown, never a missing run and never a time.
func TestSecurityUpdatesUnreadableStampIsAnError(t *testing.T) {
	dir := t.TempDir()
	notAFolder := filepath.Join(dir, "periodic")
	if err := os.WriteFile(notAFolder, nil, 0644); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "20auto-upgrades")
	if err := os.WriteFile(config, nil, 0644); err != nil {
		t.Fatal(err)
	}
	if got, err := readSecurityUpdates(notAFolder, config); err == nil {
		t.Fatalf("unreadable stamp gave %+v", got)
	}
}
