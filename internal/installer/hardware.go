// internal/installer/hardware.go

package installer

// The installer measures the machine it runs on, memory from
// /proc/meminfo and the size of the filesystem that will hold Bitcoin
// Core's data. Preflight refuses a machine below the limits set here, and
// the measured memory sets Bitcoin Core's database cache without asking.
//
// The image build (--until=bake) runs on a builder machine, so preflight
// skips the machine checks there and runs them at first boot. The cache is
// still chosen during the build, because Bitcoin Core is installed then.

import (
	"os"
	"strings"
	"syscall"
)

// Hardware is the measured machine. A zero field was not measured.
type Hardware struct {
	RAMMB       int // MemTotal
	DiskTotalGB int // filesystem holding the data dir
}

// The smallest machine the installer accepts. The README asks for 4 GB of
// memory and 90 GB of disk. A "4 GB" plan reports about 3.8 GB and a
// "90 GB" plan about 89 GB, so the limits leave room for how providers count.
const (
	minMemoryMB = 3400
	minDiskGB   = 80
)

// DetectHardware measures the machine. A measurement that fails leaves its
// field zero, which the checks and RecommendDbCache treat as unknown.
func DetectHardware() Hardware {
	var hw Hardware

	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		hw.RAMMB = parseMemTotalMB(string(data))
	}

	// The bitcoin data dir may not exist yet on a fresh machine, so measure
	// the nearest existing parent. /var/lib exists on any Debian system.
	for _, p := range []string{"/var/lib/bitcoin", "/var/lib", "/"} {
		var st syscall.Statfs_t
		if err := syscall.Statfs(p, &st); err == nil {
			hw.DiskTotalGB = int(uint64(st.Blocks) *
				uint64(st.Bsize) / 1_000_000_000)
			break
		}
	}
	return hw
}

// parseMemTotalMB extracts MemTotal from /proc/meminfo content, in MB. It
// returns 0 when the line is absent or garbled.
func parseMemTotalMB(meminfo string) int {
	for _, line := range strings.Split(meminfo, "\n") {
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		kb := 0
		for _, c := range fields[1] {
			if c < '0' || c > '9' {
				return 0
			}
			kb = kb*10 + int(c-'0')
		}
		return kb / 1024
	}
	return 0
}

// RecommendDbCache maps measured memory to a bitcoind dbcache in MB. The
// table is conservative and stops at 2048, because sync gains flatten beyond
// that on a node that also runs LND and Tor, which need their own memory.
//
//	unknown or below 3 GB → 512
//	3 GB or more          → 1024
//	6 GB or more          → 2048
func RecommendDbCache(ramMB int) int {
	switch {
	case ramMB >= 6144:
		return 2048
	case ramMB >= 3072:
		return 1024
	default:
		return 512
	}
}

// recommendedDbCache is the cache the installer records for this machine.
func recommendedDbCache() int { return RecommendDbCache(detectHardware().RAMMB) }
