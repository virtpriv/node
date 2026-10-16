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

// ── RecommendDbCache ─────────────────────────────────────

// LND gets memory first. Bitcoin Core's cache is 512 MiB below 7.5 GB of
// memory, 1024 MiB from 7.5 GB and 2048 MiB from 15 GB, 1 GB being
// 1,000,000,000 bytes as the System screen shows it.
func TestRecommendDbCache(t *testing.T) {
	const mib = 1 << 20
	cases := []struct {
		memBytes uint64
		want     int
	}{
		{0, 512},               // memory could not be read
		{3824 * mib, 512},      // a machine sold as 4 GB
		{7_499_999_999, 512},   // just under 7.5 GB
		{7_500_000_000, 1024},  // 7.5 GB
		{7800 * mib, 1024},     // a machine sold as 8 GB
		{14_999_999_999, 1024}, // just under 15 GB
		{15_000_000_000, 2048}, // 15 GB
	}
	for _, tt := range cases {
		if got := RecommendDbCache(tt.memBytes); got != tt.want {
			t.Errorf("RecommendDbCache(%d) = %d, want %d",
				tt.memBytes, got, tt.want)
		}
	}
}

// Every recommendation must be a value the install record accepts.
func TestRecommendationsAreRecordable(t *testing.T) {
	for _, mem := range []uint64{0, 4_000_000_000, 8_000_000_000, 16_000_000_000} {
		if rec := RecommendDbCache(mem); !validDbCache(rec) {
			t.Errorf("recommendation %d for %d bytes is not recordable", rec, mem)
		}
	}
}
