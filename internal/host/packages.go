package host

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/virtpriv/node/internal/system"
)

// UpgradePackages preserves existing configuration on conflict. Installation
// chooses when to run it; runtime package updates first refresh package lists.
// Both root entry points set noninteractive apt and needrestart environments.
func UpgradePackages() error {
	return upgradePackages(system.RunRoot)
}

func upgradePackages(run func(string, ...string) error) error {
	return run("apt-get", "upgrade", "-y", "-qq",
		"-o", "Dpkg::Options::=--force-confdef",
		"-o", "Dpkg::Options::=--force-confold")
}

// UpdatePackages completes accepted package work independently of the observer.
// Do not attach the terminal's cancellation to apt/dpkg: a lost connection must
// not interrupt package configuration. Apt retains its installed Tor routing,
// download timeouts and retries; only the read-only final audit has a deadline.
func UpdatePackages(progress func(int)) error {
	if os.Geteuid() != 0 {
		return errors.New("package updates require the root helper")
	}
	return updatePackages(system.RunRoot, system.RunOutputWithTimeout, progress)
}

func updatePackages(run func(string, ...string) error, output func(time.Duration, string, ...string) (string, error), progress func(int)) error {
	if err := run("apt-get", "update", "--error-on=any", "-qq"); err != nil {
		return fmt.Errorf("refresh package lists: %w", err)
	}
	progress(0)
	if err := upgradePackages(run); err != nil {
		return fmt.Errorf("upgrade packages: %w", err)
	}
	progress(1)
	out, err := output(10*time.Second, "dpkg", "--audit")
	if err != nil {
		return fmt.Errorf("package consistency audit failed: %w", err)
	}
	if strings.TrimSpace(out) != "" {
		return fmt.Errorf("package consistency audit reported problems: %s", strings.TrimSpace(out))
	}
	return nil
}
