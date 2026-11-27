package host

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

// ufwMulticastPorts are the ports ufw's own rules let in besides the SSH
// ports vpn opens: mDNS and SSDP, each to its multicast address
// (/etc/ufw/before.rules). DHCP replies to UDP port 68 are let in too, and
// only the machine's DHCP client listens there.
var ufwMulticastPorts = map[string]string{"5353": "mDNS", "1900": "SSDP"}

// CheckOnlySSHAnswers is the fact "only SSH answers from outside the
// machine". It asks ufw for the rules added to it, which must be vpn's SSH
// rules alone, since a machine can hold ufw with rules of its own before the
// install. The firewall drops every other port, so it then asks the kernel
// which programs listen on the two ports ufw's own rules let in. It is asked
// right after the firewall step and when the install finishes.
func CheckOnlySSHAnswers() error {
	obs, err := observeSSHForFirewall()
	if err != nil {
		return fmt.Errorf("observe SSH ports: %w", err)
	}
	if err := checkAddedRules(obs.Ports); err != nil {
		return err
	}
	out, err := askTool(questionTimeout, "ss", "-Hlnup")
	if err != nil {
		return fmt.Errorf("cannot read which programs listen: %w", err)
	}
	return listenersVerdict(out)
}

var askAddedRules = func() (string, error) {
	return askTool(questionTimeout, "ufw", "show", "added")
}

// checkAddedRules asks ufw for the rules added to it, which must be vpn's
// SSH rules alone.
func checkAddedRules(sshPorts []int) error {
	added, err := askAddedRules()
	if err != nil {
		return fmt.Errorf("cannot read the firewall's rules: %w", err)
	}
	return addedRulesVerdict(added, sshPorts)
}

// addedRulesVerdict reads ufw show added, which prints a heading and then
// each rule as the command that added it, or "(None)".
func addedRulesVerdict(out string, sshPorts []int) error {
	var f findings
	for _, rule := range strings.Split(out, "\n")[1:] {
		rule = strings.TrimSpace(rule)
		vpns := slices.ContainsFunc(sshPorts, func(p int) bool { return rule == fmt.Sprintf("ufw allow %d/tcp", p) })
		if rule != "" && rule != "(None)" && !vpns {
			f.add("found the firewall rule %s, which vpn did not set", rule)
		}
	}
	return f.err("remove", "so only SSH answers from outside")
}

// listenersVerdict reads ss's lines. Asked for UDP only, ss prints no
// protocol column: the state, two queue sizes, the local address and port,
// the peer and the program.
func listenersVerdict(out string) error {
	var found []string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		i := strings.LastIndex(fields[3], ":")
		if i < 0 {
			continue
		}
		address, port := fields[3][:i], fields[3][i+1:]
		kind, ok := ufwMulticastPorts[port]
		if !ok || isLoopback(address) {
			continue
		}
		program := "a program"
		if len(fields) > 5 {
			if name, _, ok := strings.Cut(strings.TrimPrefix(fields[5], `users:(("`), `"`); ok {
				program = name
			}
		}
		found = append(found, fmt.Sprintf("found %s listening on UDP port %s (%s), which the firewall lets in. Remove it or stop its service, so only SSH answers from outside", program, port, kind))
	}
	if len(found) == 0 {
		return nil
	}
	return errors.New(strings.Join(found, "\n\n"))
}

// isLoopback reads an address as ss prints it: "*", "0.0.0.0", "[::1]" or
// one with the card's name after "%".
func isLoopback(address string) bool {
	address = strings.Trim(address, "[]")
	address, _, _ = strings.Cut(address, "%")
	a, err := netip.ParseAddr(address)
	return err == nil && a.IsLoopback()
}
