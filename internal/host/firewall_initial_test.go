package host

import (
	"errors"
	"reflect"
	"testing"

	"github.com/virtpriv/node/internal/config"
)

func withInitialFirewallTestDeps(t *testing.T) {
	t.Helper()
	oldObserve := observeSSHForFirewall
	oldInstall := installUFWForFirewall
	oldRead := readUFWDefaultForFirewall
	oldWrite := writeUFWDefaultForFirewall
	oldRun := runInitialFirewallCommand
	t.Cleanup(func() {
		observeSSHForFirewall = oldObserve
		installUFWForFirewall = oldInstall
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
	installUFWForFirewall = func() error { mutated = true; return nil }
	readUFWDefaultForFirewall = func() (string, error) { mutated = true; return "", nil }
	writeUFWDefaultForFirewall = func([]byte) error { mutated = true; return nil }
	runInitialFirewallCommand = func([]string) error { mutated = true; return nil }
	if err := ConfigureInitialFirewall(config.Default()); err == nil {
		t.Fatal("firewall rewrite accepted unavailable SSH observation")
	}
	if mutated {
		t.Fatal("firewall path mutated before SSH observation succeeded")
	}
}

func TestConfigureFirewallUsesFreshObservedPorts(t *testing.T) {
	withInitialFirewallTestDeps(t)
	prepared := false
	observeSSHForFirewall = func() (SSHObservation, error) {
		if !prepared {
			return SSHObservation{Ports: []int{22}, PasswordAuth: true}, nil
		}
		return SSHObservation{Ports: []int{2222, 22022}, PasswordAuth: true}, nil
	}
	installUFWForFirewall = func() error { prepared = true; return nil }
	readUFWDefaultForFirewall = func() (string, error) { return "IPV6=yes\n", nil }
	writeUFWDefaultForFirewall = func([]byte) error { return nil }
	var got [][]string
	runInitialFirewallCommand = func(args []string) error {
		got = append(got, append([]string(nil), args...))
		return nil
	}
	cfg := config.Default()
	cfg.P2PMode = "hybrid"
	cfg.SyncthingEnabled = true
	if err := ConfigureInitialFirewall(cfg); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"ufw", "default", "deny", "incoming"},
		{"ufw", "default", "allow", "outgoing"},
		{"ufw", "allow", "2222/tcp"},
		{"ufw", "allow", "22022/tcp"},
		{"ufw", "allow", "9735/tcp"},
		{"ufw", "allow", "8080/tcp"},
		{"ufw", "allow", "22000/tcp"},
		{"ufw", "--force", "enable"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("commands\n got: %#v\nwant: %#v", got, want)
	}
}

func TestConfigureFirewallReobservesAfterPackagePreparation(t *testing.T) {
	withInitialFirewallTestDeps(t)
	calls := 0
	observeSSHForFirewall = func() (SSHObservation, error) {
		calls++
		if calls == 1 {
			return SSHObservation{Ports: []int{22}}, nil
		}
		return SSHObservation{}, errors.New("sshd changed during preparation")
	}
	installUFWForFirewall = func() error { return nil }
	rewritten := false
	readUFWDefaultForFirewall = func() (string, error) { rewritten = true; return "", nil }
	writeUFWDefaultForFirewall = func([]byte) error { rewritten = true; return nil }
	runInitialFirewallCommand = func([]string) error { rewritten = true; return nil }
	if err := ConfigureInitialFirewall(config.Default()); err == nil {
		t.Fatal("rewrite accepted failed final SSH observation")
	}
	if calls != 2 || rewritten {
		t.Fatalf("calls=%d rewritten=%v", calls, rewritten)
	}
}
