// internal/installer/hardware_test.go

package installer

import "testing"

// ── parseMemTotalMB ──────────────────────────────────────

func TestParseMemTotalMB(t *testing.T) {
	meminfo := "MemTotal:        3915776 kB\n" +
		"MemFree:          123456 kB\n" +
		"MemAvailable:    2345678 kB\n"
	if got := parseMemTotalMB(meminfo); got != 3824 {
		t.Errorf("got %d MB, want 3824", got)
	}
	if got := parseMemTotalMB(""); got != 0 {
		t.Errorf("empty: got %d, want 0", got)
	}
	if got := parseMemTotalMB("MemTotal: garbage kB\n"); got != 0 {
		t.Errorf("garbled: got %d, want 0", got)
	}
	if got := parseMemTotalMB("MemTotal:\n"); got != 0 {
		t.Errorf("missing value: got %d, want 0", got)
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
