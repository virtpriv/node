package host

import (
	"fmt"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/system"
	"strings"
)

var (
	observeSSHForFirewall     = ObserveInitialSSHState
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
// initial installation. It opens only the SSH ports. Later features, such as
// the Hybrid upgrade and the Syncthing add-on, add and verify only the rules
// they own through the additive host operations and never call it.
//
// The step observes sshd right before the rewrite and opens the ports it
// listens on. Observation failure refuses the rewrite. There is no
// cached-port or port-22 fallback. ufw itself comes from the first package
// step.
func ConfigureInitialFirewall() error {
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

	commands := buildInitialFirewallCommands(obs.Ports)
	for _, args := range commands {
		if err := runInitialFirewallCommand(args); err != nil {
			return err
		}
	}
	return nil
}

func buildInitialFirewallCommands(sshPorts []int) [][]string {
	commands := [][]string{
		{"ufw", "default", "deny", "incoming"},
		{"ufw", "default", "allow", "outgoing"},
	}
	for _, p := range sshPorts {
		commands = append(commands,
			[]string{"ufw", "allow",
				fmt.Sprintf("%d/tcp", p)})
	}

	commands = append(commands,
		[]string{"ufw", "--force", "enable"})

	return commands
}
