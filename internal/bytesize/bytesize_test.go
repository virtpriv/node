package bytesize

import "testing"

// A size is shown in the unit owners know, 1 GB being 1,000,000,000 bytes,
// and never as more than it is. One byte under a boundary shows the value
// below it.
func TestFormatNeverShowsMoreThanTheSize(t *testing.T) {
	for _, tc := range []struct {
		bytes uint64
		want  string
	}{
		{0, "under 1 MB"},
		{999_999, "under 1 MB"},
		{1_000_000, "1 MB"},
		{999_999_999, "999 MB"},
		{1_000_000_000, "1.0 GB"},
		{3_399_999_999, "3.3 GB"},
		{3_400_000_000, "3.4 GB"},
		{94_300_000_000, "94.3 GB"},
		{999_999_999_999, "999.9 GB"},
		{1_000_000_000_000, "1.0 TB"},
		{1_999_999_999_999, "1.9 TB"},
	} {
		if got := Format(tc.bytes); got != tc.want {
			t.Errorf("Format(%d) = %q, want %q", tc.bytes, got, tc.want)
		}
	}
}
