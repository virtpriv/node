// internal/installer/hardware_test.go

package installer

import "testing"

// ── parseMemTotal ────────────────────────────────────────

// MemTotal is given in units of 1024 bytes. A missing or garbled line is
// unknown (zero), never a guess.
func TestParseMemTotal(t *testing.T) {
	meminfo := "MemTotal:        3915776 kB\n" +
		"MemFree:          123456 kB\n" +
		"MemAvailable:    2345678 kB\n"
	if got := parseMemTotal(meminfo); got != 4_009_754_624 {
		t.Errorf("got %d bytes, want 4009754624", got)
	}
	for _, bad := range []string{"", "MemTotal: garbage kB\n", "MemTotal:\n"} {
		if got := parseMemTotal(bad); got != 0 {
			t.Errorf("%q: got %d, want 0", bad, got)
		}
	}
}
