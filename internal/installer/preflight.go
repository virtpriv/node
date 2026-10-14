// internal/installer/preflight.go

package installer

// Install preflight. Before the install engine changes anything, check
// what this binary relies on and refuse a machine it cannot trust. Every
// check runs, apart from the machine checks during an image build, and every
// failure is reported at once, so the owner fixes everything in one round
// trip.
//
// RunInstall calls it under root (`sudo vpn install`) before the wizard
// opens. A refused machine keeps its configuration. No user is created, no
// configuration is written and no service is restarted.
//
// Installation is already privileged, so preflight does not require a sudo
// grant for the invoking account. The installer verifies the new owner's sudo
// policy after applying its password and before disabling root SSH.
// Tor availability is checked after package installation. Initial SSH policy
// must be observable. Later SSH and firewall steps verify their own inputs.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/virtpriv/node/internal/bytesize"
	"github.com/virtpriv/node/internal/host"
	"github.com/virtpriv/node/internal/logger"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/system"
)

// PreflightResult is one check's outcome. Err == nil means pass.
type PreflightResult struct {
	Name string
	Err  error
}

// Check names double as report labels, short facts in the present tense.
const (
	checkNameDebian    = "Debian version is exactly 13"
	checkNameArch      = "CPU architecture is amd64"
	checkNameIOLogging = "sudo I/O logging is disabled"
	checkNameDpkg      = "package system is healthy (dpkg --audit)"
	checkNameUfw       = "ufw is installable"
	checkNameSSHState  = "ssh daemon state is observable"
	checkNameMemory    = "memory is at least 3.4 GB"
	checkNameDisk      = "disk is at least 80 GB"
)

// detectHardware is a variable so tests can stand in for the machine.
var detectHardware = DetectHardware

// machineResults measures the machine and checks it, unless the run builds an
// image on a builder machine. The measured values go to the install log.
func machineResults(checkMachine bool) []PreflightResult {
	if !checkMachine {
		return nil
	}
	hw := detectHardware()
	logger.Install("Preflight measured memory %s, disk %s",
		measured(hw.MemoryBytes), measured(hw.DiskBytes))
	return machineChecks(hw)
}

// machineChecks compares the measured machine with the smallest one the
// installer accepts. The comparison is in bytes. A value that was not
// measured passes.
func machineChecks(hw Hardware) []PreflightResult {
	var memory, disk error
	if hw.MemoryBytes > 0 && hw.MemoryBytes < minMemoryBytes {
		memory = fmt.Errorf("found %s, choose a machine with 4 GB or more",
			bytesize.Format(hw.MemoryBytes))
	}
	if hw.DiskBytes > 0 && hw.DiskBytes < minDiskBytes {
		disk = fmt.Errorf("found %s where /var/lib is, choose a machine with 90 GB or more",
			bytesize.Format(hw.DiskBytes))
	}
	return []PreflightResult{{checkNameMemory, memory}, {checkNameDisk, disk}}
}

// measured shows a measured size for the install log.
func measured(bytes uint64) string {
	if bytes == 0 {
		return "not measured"
	}
	return bytesize.Format(bytes)
}

// RunPreflight reports all failed checks before lifecycle initialization.
// The returned SSH observation describes preflight time, not the effective
// owner policy that installation will establish and verify later.
func RunPreflight(checkMachine bool) (host.SSHObservation, error) {
	results, obs := runPreflightChecks(checkMachine)

	failed := 0
	for _, r := range results {
		if r.Err != nil {
			failed++
		}
	}
	// Warnings never refuse. They are printed and logged so the owner can
	// judge them.
	for _, w := range preflightWarnings() {
		fmt.Fprintf(os.Stderr, "\n  WARNING: %s\n", w)
		logger.Install("Preflight WARNING: %s", w)
	}

	if failed == 0 {
		logger.Install("Preflight passed (%d/%d checks)",
			len(results), len(results))
		return obs, nil
	}

	fmt.Fprint(os.Stderr, "\n"+FormatPreflightReport(results))
	for _, r := range results {
		if r.Err != nil {
			logger.Install("Preflight FAIL: %s: %v", r.Name, r.Err)
		}
	}
	return obs, fmt.Errorf(
		"preflight: %d of %d checks failed, nothing was changed",
		failed, len(results))
}

// runPreflightChecks runs every check in order, except the machine checks
// during an image build. Running as root removed the sudo dependency that
// once made a failed check skip the checks after it.
func runPreflightChecks(checkMachine bool) ([]PreflightResult, host.SSHObservation) {
	results := []PreflightResult{
		{checkNameDebian, checkDebian13()},
		{checkNameArch, checkDebianArchitecture()},
	}
	results = append(results, machineResults(checkMachine)...)
	results = append(results,
		PreflightResult{checkNameIOLogging, checkSudoersIOLogging()},
		PreflightResult{checkNameDpkg, checkDpkgAudit()},
		PreflightResult{checkNameUfw, checkUfwInstallable()},
	)
	obs, obsErr := host.ObserveInitialSSHState()
	results = append(results,
		PreflightResult{checkNameSSHState, obsErr})
	return results, obs
}

// checkArchitecture enforces the only artifact set certified by this
// release. It runs before lifecycle initialization, so unsupported binaries
// cannot create durable installation state or begin amd64-specific work.
func checkArchitecture(arch string) error {
	if arch != "amd64" {
		return fmt.Errorf(
			"this release is verified for Debian 13 amd64 only "+
				"(found architecture %q)", arch)
	}
	return nil
}

func checkDebianArchitecture() error {
	out, err := exec.Command("dpkg", "--print-architecture").Output()
	if err != nil {
		return fmt.Errorf("cannot determine Debian architecture: %w", err)
	}
	return checkArchitecture(strings.TrimSpace(string(out)))
}

// preflightWarnings collects conditions worth telling the
// operator about that are not grounds for refusal.
func preflightWarnings() []string {
	var ws []string
	// Directory readability exposes names, not the contents of protected rules.
	// Preserve host-managed directory permissions and report the deviation.
	if fi, err := os.Stat(paths.SudoersDir); err == nil &&
		fi.Mode().Perm()&0o004 != 0 {
		ws = append(ws, fmt.Sprintf(
			"%s has mode %o, while the certified Debian baseline uses 750. VPN leaves host-managed permissions unchanged.",
			paths.SudoersDir, fi.Mode().Perm()))
	}
	return ws
}

// ── Check 1: OS ──────────────────────────────────────────

// checkDebian13 requires /etc/os-release to say ID=debian and VERSION_ID
// exactly 13, not 13 or later. Every outside command this binary runs
// (sshd -T -C flags, chpasswd, dpkg, apt) was verified on a live system with
// the package versions of Debian 13. Debian 14 ships new versions that may
// break those commands. Supporting Debian 14 means verifying the command list
// again, then widening this check in a release.
func checkDebian13() error {
	data, err := os.ReadFile(paths.OSRelease)
	if err != nil {
		return fmt.Errorf("cannot read %s: %w", paths.OSRelease, err)
	}
	return checkOSRelease(string(data))
}

// checkOSRelease parses os-release content without side effects, which the
// tests cover. It matches keys at the start of a line, not substrings, so
// ID_LIKE=debian (Ubuntu and others) can never match.
func checkOSRelease(content string) error {
	id, versionID := "", ""
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if v, ok := strings.CutPrefix(line, "ID="); ok {
			id = strings.Trim(v, `"`)
		}
		if v, ok := strings.CutPrefix(line, "VERSION_ID="); ok {
			versionID = strings.Trim(v, `"`)
		}
	}
	if id != "debian" {
		return fmt.Errorf(
			"this installer supports Debian only (found ID=%q)", id)
	}
	if versionID != "13" {
		return fmt.Errorf(
			"this release is verified for Debian 13 only "+
				"(found VERSION_ID=%q). Newer Debian releases are "+
				"refused until their system commands are verified again",
			versionID)
	}
	return nil
}

// ── Check 2: sudo I/O logging ────────────────────────────

// checkSudoersIOLogging requires that no sudoers file enables sudo's
// input and output recording. It reads /etc/sudoers and every file sudo's
// @includedir would parse in /etc/sudoers.d. As documented and accepted, a
// nonstandard @includedir pointing elsewhere is not followed.
//
// Installation can run through sudo and handles account credentials. Retain
// the existing refusal of observed terminal recording. Runtime node operations
// use the helper. Owners can also use sudo for general host maintenance.
func checkSudoersIOLogging() error {
	files := []string{paths.SudoersFile}
	dropIns, err := listSudoersDropIns()
	if err != nil {
		return err
	}
	files = append(files, dropIns...)

	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("cannot read %s: %w", f, err)
		}
		if directive, found :=
			sudoersEnablesIOLogging(string(data)); found {
			return fmt.Errorf(
				"%s enables sudo I/O recording (%s), which would "+
					"write the admin password to disk when this app "+
					"sets it. Remove that setting and try again",
				f, directive)
		}
	}
	return nil
}

// listSudoersDropIns lists the files sudo's @includedir would parse. As
// root it reads the directory directly. An earlier `sudo find` workaround,
// needed because /etc/sudoers.d is 750 root:root on Debian 13 and the check
// then ran without root, is gone. A missing directory means nothing to scan,
// and any other failure refuses. Subdirectories are skipped because sudo's
// includedir reads regular files only.
func listSudoersDropIns() ([]string, error) {
	entries, err := os.ReadDir(paths.SudoersDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf(
			"cannot list %s: %w", paths.SudoersDir, err)
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !sudoersFileIncluded(e.Name()) {
			continue
		}
		files = append(files, paths.SudoersDir+"/"+e.Name())
	}
	return files, nil
}

// sudoersFileIncluded mirrors sudo's @includedir rule. sudo ignores files
// whose names contain a '.' or end in '~', so a finding in them would be a
// false refusal. Covered by the tests.
func sudoersFileIncluded(name string) bool {
	return !strings.Contains(name, ".") &&
		!strings.HasSuffix(name, "~")
}

// sudoersEnablesIOLogging reports whether sudoers content enables I/O
// recording in either of two ways.
//
//   - A Defaults option, `Defaults log_input` or `Defaults log_output`. An
//     odd number of leading '!' disables it. Comments and @include or
//     #include lines cannot enable anything and are skipped.
//   - A command tag in a user spec, `LOG_INPUT:` or `LOG_OUTPUT:`, in
//     capitals and ending in a colon. NOLOG_ tags disable and do not match.
//
// When unsure it refuses. Covered by the tests.
func sudoersEnablesIOLogging(content string) (string, bool) {
	// Join backslash line continuations first.
	content = strings.ReplaceAll(content, "\\\n", " ")

	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		for _, f := range strings.Fields(line) {
			if f == "LOG_INPUT:" || f == "LOG_OUTPUT:" {
				return f, true
			}
		}

		if !strings.HasPrefix(line, "Defaults") {
			continue
		}
		rest := line[len("Defaults"):]
		for _, tok := range strings.FieldsFunc(rest,
			func(r rune) bool {
				return r == ' ' || r == '\t' ||
					r == ',' || r == '='
			}) {
			neg := 0
			for strings.HasPrefix(tok, "!") {
				tok = tok[1:]
				neg++
			}
			if (tok == "log_input" || tok == "log_output") &&
				neg%2 == 0 {
				return tok, true
			}
		}
	}
	return "", false
}

// ── Check 3: dpkg ────────────────────────────────────────

// checkDpkgAudit requires that the package database has no packages stuck
// in a broken state. A stuck dpkg would otherwise stop the install midway
// through the engine's own apt operations. It only reads and takes no lock.
func checkDpkgAudit() error {
	out, err := system.RunOutputWithTimeout(60*time.Second, "dpkg", "--audit")
	return dpkgAuditVerdict(out, err)
}

// dpkgAuditVerdict counts as clean only exit status 0 with empty output, so
// it does not depend on how dpkg's exit status changes between versions.
// Covered by the tests.
func dpkgAuditVerdict(output string, err error) error {
	if err != nil {
		return fmt.Errorf("dpkg --audit failed: %v", err)
	}
	if s := strings.TrimSpace(output); s != "" {
		first := strings.SplitN(s, "\n", 2)[0]
		return fmt.Errorf(
			"dpkg reports packages in a broken state (%s ...). "+
				"Fix with: dpkg --configure -a && "+
				"apt-get -f install", first)
	}
	return nil
}

// ── Check 4: ufw ─────────────────────────────────────────

// checkUfwInstallable requires that apt's package index on disk can provide
// ufw when the engine's base-package step installs it. It works offline,
// installs nothing, and passes at once when ufw is already present, as on a
// resumed install.
//
// Since the install script became part of this binary, nothing else proves
// apt works first. The base-package step is the first apt operation on the
// machine, and the firewall step right after it needs ufw.
func checkUfwInstallable() error {
	if _, err := exec.LookPath("ufw"); err == nil {
		return nil
	}
	out, err := runAptCachePolicy("ufw", 30*time.Second)
	if err != nil {
		return fmt.Errorf("apt-cache policy ufw failed: %v", err)
	}
	return ufwCandidateVerdict(out)
}

// runAptCachePolicy runs `apt-cache policy <pkg>` with LC_ALL=C so the
// Candidate line is the same in every locale. It runs the command itself,
// not through the system wrappers, only to set that variable.
func runAptCachePolicy(
	pkg string, timeout time.Duration,
) (string, error) {
	ctx, cancel := context.WithTimeout(
		context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "apt-cache", "policy", pkg)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("apt-cache policy %s: %w", pkg, err)
	}
	return string(out), nil
}

// ufwCandidateVerdict parses apt-cache policy output. Installable means a
// Candidate line with a real version. Covered by the tests.
func ufwCandidateVerdict(output string) error {
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		if v, ok := strings.CutPrefix(line, "Candidate:"); ok {
			v = strings.TrimSpace(v)
			if v != "" && v != "(none)" {
				return nil
			}
			return errors.New(
				"apt has no installable ufw candidate, package " +
					"lists may be broken. Run: apt-get update")
		}
	}
	return errors.New(
		"no Candidate line in apt-cache policy output, apt " +
			"package lists may be missing. Run: apt-get update")
}

// ── Report formatting ────────────────────────────────────

// FormatPreflightReport renders the full check list with every failure's reason.
func FormatPreflightReport(results []PreflightResult) string {
	var b strings.Builder
	b.WriteString("  Checking this machine before changing anything\n\n")
	failed := 0
	for _, r := range results {
		if r.Err == nil {
			fmt.Fprintf(&b, "    [ OK ] %s\n", r.Name)
			continue
		}
		failed++
		fmt.Fprintf(&b, "    [FAIL] %s\n", r.Name)
		for _, line := range strings.Split(ansi.Wordwrap(r.Err.Error(), 56, ""), "\n") {
			fmt.Fprintf(&b, "           %s\n", line)
		}
	}
	if failed > 0 {
		b.WriteString("\n  Refusing to install. Nothing on this " +
			"system has been changed.\n")
	}
	return b.String()
}
