package host

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/system"
)

var (
	observeSSHForFirewall     = ObserveInitialSSHState
	readUFWDefaultForFirewall = func() (string, error) {
		data, err := system.ReadFileRoot(paths.UFWDefault)
		return string(data), err
	}
	writeUFWDefaultForFirewall = func(data []byte) error {
		return system.WriteFileRoot(paths.UFWDefault, data, 0o644)
	}
	runInitialFirewallCommand = func(args []string) error {
		return system.RunRoot(args[0], args[1:]...)
	}
)

// ConfigureInitialFirewall establishes the node's global UFW baseline during
// initial installation. It opens only the SSH ports.
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

	// Debian's ufw ships IPV6=yes. vpn sets IPV6=no: ufw then drops all IPv6
	// traffic in and out except loopback, so nothing leaves over IPv6 even if
	// IPv6 is turned on again later. This is the one file of Debian's that
	// vpn changes. The file's bytes are read and written back as they are but
	// for that value, so IPV6=yes and ufw reload give Debian's file again.
	ufwDefault, err := readUFWDefaultForFirewall()
	if err == nil {
		content := strings.ReplaceAll(
			ufwDefault, "IPV6=yes", "IPV6=no")
		if err := writeUFWDefaultForFirewall([]byte(content)); err != nil {
			return err
		}
	}

	commands := buildInitialFirewallCommands(obs.Ports)
	enable := commands[len(commands)-1]
	for _, args := range commands[:len(commands)-1] {
		if err := runInitialFirewallCommand(args); err != nil {
			return err
		}
	}
	// Asked once vpn's SSH rules are in, so removing an owner's rule never
	// shuts SSH, and before ufw is turned on, which would let that rule in.
	if err := checkAddedRules(obs.Ports); err != nil {
		return err
	}
	return runInitialFirewallCommand(enable)
}

// SaveFirewallRules keeps the firewall's rules as the firewall step left
// them, for CheckFirewallRules. It runs again with the step.
func SaveFirewallRules() error {
	rules, err := firewallRules()
	if err != nil {
		return err
	}
	return system.WriteFileRoot(paths.FirewallRules, []byte(rules), 0o600)
}

// CheckFirewallRules is the fact "the firewall's rules are as the install
// set them": ufw's tables, as iptables-save prints them, against the copy
// the firewall step kept. A rule in a table of its own is not in them, and
// cannot let traffic in past ufw's, but a table of its own can send another
// port on to sshd.
func CheckFirewallRules() error {
	saved, err := os.ReadFile(paths.FirewallRules)
	if err != nil {
		return fmt.Errorf("cannot read %s: %w", paths.FirewallRules, unwrapPath(err))
	}
	now, err := firewallRules()
	if err != nil {
		return err
	}
	return firewallVerdict(string(saved), now)
}

func firewallVerdict(saved, now string) error {
	if saved == now {
		return nil
	}
	old := strings.Split(saved, "\n")
	cur := strings.Split(now, "\n")
	// A rule can stand twice, so each line is counted.
	counts := func(lines []string) map[string]int {
		n := map[string]int{}
		for _, l := range lines {
			n[l]++
		}
		return n
	}
	had, has := counts(old), counts(cur)
	for _, line := range cur {
		if has[line] > had[line] {
			return fmt.Errorf("found a firewall rule the install did not set: %s. Undo the change, so only SSH reaches the machine", line)
		}
	}
	for _, line := range old {
		if had[line] > has[line] {
			return fmt.Errorf("found a firewall rule of the install gone: %s. Undo the change, so only SSH reaches the machine", line)
		}
	}
	return errors.New("found the firewall's rules in another order than the install set them. Undo the change, so only SSH reaches the machine")
}

// firewallRules is ufw's IPv4 and IPv6 tables, as the iptables programs on
// Debian's image print them. Their comment lines hold the time and the
// counters on each chain's line change with traffic, so both are left out.
func firewallRules() (string, error) {
	var b strings.Builder
	for _, program := range []string{"iptables-save", "ip6tables-save"} {
		out, err := askTool(questionTimeout, program)
		if err != nil {
			return "", fmt.Errorf("cannot read the firewall's rules: %w", err)
		}
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, "#") {
				continue
			}
			if strings.HasPrefix(line, ":") {
				if i := strings.LastIndex(line, " ["); i > 0 {
					line = line[:i] + " [0:0]"
				}
			}
			b.WriteString(line + "\n")
		}
	}
	return b.String(), nil
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
