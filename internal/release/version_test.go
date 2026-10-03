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

// SameMajor supplies canonical version validation to Newer as well as the
// legacy self-update gate. Keep the input-shape cases here, not in both tables.
func TestSameMajor(t *testing.T) {
	cases := []struct {
		current, target string
		same            bool
		wantErr         bool
	}{
		{"0.7.0", "0.7.1", true, false},
		{"0.7.0", "0.9.9", true, false},
		{"0.7.0", "1.0.0", false, false},
		{"1.2.3", "2.0.0", false, false},
		{"10.0.0", "1.0.0", false, false}, // "10" != "1"
		{"0.7.0", "0.7.1-rc.0", false, true},
		{"0.7.0", "0.7.1-rc.01", false, true},
		{"0.7.0", "0.7.1-beta.1", false, true},
		{"0.7.0", "0.7.1-rc.1+build", false, true},
		{"0.7.0-rc.01", "0.7.1", false, true},
		{"dev", "0.7.1", false, true},
		{"0.7.0", "dev", false, true},
		{"0.7.0", "", false, true},
		{"0.7.0", "v0.7.1", false, true},
		{"0.7.0", "0.7.1-rc1", false, true},
		{"0.7.0", "0.7.1+build", false, true},
		{"0.7.0", "0.07.1", false, true},
		{"00.7.0", "0.7.1", false, true},
		{"0.7.0", "0.7.1\n", false, true},
		{"0.7.0", "0.7", false, true},
		{"0.7.0", "0.7.1.2", false, true},
		{"0.7.0", "0.7.1;rm -rf /", false, true},
	}
	for _, c := range cases {
		same, err := SameMajor(c.current, c.target)
		if c.wantErr {
			if err == nil {
				t.Errorf("SameMajor(%q,%q): expected error",
					c.current, c.target)
			}
			continue
		}
		if err != nil {
			t.Errorf("SameMajor(%q,%q): %v", c.current, c.target, err)
			continue
		}
		if same != c.same {
			t.Errorf("SameMajor(%q,%q) = %v, want %v",
				c.current, c.target, same, c.same)
		}
	}
}
