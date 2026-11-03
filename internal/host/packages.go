package host

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/virtpriv/node/internal/logger"
	"github.com/virtpriv/node/internal/system"
)

// Every package update runs Debian's own unattended-upgrade with vpn's rules:
// install step 11, Update Packages and the nightly job. It installs the new
// packages a fix needs, a new kernel among them, and keeps configuration
// files an owner changed. apt goes through Tor by then.

// apt runs in the C locale here, so its lock messages can be recognized.
var runApt = system.RunRootCLocale

// refreshPackageLists fails when any source cannot be reached, so the
// upgrade never runs on old lists or none.
func refreshPackageLists() error {
	return runApt("apt-get", "update", "--error-on=any", "-qq")
}

// unattendedUpgradeLog is where unattended-upgrade writes why it failed. Its
// own output says nothing without its verbose mode.
const unattendedUpgradeLog = "/var/log/unattended-upgrades/unattended-upgrades.log"

func runUnattendedUpgrade() error {
	if err := runApt("unattended-upgrade"); err != nil {
		return fmt.Errorf("%w (its log is %s)", err, unattendedUpgradeLog)
	}
	return nil
}

// UpgradeDebian is install step 11's package work. The lists come first,
// since step 10 replaced the sources and apt has no lists for vpn's yet.
// unattended-upgrades is then installed, which turns the nightly job on,
// vpn's rules are written, and the program runs once. The reboot check runs
// once more at the end, since with nothing to install no hook runs. A
// resumed install repeats the whole step.
func UpgradeDebian() error {
	job := newDebianAptJob()
	if err := job.run(refreshPackageLists); err != nil {
		return err
	}
	if err := job.run(func() error { return installPackages(job, "unattended-upgrades") }); err != nil {
		return err
	}
	if err := ConfigureUnattendedUpgrades(); err != nil {
		return err
	}
	if err := job.run(runUnattendedUpgrade); err != nil {
		return err
	}
	logRebootCheck(logger.Install)
	return nil
}

// installPackages runs apt-get install, waiting for apt's lock for the rest
// of the job's hour.
func installPackages(job *debianAptJob, names ...string) error {
	args := []string{"install", "-y", "-qq",
		"-o", fmt.Sprintf("DPkg::Lock::Timeout=%d", int(job.left().Seconds()))}
	return runApt("apt-get", append(args, names...)...)
}

// logRebootCheck runs the reboot check and logs what it found. A check that
// could not read part of the machine is logged, never a failure.
func logRebootCheck(log func(string, ...any)) {
	reasons, err := CheckReboot()
	for _, r := range reasons {
		log("a restart is needed: %s", r)
	}
	if err != nil {
		log("WARNING: the restart check failed: %s", strings.ReplaceAll(err.Error(), "\n", ", "))
	}
}

// UpdatePackages is Update Packages. It completes accepted package work
// independently of the observer. Do not attach the terminal's cancellation
// to apt or dpkg: a lost connection must not interrupt package
// configuration. Only the read-only final audit has a deadline. A restart is
// refused until it ends, so one an update asks for waits for the next night.
func UpdatePackages(progress func(int)) error {
	if os.Geteuid() != 0 {
		return errors.New("package updates require the root helper")
	}
	inhibitor, err := system.InhibitShutdown("updating Debian's packages")
	if err != nil {
		return nothingChanged(fmt.Errorf("cannot hold off a restart: %w", err))
	}
	defer inhibitor.Release()
	job := newDebianAptJob()
	if err := job.run(refreshPackageLists); err != nil {
		return fmt.Errorf("refresh package lists: %w", err)
	}
	progress(0)
	if err := job.run(runUnattendedUpgrade); err != nil {
		return fmt.Errorf("upgrade packages: %w", err)
	}
	logRebootCheck(logger.System)
	progress(1)
	return packageAuditResult(system.RunOutputWithTimeout(10*time.Second, "dpkg", "--audit"))
}

// packageAuditResult reads dpkg --audit, which prints nothing on a healthy
// machine.
func packageAuditResult(out string, err error) error {
	if err != nil {
		return fmt.Errorf("package consistency audit failed: %w", err)
	}
	if strings.TrimSpace(out) != "" {
		return fmt.Errorf("package consistency audit reported problems: %s", strings.TrimSpace(out))
	}
	return nil
}

// ── Waiting for Debian's own apt job ─────────────────────

// debianAptJobLimit is how long vpn waits for Debian's job, as long as
// Debian's own apt.systemd.daily waits for apt's lock (flock -w 3600).
const debianAptJobLimit = time.Hour

const debianAptJobPoll = 5 * time.Second

var errDebianJobStillRunning = errors.New("the daily Debian update was still running after an hour")

// debianAptJob waits while Debian's nightly job runs, so vpn's apt work does
// not fail on apt's lock. The hour counts from the first wait.
type debianAptJob struct {
	running  func() (bool, error)
	sleep    func(time.Duration)
	now      func() time.Time
	deadline time.Time
}

func newDebianAptJob() *debianAptJob {
	return &debianAptJob{
		running: debianAptJobRunning,
		sleep:   time.Sleep,
		now:     time.Now,
	}
}

// run waits while Debian's job runs, then runs cmd. The job can start
// between the wait and cmd. So a lock error, or any failure while the job is
// running, waits once more and tries once more. unattended-upgrade names
// another one holding its lock only in its own log, so its failure carries
// no message to recognize. A second failure is returned as cmd's own.
func (j *debianAptJob) run(cmd func() error) error {
	if j.deadline.IsZero() {
		j.deadline = j.now().Add(debianAptJobLimit)
	}
	var cmdErr error
	for try := 0; try < 2; try++ {
		if err := j.wait(); err != nil {
			return err
		}
		if cmdErr = cmd(); cmdErr == nil {
			return nil
		}
		if !isAptLockError(cmdErr) {
			if running, err := j.running(); err != nil || !running {
				return cmdErr
			}
		}
	}
	return cmdErr
}

func (j *debianAptJob) wait() error {
	for {
		running, err := j.running()
		if err != nil {
			return err
		}
		if !running {
			return nil
		}
		if !j.now().Before(j.deadline) {
			return errDebianJobStillRunning
		}
		j.sleep(min(debianAptJobPoll, j.deadline.Sub(j.now())))
	}
}

// left is the rest of the job's hour, at least one second.
func (j *debianAptJob) left() time.Duration {
	if j.deadline.IsZero() {
		return debianAptJobLimit
	}
	return max(time.Second, j.deadline.Sub(j.now()))
}

// isAptLockError recognizes apt's and unattended-upgrade's messages, in the
// C locale, for a lock another apt holds.
func isAptLockError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "Could not get lock") ||
		strings.Contains(msg, "Cache lock can not be acquired")
}

// debianAptJobUnits are Debian's nightly job: the list refresh and download,
// then the upgrade.
var debianAptJobUnits = []string{"apt-daily.service", "apt-daily-upgrade.service"}

// debianAptJobRunning asks systemd whether Debian's job runs. Both are
// oneshot units, which systemd shows as activating while they run.
func debianAptJobRunning() (bool, error) {
	out, err := runAptTool("systemctl", append([]string{"show", "--property=Id,ActiveState"}, debianAptJobUnits...)...)
	if err != nil {
		return false, fmt.Errorf("cannot read whether Debian's daily update runs: %w", err)
	}
	units := system.ParseUnitProperties(out)
	for _, unit := range debianAptJobUnits {
		state, ok := units[unit]["ActiveState"]
		if !ok {
			return false, fmt.Errorf("cannot read whether %s runs", unit)
		}
		switch state {
		case "active", "activating", "deactivating", "reloading", "refreshing":
			return true, nil
		}
	}
	return false, nil
}
