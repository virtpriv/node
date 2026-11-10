package release

import "testing"

func TestNewer(t *testing.T) {
	for _, tc := range []struct {
		current, target string
		want            bool
		wantErr         bool
	}{
		{"0.7.0", "0.7.0", false, false},
		{"0.7.0", "0.7.1", true, false},
		{"0.9.9", "0.10.0", true, false},
		{"0.10.0", "0.9.9", false, false},
		{"0.7.9", "0.7.10", true, false},
		{"0.7.0", "1.0.0", true, false},
		{"0.7.0", "0.7.1-rc.1", true, false},
		{"0.7.0-rc.1", "0.7.0-rc.2", true, false},
		{"0.7.0-rc.9", "0.7.0-rc.10", true, false},
		{"0.7.0-rc.10", "0.7.0-rc.9", false, false},
		{"0.7.0-rc.1", "0.7.0-rc.1", false, false},
		{"0.7.0-rc.2", "0.7.0", true, false},
		{"0.7.0", "0.7.0-rc.2", false, false},
		{"0.7.1-rc.1", "0.7.0", false, false},
		{"dev", "0.7.1", false, true},
		{"0.7.0", "0.7.1-rc1", false, true},
	} {
		t.Run(tc.current+"_to_"+tc.target, func(t *testing.T) {
			got, err := Newer(tc.current, tc.target)
			if got != tc.want || (err != nil) != tc.wantErr {
				t.Fatalf("Newer(%q, %q) = %v, %v", tc.current, tc.target, got, err)
			}
		})
	}
}

// Newer refuses every version outside the release contract, in either
// position, before it compares anything.
func TestNewerRefusesNonReleaseVersions(t *testing.T) {
	for _, c := range []struct{ current, target string }{
		{"0.7.0", "0.7.1-rc.0"},
		{"0.7.0", "0.7.1-rc.01"},
		{"0.7.0", "0.7.1-beta.1"},
		{"0.7.0", "0.7.1-rc.1+build"},
		{"0.7.0-rc.01", "0.7.1"},
		{"dev", "0.7.1"},
		{"0.7.0", "dev"},
		{"0.7.0", ""},
		{"0.7.0", "v0.7.1"},
		{"0.7.0", "0.7.1-rc1"},
		{"0.7.0", "0.7.1+build"},
		{"0.7.0", "0.07.1"},
		{"00.7.0", "0.7.1"},
		{"0.7.0", "0.7.1\n"},
		{"0.7.0", "0.7"},
		{"0.7.0", "0.7.1.2"},
		{"0.7.0", "0.7.1;rm -rf /"},
	} {
		if newer, err := Newer(c.current, c.target); err == nil || newer {
			t.Errorf("Newer(%q,%q) = %v, %v, want a refusal", c.current, c.target, newer, err)
		}
	}
}
