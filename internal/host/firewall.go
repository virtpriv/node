package host

import (
	"errors"
	"fmt"
	"strings"

	"github.com/virtpriv/node/internal/system"
)

var (
	readUFWStatusForFeature = func() (string, error) {
		return system.RunRootOutput(
			"env", "LC_ALL=C", "ufw", "status")
	}
	runFirewallCommand = func(args []string) error { return system.RunRoot(args[0], args[1:]...) }
)

// RequireActiveFirewall is the shared prerequisite for additive post-install
// firewall changes. A feature operation never installs, enables, or repairs
// UFW; an inactive firewall is an inconsistent base-node state that must be
// reported before the feature mutates anything.
func RequireActiveFirewall() error {
	status, err := readUFWStatusForFeature()
	if err != nil {
		return fmt.Errorf("read UFW status: %w", err)
	}
	if !ufwStatusActive(status) {
		return errors.New(
			"UFW is not active: refusing post-install firewall mutation")
	}
	return nil
}

func ufwStatusActive(status string) bool {
	for _, line := range strings.Split(status, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		return line == "Status: active"
	}
	return false
}

func ufwAllowsTCPPort(status string, port int) bool {
	want := fmt.Sprintf("%d/tcp", port)
	for _, line := range strings.Split(status, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == want && fields[1] == "ALLOW" {
			return true
		}
	}
	return false
}

// allowOwnedFirewallRules performs an additive-only UFW change. It rechecks
// the prerequisite immediately before mutation, adds no defaults or unrelated
// rules, never enables UFW, and verifies each requested rule from live status
// before reporting success. A retry is safe because `ufw allow` is idempotent.
func allowOwnedFirewallRules(ports ...int) error {
	if err := RequireActiveFirewall(); err != nil {
		return err
	}
	for _, port := range ports {
		if err := runFirewallCommand([]string{
			"ufw", "allow", fmt.Sprintf("%d/tcp", port),
		}); err != nil {
			return fmt.Errorf("allow %d/tcp: %w", port, err)
		}
	}
	status, err := readUFWStatusForFeature()
	if err != nil {
		return fmt.Errorf("verify UFW rules: %w", err)
	}
	if !ufwStatusActive(status) {
		return errors.New("UFW became inactive while adding feature rules")
	}
	for _, port := range ports {
		if !ufwAllowsTCPPort(status, port) {
			return fmt.Errorf(
				"UFW did not report required %d/tcp allow rule", port)
		}
	}
	return nil
}

func allowHybridP2PFirewallRules() error {
	return allowOwnedFirewallRules(9735, 8080)
}

func AllowSyncthingFirewallRule() error {
	return allowOwnedFirewallRules(22000)
}
