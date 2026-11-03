package host

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/virtpriv/node/internal/system"
)

// The flag Debian's tools read, in the form Debian's own kernel hook writes
// it: an empty file, and one name per line in the .pkgs file beside it.
// unattended-upgrades reboots when the first exists.
const (
	rebootRequiredFlag = "/var/run/reboot-required"
	rebootRequiredPkgs = "/var/run/reboot-required.pkgs"
)

// rebootCheckUnits are the services that run programs from Debian's packages
// and keep running across an update. LND and vpn's helper are built with no
// outside libraries, so Debian's updates never leave them on old code.
var rebootCheckUnits = []string{"tor@default.service", "ssh.service", "bitcoind.service", "fail2ban.service"}

// rebootReason is one reason to restart. name goes in the .pkgs file, text is
// for people.
type rebootReason struct{ name, text string }

// rebootHost is what the check reads: the files under root, "" on a node,
// and the answers of dpkg and systemctl.
type rebootHost struct {
	root    string
	run     func(name string, args ...string) (string, error)
	compare func(a, op, b string) (bool, error)
}

func productionRebootHost() rebootHost {
	return rebootHost{run: runAptTool, compare: compareDebianVersions}
}

// compareDebianVersions asks dpkg whether "a op b" holds. dpkg exits 0 when
// it does and 1 when it does not.
func compareDebianVersions(a, op, b string) (bool, error) {
	err := exec.Command("dpkg", "--compare-versions", a, op, b).Run()
	if err == nil {
		return true, nil
	}
	if exit, ok := errors.AsType[*exec.ExitError](err); ok && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("dpkg --compare-versions %s %s %s: %w", a, op, b, err)
}

func (h rebootHost) path(p string) string { return filepath.Join(h.root, p) }

// CheckReboot is the reboot check of install step 11 and Update Packages,
// which also runs from apt's hook after every package change. It raises the
// flag when the running kernel is older than the newest one installed, or
// when one of rebootCheckUnits runs a program or library file that an update
// replaced. It returns what it added to the flag. An error means part of the
// machine could not be checked, and the reasons found elsewhere still count.
func CheckReboot() ([]string, error) {
	raised, err := productionRebootHost().checkAndRaise()
	var texts []string
	for _, r := range raised {
		texts = append(texts, r.text)
	}
	return texts, err
}

// RebootCheckHook is `vpn reboot-check`, the command apt's hook runs. apt
// must never fail on it, so it has no failure to return: it prints a line for
// each new reason, and an error as one line on stderr.
func RebootCheckHook(stdout, stderr io.Writer) {
	rebootCheckHook(productionRebootHost(), stdout, stderr)
}

func rebootCheckHook(h rebootHost, stdout, stderr io.Writer) {
	raised, err := h.checkAndRaise()
	for _, r := range raised {
		fmt.Fprintf(stdout, "vpn: a restart is needed: %s\n", r.text)
	}
	if err != nil {
		fmt.Fprintf(stderr, "vpn: the restart check failed: %s\n", strings.ReplaceAll(err.Error(), "\n", ", "))
	}
}

func (h rebootHost) checkAndRaise() ([]rebootReason, error) {
	var reasons []rebootReason
	kernel, kernelErr := h.kernel()
	if kernel != nil {
		reasons = append(reasons, *kernel)
	}
	programs, programsErr := h.programs()
	reasons = append(reasons, programs...)
	raised, raiseErr := h.raise(reasons)
	return raised, errors.Join(kernelErr, programsErr, raiseErr)
}

// debianKernelVersion finds the Debian version in the running kernel's
// version string, for example "#1 SMP PREEMPT_DYNAMIC Debian 6.12.113-1
// (2026-10-20)".
var debianKernelVersion = regexp.MustCompile(`\bDebian (\d\S*)`)

// kernel reads the running kernel's version and the installed packages.
func (h rebootHost) kernel() (*rebootReason, error) {
	data, err := os.ReadFile(h.path("/proc/sys/kernel/version"))
	if err != nil {
		return nil, fmt.Errorf("cannot read the running kernel's version: %w", unwrapPath(err))
	}
	out, err := h.run("dpkg-query", "-W", "-f=${db:Status-Status} ${binary:Package} ${Version}\n")
	if err != nil {
		return nil, fmt.Errorf("cannot read the installed kernels: %w", err)
	}
	return newerKernel(string(data), out, h.compare)
}

// newerKernel compares the running kernel's Debian version with the newest
// fully installed linux-image-<version> package, from dpkg-query lines of
// status, name and version. A package removed with its settings kept is not
// installed. A new build of the same package counts, so versions are
// compared, not names.
func newerKernel(version, packages string, compare func(a, op, b string) (bool, error)) (*rebootReason, error) {
	m := debianKernelVersion.FindStringSubmatch(version)
	if m == nil {
		return nil, fmt.Errorf("the running kernel is not one of Debian's (%s)", strings.TrimSpace(version))
	}
	running := m[1]
	var newestName, newest string
	for line := range strings.SplitSeq(packages, "\n") {
		f := strings.Fields(line)
		if len(f) != 3 || f[0] != "installed" || !isKernelPackage(f[1]) {
			continue
		}
		if newest != "" {
			if older, err := compare(newest, "lt", f[2]); err != nil {
				return nil, err
			} else if !older {
				continue
			}
		}
		newestName, newest = f[1], f[2]
	}
	if newest == "" {
		return nil, nil
	}
	older, err := compare(running, "lt", newest)
	if err != nil || !older {
		return nil, err
	}
	return &rebootReason{name: newestName,
		text: fmt.Sprintf("kernel %s is installed and %s is running", newest, running)}, nil
}

// isKernelPackage matches linux-image-<version>, not the packages that
// follow the newest kernel (linux-image-amd64, linux-image-cloud-amd64).
func isKernelPackage(name string) bool {
	v, ok := strings.CutPrefix(name, "linux-image-")
	return ok && v != "" && v[0] >= '0' && v[0] <= '9'
}

// programs reads which of rebootCheckUnits run and what each one runs.
func (h rebootHost) programs() ([]rebootReason, error) {
	out, err := h.run("systemctl", append([]string{"show", "--property=Id,MainPID"}, rebootCheckUnits...)...)
	if err != nil {
		return nil, fmt.Errorf("cannot read which services run: %w", err)
	}
	units := system.ParseUnitProperties(out)
	var reasons []rebootReason
	var errs []error
	for _, unit := range rebootCheckUnits {
		pid := units[unit]["MainPID"]
		if pid == "" || pid == "0" {
			continue
		}
		maps, err := os.ReadFile(h.path(filepath.Join("/proc", pid, "maps")))
		if err != nil {
			errs = append(errs, fmt.Errorf("cannot read the files %s runs: %w", unit, unwrapPath(err)))
			continue
		}
		if file := replacedFile(string(maps)); file != "" {
			reasons = append(reasons, rebootReason{name: unit,
				text: fmt.Sprintf("%s runs the replaced file %s", unit, file)})
		}
	}
	return reasons, errors.Join(errs...)
}

// replacedFile returns the first file in a process's /proc/<pid>/maps that
// Debian replaced while the process ran, or "". dpkg replaces a file by
// renaming the new one over it, so the process keeps the old one, which the
// list shows as "(deleted)". Only a mapping that can run counts, so data files
// do not, and only a file under /usr outside /usr/local, where Debian keeps
// every program and library it installs and nothing else. Memory files and
// vpn's own programs are left out that way. A line is "address perms offset
// dev inode path".
func replacedFile(maps string) string {
	for line := range strings.SplitSeq(maps, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 || !strings.Contains(fields[1], "x") {
			continue
		}
		path, ok := strings.CutSuffix(strings.Join(fields[5:], " "), " (deleted)")
		if ok && strings.HasPrefix(path, "/usr/") && !strings.HasPrefix(path, "/usr/local/") {
			return path
		}
	}
	return ""
}

// raise adds the reasons to the flag as Debian's kernel hook does, each name
// once, and returns the ones it added.
func (h rebootHost) raise(reasons []rebootReason) ([]rebootReason, error) {
	if len(reasons) == 0 {
		return nil, nil
	}
	flag, err := os.OpenFile(h.path(rebootRequiredFlag), os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("cannot raise the restart flag: %w", unwrapPath(err))
	}
	if err := flag.Close(); err != nil {
		return nil, fmt.Errorf("cannot raise the restart flag: %w", err)
	}
	data, err := os.ReadFile(h.path(rebootRequiredPkgs))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("cannot read %s: %w", rebootRequiredPkgs, unwrapPath(err))
	}
	listed := strings.Fields(string(data))
	var added []rebootReason
	var lines strings.Builder
	if len(data) > 0 && data[len(data)-1] != '\n' {
		lines.WriteByte('\n')
	}
	for _, r := range reasons {
		if slices.Contains(listed, r.name) {
			continue
		}
		listed = append(listed, r.name)
		added = append(added, r)
		lines.WriteString(r.name + "\n")
	}
	if len(added) == 0 {
		return nil, nil
	}
	pkgs, err := os.OpenFile(h.path(rebootRequiredPkgs), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("cannot write %s: %w", rebootRequiredPkgs, unwrapPath(err))
	}
	if _, err := pkgs.WriteString(lines.String()); err != nil {
		pkgs.Close()
		return nil, fmt.Errorf("cannot write %s: %w", rebootRequiredPkgs, err)
	}
	if err := pkgs.Close(); err != nil {
		return nil, fmt.Errorf("cannot write %s: %w", rebootRequiredPkgs, err)
	}
	return added, nil
}
