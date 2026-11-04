package host

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/virtpriv/node/internal/logger"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/system"
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
	if err := system.RunRoot("install", "-m", "755",
		self, paths.BinaryPath); err != nil {
		return fmt.Errorf("install binary: %w", err)
	}
	logger.Install("binary installed to %s (from %s)",
		paths.BinaryPath, self)
	return nil
}

// InstallBasePackages prepares the packages needed before Tor routing is
// configured. Installer keeps this initial clearnet phase before its Tor gate.
// apt-transport-tor comes here with Tor, so step 10 only writes files.
func InstallBasePackages() error {
	if err := system.RunRoot("apt-get", "update", "-qq"); err != nil {
		return err
	}
	return system.RunRoot("apt-get", "install", "-y", "-qq",
		"sudo", "gnupg", "tor", "torsocks", "apt-transport-tor", "wget", "curl", "ufw")
}

// PrepareBaseHost repairs unresolved local hostname lookup when possible and
// requests NTP synchronization. Failure to enable NTP remains a logged warning.
func PrepareBaseHost() error {
	if name, err := os.Hostname(); err == nil && name != "" {
		if err := system.RunSilent(
			"getent", "hosts", name); err != nil {
			hosts, readErr := os.ReadFile("/etc/hosts")
			if readErr == nil {
				content := string(hosts)
				if !strings.HasSuffix(content, "\n") {
					content += "\n"
				}
				content += "127.0.0.1 " + name + "\n"
				if err := system.WriteFileRoot("/etc/hosts",
					[]byte(content), 0644); err != nil {
					return fmt.Errorf(
						"fix hostname resolution: %w", err)
				}
				logger.Install("hostname resolution fixed (%s)", name)
			}
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

// ConfigureUnattendedUpgrades writes vpn's rules for Debian's
// unattended-upgrade, which every package update runs, and apt's hook for
// the reboot check. The three origin lines are Debian's own, from the
// template in its unattended-upgrades package. Debian's security archive has
// the suite stable-security and the codename <codename>-security, so it is
// matched by codename. Backports are left out, as in Debian's template.
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
// them a changed file holds its package back for good.
func ConfigureUnattendedUpgrades() error {
	autoConf := `APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
APT::Periodic::AutocleanInterval "7";
`
	if err := system.WriteFileRoot(paths.AutoUpgrades,
		[]byte(autoConf), 0644); err != nil {
		return err
	}

	upgradeConf := `// Virtual Private Node installs Debian's updates by itself.
Unattended-Upgrade::Origins-Pattern {
        "origin=Debian,codename=${distro_codename},label=Debian";
        "origin=Debian,codename=${distro_codename},label=Debian-Security";
        "origin=Debian,codename=${distro_codename}-security,label=Debian-Security";
};
Unattended-Upgrade::Automatic-Reboot "true";
Unattended-Upgrade::Automatic-Reboot-Time "now";
Unattended-Upgrade::Remove-Unused-Kernel-Packages "true";
Unattended-Upgrade::OnlyOnACPower "false";
Unattended-Upgrade::Skip-Updates-On-Metered-Connections "false";
Dpkg::Options {
        "--force-confdef";
        "--force-confold";
};
`
	if err := system.WriteFileRoot(paths.UnattendedUpgrades,
		[]byte(upgradeConf), 0644); err != nil {
		return err
	}
	return system.WriteFileRoot(paths.AptRebootCheck, []byte(aptRebootCheckHook), 0644)
}

// aptRebootCheckHook runs the reboot check after every package change. It
// never makes apt fail.
const aptRebootCheckHook = `// Virtual Private Node checks after each package change whether a restart is needed.
DPkg::Post-Invoke {"if [ -x ` + paths.BinaryPath + ` ]; then ` + paths.BinaryPath + ` reboot-check || true; fi";};
`

// InstallFail2ban installs the SSH intrusion-prevention service, once
// Debian's own job is not running.
func InstallFail2ban() error {
	job := newDebianAptJob()
	return job.run(func() error { return installPackages(job, "fail2ban") })
}

// ConfigureFail2ban writes the initial SSH jail and starts the service.
func ConfigureFail2ban() error {
	content := `# Virtual Private Node, stricter SSH login protection.
[sshd]
enabled = true
mode = aggressive
port = ssh
maxretry = 5
findtime = 600
bantime = 600
`
	if err := system.WriteFileRoot(paths.Fail2banJail,
		[]byte(content), 0644); err != nil {
		return err
	}
	if err := system.RunRoot("systemctl", "enable",
		"fail2ban"); err != nil {
		return err
	}
	return system.RunRoot("systemctl", "restart", "fail2ban")
}
