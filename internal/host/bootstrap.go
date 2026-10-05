package host

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/virtpriv/node/internal/logger"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/system"
)

// InstallInitialBinary places the running executable at the fixed VPN path.
// Only the admitted base installer calls this; it is not an update operation.
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
func InstallBasePackages() error {
	if err := system.RunRoot("apt-get", "update", "-qq"); err != nil {
		return err
	}
	return system.RunRoot("apt-get", "install", "-y", "-qq",
		"sudo", "gnupg", "tor", "torsocks", "wget", "curl", "ufw")
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
	// NTP remains best effort; inability to enable it is logged.
	if err := system.RunRootSilent(
		"timedatectl", "set-ntp", "true"); err != nil {
		logger.Install(
			"WARNING: could not enable NTP sync (%v)", err)
	}
	return nil
}

// DisableIPv6 applies the base appliance IPv4 policy.
func DisableIPv6() error {
	content := `# Virtual Private Node — disable IPv6
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

// InstallUnattendedUpgrades installs Debian's security-update tooling.
func InstallUnattendedUpgrades() error {
	return system.RunRoot("apt-get", "install", "-y", "-qq",
		"unattended-upgrades", "apt-listchanges")
}

// ConfigureUnattendedUpgrades sets the appliance's security-only apt policy,
// including the existing automatic reboot schedule.
func ConfigureUnattendedUpgrades() error {
	autoConf := `APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
APT::Periodic::AutocleanInterval "7";
`
	if err := system.WriteFileRoot(paths.AutoUpgrades,
		[]byte(autoConf), 0644); err != nil {
		return err
	}

	upgradeConf := `// Virtual Private Node — Unattended Upgrades
Unattended-Upgrade::Allowed-Origins {
    "${distro_id}:${distro_codename}-security";
};
Unattended-Upgrade::Automatic-Reboot "true";
Unattended-Upgrade::Automatic-Reboot-Time "04:00";
Unattended-Upgrade::Remove-Unused-Kernel-Packages "true";
Unattended-Upgrade::Remove-Unused-Dependencies "true";
`
	return system.WriteFileRoot(paths.UnattendedUpgrades,
		[]byte(upgradeConf), 0644)
}

// InstallFail2ban installs the SSH intrusion-prevention service.
func InstallFail2ban() error {
	return system.RunRoot("apt-get", "install",
		"-y", "-qq", "fail2ban")
}

// ConfigureFail2ban writes the initial SSH jail and starts the service.
func ConfigureFail2ban() error {
	content := `# Virtual Private Node — Fail2ban
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

// ConfigureAptTor routes subsequent apt downloads through Tor and bounds
// download retries. Package configuration remains unbounded so callers do not
// kill dpkg during a transaction. Installer must pass its Tor gate first.
func ConfigureAptTor() error {
	content := `Acquire::http::Proxy "socks5h://127.0.0.1:9050";
Acquire::https::Proxy "socks5h://127.0.0.1:9050";
Acquire::http::Timeout "60";
Acquire::https::Timeout "60";
Acquire::Retries "3";
`
	return system.WriteFileRoot(paths.AptTorProxy,
		[]byte(content), 0644)
}

// EnsureGPG installs the verifier when the admitted installation needs it.
func EnsureGPG() error {
	if _, err := exec.LookPath("gpg"); err == nil {
		return nil
	}
	return system.RunRoot("apt-get", "install", "-y", "-qq", "gnupg")
}
