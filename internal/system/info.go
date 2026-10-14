// internal/system/info.go

package system

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/virtpriv/node/internal/bytesize"
)

// DiskInfo is the size and use of a filesystem in bytes. The screen formats
// them.
type DiskInfo struct {
	TotalBytes, UsedBytes uint64
	Percent               string
}

// MemInfo is the size and use of memory in bytes. The screen formats them.
type MemInfo struct {
	TotalBytes, UsedBytes uint64
	Percent               string
}

func ReadDisk(ctx context.Context, path string) (DiskInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "df", "-B1", "--output=size,used,pcent", path).Output()
	if err != nil {
		return DiskInfo{}, err
	}
	return parseDisk(string(out))
}

// parseDisk reads df -B1 output, a header line and then size, used and
// percent, the sizes in bytes.
func parseDisk(out string) (DiskInfo, error) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return DiskInfo{}, errors.New("missing disk usage")
	}
	f := strings.Fields(lines[1])
	if len(f) != 3 {
		return DiskInfo{}, errors.New("invalid disk usage")
	}
	size, sizeErr := strconv.ParseUint(f[0], 10, 64)
	used, usedErr := strconv.ParseUint(f[1], 10, 64)
	if sizeErr != nil || usedErr != nil {
		return DiskInfo{}, errors.New("invalid disk usage")
	}
	return DiskInfo{TotalBytes: size, UsedBytes: used, Percent: f[2]}, nil
}

func ReadMemory() (MemInfo, error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return MemInfo{}, err
	}
	return parseMemory(string(data))
}

// parseMemory reads /proc/meminfo, which counts in units of 1024 bytes.
func parseMemory(data string) (MemInfo, error) {
	var total, avail uint64
	haveTotal, haveAvail := false, false
	for _, line := range strings.Split(data, "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			n, err := fmt.Sscanf(line, "MemTotal: %d kB", &total)
			haveTotal = n == 1 && err == nil
		}
		if strings.HasPrefix(line, "MemAvailable:") {
			n, err := fmt.Sscanf(line, "MemAvailable: %d kB", &avail)
			haveAvail = n == 1 && err == nil
		}
	}
	if !haveTotal || !haveAvail || total == 0 || avail > total || total > math.MaxUint64/1024 {
		return MemInfo{}, errors.New("invalid memory usage")
	}
	used := total - avail
	return MemInfo{TotalBytes: total * 1024, UsedBytes: used * 1024,
		Percent: fmt.Sprintf("%.0f%%", float64(used)/float64(total)*100)}, nil
}

// ReadServiceActive separates an inactive unit from a failed systemd query.
func ReadServiceActive(ctx context.Context, name string) (bool, error) {
	state, err := ReadServiceState(ctx, name)
	return state == "active" || state == "reloading" || state == "refreshing", err
}

// ReadServiceState retains transitional states for mutation postconditions.
func ReadServiceState(ctx context.Context, name string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "systemctl", "show", "--property=ActiveState", "--value", name)
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	state := strings.TrimSpace(string(out))
	switch state {
	case "active", "reloading", "refreshing", "inactive", "failed", "activating", "deactivating", "maintenance":
		return state, nil
	default:
		return "", errors.New("unavailable service state")
	}
}

func ReadRebootRequired() (bool, error) {
	_, err := os.Stat("/var/run/reboot-required")
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// DirSize measures the disk space a directory tree uses, with du, and
// formats it for the screen, because the helper sends it as text. Root
// only, because the data directories it is used on belong to service
// users. The unprivileged status screen gets the LND size through the
// helper's dir-size operation (which calls this as root) and the Bitcoin
// size from bitcoind's own RPC. It never calls this directly.
func DirSize(path string) string {
	if os.Geteuid() != 0 {
		return "N/A"
	}
	out, err := exec.Command("du", "-s", "-B1", path).Output()
	if err != nil {
		return "N/A"
	}
	bytes, ok := parseDu(string(out))
	if !ok {
		return "N/A"
	}
	return bytesize.Format(bytes)
}

// parseDu reads du -s -B1 output, the bytes used, a tab and the path.
func parseDu(out string) (uint64, bool) {
	f := strings.Fields(out)
	if len(f) < 1 {
		return 0, false
	}
	bytes, err := strconv.ParseUint(f[0], 10, 64)
	return bytes, err == nil
}

func IsServiceActive(name string) bool {
	return exec.Command("systemctl", "is-active", "--quiet", name).Run() == nil
}

// ── Public IP detection ──────────────────────────────────

var (
	cachedIP string
	ipOnce   sync.Once
)

// PublicIPv4 returns the server's public IPv4 address.
// Uses the kernel routing table (no network call) and caches the result.
// Only relevant in hybrid (clearnet+tor) P2P mode.
func PublicIPv4() string {
	ipOnce.Do(func() {
		cachedIP = detectPublicIPv4()
	})
	return cachedIP
}

func detectPublicIPv4() string {
	ip, _ := ReadPublicIPv4(context.Background())
	return ip
}

func ReadPublicIPv4(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ip", "-4", "route", "get", "1.1.1.1").Output()
	if err != nil {
		return "", err
	}
	ip := ParseSourceIP(string(out))
	if ip == "" {
		return "", errors.New("public IPv4 unavailable")
	}
	return ip, nil
}

// ParseSourceIP extracts the source IP from "ip route get" output.
// Exported for testing.
func ParseSourceIP(routeOutput string) string {
	i := strings.Index(routeOutput, "src ")
	if i == -1 {
		return ""
	}
	fields := strings.Fields(routeOutput[i+4:])
	if len(fields) == 0 {
		return ""
	}
	ip := net.ParseIP(fields[0])
	if ip == nil || ip.IsPrivate() || ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return ""
	}
	return ip.String()
}

// SecurityUpdates is what Debian's daily apt job has recorded. LastRun is
// zero until both the package list refresh and the upgrade have succeeded.
// Configured is when the install wrote vpn's update settings.
type SecurityUpdates struct {
	LastRun    time.Time
	Configured time.Time
}

const aptPeriodicDir = "/var/lib/apt/periodic"

// ReadSecurityUpdates reads apt's success stamps and the time of the update
// settings file the install wrote. Every account can read both.
func ReadSecurityUpdates(config string) (SecurityUpdates, error) {
	return readSecurityUpdates(aptPeriodicDir, config)
}

// apt.systemd.daily touches update-stamp after a successful package list
// refresh and upgrade-stamp after a successful unattended-upgrade run. A
// failed step leaves its stamp alone, so the older stamp is the last run in
// which both succeeded.
func readSecurityUpdates(dir, config string) (SecurityUpdates, error) {
	var result SecurityUpdates
	info, err := os.Stat(config)
	if err != nil {
		return result, err
	}
	result.Configured = info.ModTime()
	for _, name := range []string{"update-stamp", "upgrade-stamp"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if errors.Is(err, os.ErrNotExist) {
			result.LastRun = time.Time{}
			return result, nil
		}
		if err != nil {
			return SecurityUpdates{}, err
		}
		if result.LastRun.IsZero() || info.ModTime().Before(result.LastRun) {
			result.LastRun = info.ModTime()
		}
	}
	return result, nil
}

// ReadUnitProperties reads systemd's record of the given units, which every
// account can read. The result is keyed by unit name.
func ReadUnitProperties(ctx context.Context, units []string, properties ...string) (map[string]map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	args := []string{"show", "--timestamp=unix", "--property=Id," + strings.Join(properties, ",")}
	cmd := exec.CommandContext(ctx, "systemctl", append(args, units...)...)
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return ParseUnitProperties(string(out)), nil
}

// ParseUnitProperties reads the output of systemctl show for several units,
// one block of Key=value lines per unit, blocks separated by an empty line.
// A block without an Id is left out.
func ParseUnitProperties(out string) map[string]map[string]string {
	units := make(map[string]map[string]string)
	for block := range strings.SplitSeq(out, "\n\n") {
		values := make(map[string]string)
		for line := range strings.SplitSeq(block, "\n") {
			if key, value, ok := strings.Cut(line, "="); ok {
				values[key] = value
			}
		}
		if id := values["Id"]; id != "" {
			units[id] = values
		}
	}
	return units
}
