package host

// Initial SSH observation uses root's connection context before the operator
// exists. Runtime operator password-auth observation remains a separate query.

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/virtpriv/node/internal/system"
)

// SSHObservation is an effective configuration snapshot for initial SSH setup.
type SSHObservation struct {
	// Ports are the listening ports, deduplicated, sorted.
	Ports []int
	// PasswordAuth reports whether password authentication is
	// effectively enabled.
	PasswordAuth bool
}

// ObserveInitialSSHState queries effective SSH configuration for initial setup.
// Preflight and firewall callers refuse on error. Owner password access is
// established separately by the installer's SSH hardening step.
func ObserveInitialSSHState() (SSHObservation, error) {
	out, err := system.RunRootOutput("sshd", "-T",
		"-C", "user=root,host=localhost,addr=127.0.0.1")
	if err != nil {
		return SSHObservation{}, fmt.Errorf(
			"query effective sshd config: %w", err)
	}
	return parseSSHObservation(out)
}

// parseSSHObservation extracts ports and password-auth state
// from sshd -T output (one "keyword value..." pair per line,
// keywords lowercased by sshd).
//
// Ports are the UNION of every `port` line and every
// `listenaddress host:port` line: a ListenAddress with an
// explicit port makes sshd listen there even when it differs
// from the Port directive. The union direction is fail-safe
// for a firewall allow-list: allowing a port sshd does not
// listen on wastes a rule; missing one it does listen on is
// the lockout.
func parseSSHObservation(sshdOutput string) (SSHObservation, error) {
	portSet := map[int]bool{}
	pwSeen := false
	pw := false

	for _, line := range strings.Split(sshdOutput, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch strings.ToLower(fields[0]) {
		case "port":
			if p, err := strconv.Atoi(fields[1]); err == nil &&
				p > 0 && p < 65536 {
				portSet[p] = true
			}
		case "listenaddress":
			if p, ok := portFromListenAddress(fields[1]); ok {
				portSet[p] = true
			}
		case "passwordauthentication":
			// First match wins, like sshd itself.
			if !pwSeen {
				pw = strings.EqualFold(fields[1], "yes")
				pwSeen = true
			}
		}
	}

	if len(portSet) == 0 {
		return SSHObservation{}, errors.New(
			"no listening port found in sshd -T output")
	}
	if !pwSeen {
		return SSHObservation{}, errors.New(
			"passwordauthentication not present in sshd -T output")
	}

	ports := make([]int, 0, len(portSet))
	for p := range portSet {
		ports = append(ports, p)
	}
	sort.Ints(ports)
	return SSHObservation{Ports: ports, PasswordAuth: pw}, nil
}

// portFromListenAddress extracts the port from a
// ListenAddress value as printed by sshd -T: "0.0.0.0:22",
// "[::]:22", or an address with no port (no port to extract).
func portFromListenAddress(addr string) (int, bool) {
	i := strings.LastIndex(addr, ":")
	if i < 0 || i == len(addr)-1 {
		return 0, false
	}
	// Bare IPv6 without brackets has colons but no port
	// separator we can trust; only accept a numeric suffix
	// that follows either a bracket or a dotted/hostname
	// form ("]:" or a single-colon value).
	if strings.Count(addr, ":") > 1 && !strings.Contains(addr, "]:") {
		return 0, false
	}
	p, err := strconv.Atoi(addr[i+1:])
	if err != nil || p <= 0 || p >= 65536 {
		return 0, false
	}
	return p, true
}
