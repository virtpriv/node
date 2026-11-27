package host

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/virtpriv/node/internal/logger"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/system"
	"github.com/virtpriv/node/internal/update/files"
)

// InstallInitialBinary places the running executable at the fixed VPN path.
// Only the admitted base installer calls this. It is not an update operation.
func InstallInitialBinary() error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate own binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	if self == paths.BinaryPath {
		return nil
	}
	// Copy the running program itself, even if its file was changed or
	// replaced after it started.
	running, err := os.Open("/proc/self/exe")
	if err != nil {
		return fmt.Errorf("install binary: %w", err)
	}
	defer running.Close()
	if err := files.Replace(paths.BinaryPath, 0755, func(w io.Writer) error {
		_, err := io.Copy(w, running)
		return err
	}); err != nil {
		return fmt.Errorf("install binary: %w", err)
	}
	logger.Install("binary installed to %s (from %s)",
		paths.BinaryPath, self)
	return nil
}

// InstallBasePackages prepares the packages needed before Tor routing is
// configured. Installer keeps this initial clearnet phase before its Tor gate.
// apt-transport-tor comes here with Tor, so step 9 only writes files.
//
// An image may install no recommended packages, so the ones vpn uses are
// named. logrotate otherwise comes only with Debian's tor, and without it
// vpn's log and Debian's own logs have no limit. ca-certificates is needed by
// every download over HTTPS, apt's through Tor included. The system bus and a
// clock service are named as Debian's systemd recommends them, each with its
// alternatives. apt-get satisfy then installs one only when the machine has
// none, so an image's own dbus-broker or chrony stays.
//
// libnss-myhostname makes the machine's own name resolve by Debian's own
// means, so vpn writes no line to /etc/hosts. Debian's cloud images have it.
// On an image without it sudo warns that it cannot resolve the name, or
// waits for a name server that does not answer.
//
// Debian does not start a newly installed dbus before the next boot. The
// restart hold and step 4's clock setting need it now, so it is started.
//
// Both apt commands wait while Debian's own update runs, as the later steps
// do, since Debian starts it at hours it picks at random and after a boot
// that missed one. The lists are refreshed in the strict form. The plain
// form passes when Debian's servers cannot be reached, and the step then
// fails on its packages. A stop asked through ctx ends the wait and is seen
// before the second command, so a step paused there is done again. say tells
// the owner what the step is doing.
func InstallBasePackages(ctx context.Context, say func(string)) error {
	job := newDebianAptJob(say)
	if err := job.run(ctx, "Downloading Debian's package lists",
		sentence("Debian's package tool did not finish downloading the package lists.", refreshPackageLists)); err != nil {
		return err
	}
	if err := job.run(ctx, "Installing the packages",
		sentence("Debian's package tool did not finish installing the packages.", func() error {
			return aptGet(job, "satisfy",
				"sudo", "gnupg", "tor", "torsocks", "apt-transport-tor", "wget", "curl", "ufw", "logrotate",
				"ca-certificates", "default-dbus-system-bus | dbus-system-bus", "systemd-timesyncd | time-daemon",
				"libnss-myhostname")
		})); err != nil {
		return err
	}
	return system.RunRoot("systemctl", "start", "dbus.socket")
}

// logRotation keeps vpn's log at about 10 MB with four older copies.
//
// Every vpn program opens the log once when it starts and keeps it open, as
// the TUI does for as long as it runs. So the log is copied, then emptied in
// place, and keeps its owner and mode. Debian's default instead renames it
// and creates a new file: the programs would go on writing to the renamed
// copy, and a root program starting in between would create the new log as
// root, which the TUI cannot write. The cost, from logrotate's manual: a line
// written during the copy can be lost.
//
// Rotated by size only, so a quiet node keeps its install lines. The size is
// in bytes, since logrotate's M is 1,048,576. Debian's daily timer checks it.
const logRotation = `# Virtual Private Node keeps its own log to about 10 MB, with four older copies.
` + paths.LogFile + ` {
	size 10000000
	rotate 4
	compress
	copytruncate
	missingok
}
`

// ConfigureLogRotation writes vpn's logrotate file. Its folder comes with
// the logrotate package, which InstallBasePackages installs.
func ConfigureLogRotation() error {
	return system.WriteFileRoot(paths.LogRotate, []byte(logRotation), 0644)
}

// PrepareBaseHost asks whether the machine's own name resolves and requests
// NTP synchronization. Neither stops the install. The name resolves through
// libnss-myhostname, which step 2 installs. Debian's package adds itself to
// /etc/nsswitch.conf only where that file names "files" for hosts, so a name
// that still does not resolve is logged. vpn does not write /etc/hosts.
func PrepareBaseHost() error {
	if name, err := os.Hostname(); err == nil && name != "" {
		if err := system.RunSilent("getent", "hosts", name); err != nil {
			logger.Install("the machine's name %s does not resolve, so sudo may warn or wait", name)
		}
	}
	// NTP stays best effort. A failure to turn it on is logged.
	if err := system.RunRootSilent(
		"timedatectl", "set-ntp", "true"); err != nil {
		logger.Install(
			"WARNING: could not enable NTP sync (%v)", err)
	}
	return nil
}

// DisableIPv6 applies the base appliance IPv4 policy.
func DisableIPv6() error {
	content := `# Virtual Private Node turns IPv6 off.
net.ipv6.conf.all.disable_ipv6 = 1
net.ipv6.conf.default.disable_ipv6 = 1
net.ipv6.conf.lo.disable_ipv6 = 1
`
	if err := system.WriteFileRoot(
		paths.DisableIPv6Conf, []byte(content), 0644); err != nil {
		return err
	}
	return system.RunRootSilent("sysctl", "--system")
}

// nightlyValues are vpn's settings for Debian's nightly job and its
// unattended-upgrade, in the order vpn's file holds them. Step 10 writes
// them and CheckNightlyValues asks apt for them.
var nightlyValues = []struct{ key, value string }{
	{"APT::Periodic::Update-Package-Lists", "1"},
	{"APT::Periodic::Unattended-Upgrade", "1"},
	{"APT::Periodic::AutocleanInterval", "7"},
	{"Unattended-Upgrade::Automatic-Reboot", "true"},
	{"Unattended-Upgrade::Automatic-Reboot-Time", "now"},
	{"Unattended-Upgrade::Remove-Unused-Kernel-Packages", "true"},
	{"Unattended-Upgrade::OnlyOnACPower", "false"},
	{"Unattended-Upgrade::Skip-Updates-On-Metered-Connections", "false"},
}

// vpnOrigins are the archives Debian's nightly job installs from.
var vpnOrigins = []string{
	"origin=Debian,codename=${distro_codename},label=Debian",
	"origin=Debian,codename=${distro_codename},label=Debian-Security",
	"origin=Debian,codename=${distro_codename}-security,label=Debian-Security",
}

// ConfigureUnattendedUpgrades writes vpn's rules for Debian's
// unattended-upgrade, which every package update runs, and apt's hook for
// the reboot check.
//
// The rules go into a file of vpn's own. Debian's unattended-upgrades package
// keeps its two settings files, 20auto-upgrades and 50unattended-upgrades,
// and vpn does not write them. Its own file is read after them, as the
// package's README asks of local settings. It first clears every setting of
// the nightly job, so what an image or a later version of Debian's files sets
// never mixes with vpn's values, then sets its own.
//
// The three origin lines are Debian's own, from the template in its
// unattended-upgrades package. Debian's security archive has the suite
// stable-security and the codename <codename>-security, so it is matched by
// codename. Backports are left out, as in Debian's template.
//
// The machine restarts right after the nightly run when an update needs it.
// vpn's install, Update Packages and a node update hold a block inhibitor,
// so a restart then is refused and waits for the next night.
//
// A node takes Debian's fixes on battery and on a metered connection too, so
// those two of Debian's defaults are turned off. They stop a run with a
// failure.
//
// Both dpkg options keep a configuration file the owner changed, with
// Debian's new one saved beside it, and update an untouched one. Without
// them a changed file holds its package back for good. They count for every
// apt run on the machine.
func ConfigureUnattendedUpgrades() error {
	if err := system.WriteFileRoot(paths.AptNightly, []byte(nightlySettings()), 0644); err != nil {
		return err
	}
	return system.WriteFileRoot(paths.AptRebootCheck, []byte(aptRebootCheckHook), 0644)
}

// nightlySettings is the text of vpn's nightly file.
func nightlySettings() string {
	var periodic, upgrade strings.Builder
	for _, v := range nightlyValues {
		if strings.HasPrefix(v.key, "APT::Periodic::") {
			fmt.Fprintf(&periodic, "%s %q;\n", v.key, v.value)
		} else {
			fmt.Fprintf(&upgrade, "%s %q;\n", v.key, v.value)
		}
	}
	return `// Virtual Private Node installs Debian's updates by itself. Debian's own two files stay
// as they were. What an earlier file set for the nightly job is cleared first.
#clear APT::Periodic;
#clear Unattended-Upgrade;
` + periodic.String() + `Unattended-Upgrade::Origins-Pattern {
        "` + strings.Join(vpnOrigins, `";
        "`) + `";
};
` + upgrade.String() + `Dpkg::Options {
        "--force-confdef";
        "--force-confold";
};
`
}

// aptRebootCheckHook runs the reboot check after every package change. It
// never makes apt fail.
const aptRebootCheckHook = `// Virtual Private Node checks after each package change whether a restart is needed.
DPkg::Post-Invoke {"if [ -x ` + paths.BinaryPath + ` ]; then ` + paths.BinaryPath + ` reboot-check || true; fi";};
`

// InstallFail2ban installs the SSH intrusion-prevention service and
// nftables, once Debian's own job is not running. Debian's fail2ban bans
// through nft, which only the nftables package brings. fail2ban merely
// recommends it, so a host that already has iptables gets none, and its bans
// would be recorded but never enforced. nftables' own service stays off.
func InstallFail2ban(ctx context.Context, say func(string)) error {
	job := newDebianAptJob(say)
	return job.run(ctx, "Installing fail2ban",
		sentence("Debian's package tool did not finish installing fail2ban.",
			func() error { return aptGet(job, "install", "fail2ban", "nftables") }))
}

var (
	observeSSHForFail2ban = ObserveInitialSSHState
	writeFail2banJail     = func(content []byte) error {
		return system.WriteFileRoot(paths.Fail2banJail, content, 0644)
	}
	runFail2banCommand = func(args ...string) error {
		return system.RunRoot(args[0], args[1:]...)
	}
)

// ConfigureFail2ban writes the SSH jail and starts the service. A ban covers
// every port sshd listens on, observed right before the write as the firewall
// step does, so a failed observation writes nothing. A port added after the
// install is not covered, as it is not opened in the firewall either.
func ConfigureFail2ban() error {
	obs, err := observeSSHForFail2ban()
	if err != nil {
		return fmt.Errorf("observe SSH ports before fail2ban setup: %w", err)
	}
	if err := writeFail2banJail([]byte(fail2banJail(obs.Ports))); err != nil {
		return err
	}
	if err := runFail2banCommand("systemctl", "enable", "fail2ban"); err != nil {
		return err
	}
	return runFail2banCommand("systemctl", "restart", "fail2ban")
}

// fail2banValues are the jail's values fail2ban's server is asked for, as
// fail2banJail sets them.
var fail2banValues = []struct{ name, want string }{
	{"maxretry", "5"}, {"findtime", "600"}, {"bantime", "600"},
}

// askFail2ban asks fail2ban's server. It is a variable so a test can stand
// in for the server.
var askFail2ban = func(args ...string) (string, error) {
	return askTool(questionTimeout, "fail2ban-client", args...)
}

// CheckFail2banJail is the fact "fail2ban's SSH jail is on as vpn set it":
// the running server lists the sshd jail, with vpn's values, banning through
// nft on every port sshd listens on. Right after its restart the server opens
// its socket before it has read its jails (2.5 s on an emulated VM), so the
// whole question is asked again until it holds or its limit passes. Whether
// fail2ban hears sshd is not asked (FIREWALL-001, decision 3).
func CheckFail2banJail() error {
	obs, err := observeSSHForFail2ban()
	if err != nil {
		return fmt.Errorf("observe SSH ports: %w", err)
	}
	until := time.Now().Add(questionTimeout)
	for {
		err = fail2banVerdict(obs.Ports)
		if err == nil || time.Now().After(until) {
			return err
		}
		time.Sleep(time.Second)
	}
}

func fail2banVerdict(ports []int) error {
	const cannot = "cannot read fail2ban's sshd jail: %w"
	// "Jail list:" then the jails, comma separated, empty when none is on.
	status, err := askFail2ban("status")
	if err != nil {
		return fmt.Errorf(cannot, err)
	}
	_, jails, _ := strings.Cut(status, "Jail list:")
	names := strings.FieldsFunc(jails, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' })
	if !slices.Contains(names, "sshd") {
		return errors.New("found fail2ban without its sshd jail, so logins are not rate limited. Look in /etc/fail2ban for a file that turns the jail off")
	}
	var f findings
	for _, v := range fail2banValues {
		got, err := askFail2ban("get", "sshd", v.name)
		if err != nil {
			return fmt.Errorf(cannot, err)
		}
		if got != v.want {
			f.add("found %s %s in fail2ban's sshd jail, which vpn sets to %s", v.name, got, v.want)
		}
	}
	actions, err := askFail2ban("get", "sshd", "actions")
	if err != nil {
		return fmt.Errorf(cannot, err)
	}
	// The first line is a heading, the actions follow it.
	if names := strings.Split(actions, "\n")[1:]; !slices.Equal(names, []string{"nftables"}) {
		f.add("found the ban actions %s in fail2ban's sshd jail, where vpn sets nftables", strings.Join(names, ", "))
	} else {
		got, err := askFail2ban("get", "sshd", "action", "nftables", "port")
		if err != nil {
			return fmt.Errorf(cannot, err)
		}
		want := make([]string, len(ports))
		for i, p := range ports {
			want[i] = strconv.Itoa(p)
		}
		if have := strings.Split(got, ","); !slices.Equal(have, want) {
			f.add("found fail2ban's sshd jail banning on port %s, where sshd listens on %s", got, strings.Join(want, ","))
		}
	}
	return f.err("remove the setting that changes", "so logins over SSH are rate limited")
}

// fail2banJail names the ban action InstallFail2ban guarantees and lists the
// ports comma separated, the form the action turns into one nft port set.
func fail2banJail(sshPorts []int) string {
	ports := make([]string, len(sshPorts))
	for i, p := range sshPorts {
		ports[i] = strconv.Itoa(p)
	}
	return `# Virtual Private Node, stricter SSH login protection.
# Bans go through nftables, on every port sshd listened on at install.
[sshd]
enabled = true
mode = aggressive
banaction = nftables
port = ` + strings.Join(ports, ",") + `
maxretry = 5
findtime = 600
bantime = 600
`
}
