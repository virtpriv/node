package host

import (
	"errors"
	"reflect"
	"testing"
)

func withInitialFirewallTestDeps(t *testing.T) {
	t.Helper()
	oldObserve := observeSSHForFirewall
	oldRead := readUFWDefaultForFirewall
	oldWrite := writeUFWDefaultForFirewall
	oldRun := runInitialFirewallCommand
	t.Cleanup(func() {
		observeSSHForFirewall = oldObserve
		readUFWDefaultForFirewall = oldRead
		writeUFWDefaultForFirewall = oldWrite
		runInitialFirewallCommand = oldRun
	})
}

func TestConfigureFirewallRefusesBeforeMutationWhenSSHObservationFails(t *testing.T) {
	withInitialFirewallTestDeps(t)
	observeSSHForFirewall = func() (SSHObservation, error) {
		return SSHObservation{}, errors.New("sshd unavailable")
	}
	mutated := false
	readUFWDefaultForFirewall = func() (string, error) { mutated = true; return "", nil }
	writeUFWDefaultForFirewall = func([]byte) error { mutated = true; return nil }
	runInitialFirewallCommand = func([]string) error { mutated = true; return nil }
	if err := ConfigureInitialFirewall(); err == nil {
		t.Fatal("firewall rewrite accepted unavailable SSH observation")
	}
	if mutated {
		t.Fatal("firewall path mutated before SSH observation succeeded")
	}
}

func TestConfigureFirewallOpensTheObservedPorts(t *testing.T) {
	withInitialFirewallTestDeps(t)
	observeSSHForFirewall = func() (SSHObservation, error) {
		return SSHObservation{Ports: []int{2222, 22022}, PasswordAuth: true}, nil
	}
	readUFWDefaultForFirewall = func() (string, error) { return "IPV6=yes\n", nil }
	writeUFWDefaultForFirewall = func([]byte) error { return nil }
	var got [][]string
	runInitialFirewallCommand = func(args []string) error {
		got = append(got, append([]string(nil), args...))
		return nil
	}
	if err := ConfigureInitialFirewall(); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"ufw", "default", "deny", "incoming"},
		{"ufw", "default", "allow", "outgoing"},
		{"ufw", "allow", "2222/tcp"},
		{"ufw", "allow", "22022/tcp"},
		{"ufw", "--force", "enable"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("commands\n got: %#v\nwant: %#v", got, want)
	}
}
