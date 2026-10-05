package host

import (
	"fmt"
	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/system"
	"strings"
)

var (
	observeSSHForFirewall = ObserveInitialSSHState
	installUFWForFirewall = func() error {
		return system.RunRoot("apt-get", "install", "-y", "-qq", "ufw")
	}
	readUFWDefaultForFirewall = func() (string, error) {
		return system.RunRootOutput("cat", paths.UFWDefault)
	}
	writeUFWDefaultForFirewall = func(data []byte) error {
		return system.WriteFileRoot(paths.UFWDefault, data, 0o644)
	}
	runInitialFirewallCommand = func(args []string) error {
		return system.RunRoot(args[0], args[1:]...)
	}
)

// ConfigureInitialFirewall establishes the node's global UFW baseline during
// initial installation. Post-install features must not call it: they add and
// verify only the rules they own through the additive host operations.
//
// The initial operation re-observes sshd immediately before any firewall
// mutation. Observation failure refuses the rewrite; there is no cached-port
// or port-22 fallback.
func ConfigureInitialFirewall(cfg *config.AppConfig) error {
	if _, err := observeSSHForFirewall(); err != nil {
		return fmt.Errorf("observe SSH ports before firewall preparation: %w", err)
	}
	if err := installUFWForFirewall(); err != nil {
		return err
	}
	// Package installation can take long enough for sshd configuration to
	// change. Re-observe at the last responsible moment; only this fresh
	// answer is allowed to shape the rewrite below.
	obs, err := observeSSHForFirewall()
	if err != nil {
		return fmt.Errorf("observe SSH ports before firewall rewrite: %w", err)
	}

	ufwDefault, err := readUFWDefaultForFirewall()
	if err == nil {
		content := strings.ReplaceAll(
			ufwDefault, "IPV6=yes", "IPV6=no")
		if err := writeUFWDefaultForFirewall([]byte(content)); err != nil {
			return err
		}
	}

	commands := buildInitialFirewallCommands(cfg, obs.Ports)
	for _, args := range commands {
		if err := runInitialFirewallCommand(args); err != nil {
			return err
		}
	}
	return nil
}

func buildInitialFirewallCommands(
	cfg *config.AppConfig, sshPorts []int,
) [][]string {
	commands := [][]string{
		{"ufw", "default", "deny", "incoming"},
		{"ufw", "default", "allow", "outgoing"},
	}
	for _, p := range sshPorts {
		commands = append(commands,
			[]string{"ufw", "allow",
				fmt.Sprintf("%d/tcp", p)})
	}

	if cfg.HasLND() && cfg.P2PMode == "hybrid" {
		commands = append(commands,
			[]string{"ufw", "allow", "9735/tcp"})
		commands = append(commands,
			[]string{"ufw", "allow", "8080/tcp"})
	}

	// Syncthing sync protocol: clearnet direct connection.
	// Mutual TLS with explicit device approval ensures only
	// paired devices can connect.
	if cfg.SyncthingEnabled {
		commands = append(commands,
			[]string{"ufw", "allow", "22000/tcp"})
	}

	commands = append(commands,
		[]string{"ufw", "--force", "enable"})

	return commands
}
