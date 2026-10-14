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

func TestRecommendDbCache(t *testing.T) {
	cases := []struct {
		ramMB, want int
	}{
		{0, 512},    // unknown → the historical hardcode
		{1024, 512}, // 1 GB
		{2048, 512}, // 2 GB
		{3072, 1024},
		{3824, 1024}, // a nominal "4 GB" box
		{4096, 1024},
		{6144, 2048},
		{7800, 2048}, // a nominal "8 GB" box
		{16384, 2048},
	}
	for _, tt := range cases {
		if got := RecommendDbCache(tt.ramMB); got != tt.want {
			t.Errorf("RecommendDbCache(%d) = %d, want %d",
				tt.ramMB, got, tt.want)
		}
	}
}

// Every recommendation must be a value the install record accepts.
func TestRecommendationsAreRecordable(t *testing.T) {
	for _, ram := range []int{0, 2048, 4096, 8192} {
		if rec := RecommendDbCache(ram); !validDbCache(rec) {
			t.Errorf("recommendation %d for %d MB is not recordable", rec, ram)
		}
	}
}
