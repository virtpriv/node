// internal/installer/hardware.go

package installer

// The installer measures the machine it runs on, memory from
// /proc/meminfo and the size of the filesystem that holds /var/lib.
// Preflight refuses a machine below the limits set here.

import (
	"math"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/virtpriv/node/internal/bytesize"
)

// Hardware is the measured machine, in bytes. A zero field was not measured.
type Hardware struct {
	MemoryBytes uint64 // MemTotal
	DiskBytes   uint64 // filesystem holding /var/lib
}

// The smallest machine the installer accepts. The README asks for 4 GB of
// memory and 90 GB of disk. The limits are lower so that a machine sold at
// those sizes passes whether the provider counts in GB or GiB.
const (
	minMemoryBytes = 3_400 * bytesize.MB
	minDiskBytes   = 80 * bytesize.GB
)

// DetectHardware measures the machine. A measurement that fails leaves its
// field zero, which the checks treat as unknown.
func DetectHardware() Hardware {
	var hw Hardware

	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		hw.MemoryBytes = parseMemTotal(string(data))
	}

	// /var/lib exists on any Debian system.
	for _, p := range []string{"/var/lib", "/"} {
		var st syscall.Statfs_t
		if err := syscall.Statfs(p, &st); err == nil {
			hw.DiskBytes = uint64(st.Blocks) * uint64(st.Bsize)
			break
		}
	}
	return hw
}

// parseMemTotal extracts MemTotal from /proc/meminfo content, in bytes.
// /proc/meminfo counts in units of 1024 bytes. It returns 0 when the line is
// absent or garbled.
func parseMemTotal(meminfo string) uint64 {
	for _, line := range strings.Split(meminfo, "\n") {
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil || kb > math.MaxUint64/1024 {
			return 0
		}
		return kb * 1024
	}
	return 0
}
