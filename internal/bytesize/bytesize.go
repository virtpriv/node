// Package bytesize shows sizes the way owners read them. Code keeps sizes as
// bytes and turns them into text only here, for what a person reads.
package bytesize

import "fmt"

// Sizes in bytes. 1 GB is 1,000,000,000 bytes.
const (
	MB uint64 = 1_000_000
	GB        = 1_000 * MB
	TB        = 1_000 * GB
)

// Format shows n bytes as "512 MB", "4.0 GB" or "1.9 TB". It rounds down, so
// a size just under a limit never shows the limit itself.
func Format(n uint64) string {
	switch {
	case n >= TB:
		return tenths(n, TB) + " TB"
	case n >= GB:
		return tenths(n, GB) + " GB"
	case n >= MB:
		return fmt.Sprintf("%d MB", n/MB)
	default:
		return "under 1 MB"
	}
}

// tenths shows n in the given unit with one decimal, rounded down.
func tenths(n, unit uint64) string {
	return fmt.Sprintf("%d.%d", n/unit, n%unit/(unit/10))
}
