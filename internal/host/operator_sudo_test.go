package host

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const ownerSudoListing = `Matching Defaults entries for vpn on node:
    env_reset, mail_badpass, use_pty, authenticate, !rootpw, !targetpw, !runaspw, !exempt_group

User vpn may run the following commands on node:

Sudoers entry: /etc/sudoers.d/vpn
    RunAsUsers: ALL
    RunAsGroups: ALL
    Options: authenticate
    Commands:
        ALL
`

func TestEffectiveOwnerSudoPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, out string
		ok        bool
	}{
		{"owner grant", ownerSudoListing, true},
		{"missing include", "User vpn is not allowed to run sudo on node.", false},
		{"limited commands", strings.Replace(ownerSudoListing, "        ALL", "        /usr/bin/true", 1), false},
		{"passwordless rule", ownerSudoListing + "\nSudoers entry:\n Options: !authenticate\n Commands:\n  /usr/bin/passwd\n", false},
		{"authentication disabled", strings.Replace(ownerSudoListing, "Options: authenticate", "Options: !authenticate", 1), false},
		{"root password", strings.ReplaceAll(ownerSudoListing, "!rootpw", "rootpw"), false},
		{"target password", strings.ReplaceAll(ownerSudoListing, "!targetpw", "targetpw"), false},
		{"runas password", strings.ReplaceAll(ownerSudoListing, "!runaspw", "runaspw"), false},
		{"exempt group", strings.ReplaceAll(ownerSudoListing, "!exempt_group", "exempt_group=owners"), false},
		{"command denial", ownerSudoListing + "\nSudoers entry: /etc/sudoers.d/host\n RunAsUsers: root\n Commands:\n  !/usr/bin/passwd\n", false},
		{"command password bypass", "Runas and Command-specific defaults for vpn:\n Defaults!/usr/bin/passwd !authenticate\n" + ownerSudoListing, false},
		{"accepts host timeout setting", strings.Replace(ownerSudoListing, "env_reset,", "timestamp_timeout=5, env_reset,", 1), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := verifyOperatorSudoListing(tc.out); (err == nil) != tc.ok {
				t.Fatalf("verification: %v", err)
			}
		})
	}
}

// requireLinuxRoot skips a test that needs root on Linux, or fails it when
// VPN_REQUIRE_ROOT_TESTS asks for those tests to run.
func requireLinuxRoot(t *testing.T) {
	t.Helper()
	required := os.Getenv("VPN_REQUIRE_ROOT_TESTS")
	if required != "" && required != "1" {
		t.Fatal("VPN_REQUIRE_ROOT_TESTS must be unset or 1")
	}
	if runtime.GOOS == "linux" && os.Geteuid() == 0 {
		return
	}
	if required == "1" {
		t.Fatal("this test requires Linux root")
	}
	t.Skip("this test requires Linux root")
}

func TestRootOwnerSudoPublicationAndRefusal(t *testing.T) {
	requireLinuxRoot(t)
	for _, phase := range []string{"existing policy invalid", "candidate invalid", "effective conflict", "success"} {
		t.Run(phase, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "vpn")
			failure := errors.New("injected policy failure")
			run := func(_ string, args ...string) (string, error) {
				if phase == "existing policy invalid" && len(args) == 1 {
					return "", failure
				}
				if len(args) == 2 {
					data, err := os.ReadFile(args[1])
					if err != nil || string(data) != operatorSudoPolicy {
						t.Fatal("candidate validation did not read staged policy")
					}
					if _, err := os.Lstat(path); !os.IsNotExist(err) {
						t.Fatal("policy published before candidate validation")
					}
					if phase == "candidate invalid" {
						return "", failure
					}
				}
				return "", nil
			}
			verify := func() error {
				if _, err := os.Stat(path); err != nil {
					t.Fatal("effective check ran before publication")
				}
				if phase == "effective conflict" {
					return failure
				}
				return nil
			}
			err := provisionOperatorSudo(path, run, verify)
			if phase != "success" {
				if !errors.Is(err, failure) {
					t.Fatalf("lost failure: %v", err)
				}
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatal("failed policy left published grant")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			before, _ := os.Stat(path)
			if err := provisionOperatorSudo(path, func(string, ...string) (string, error) { t.Fatal("resume rewrote validated policy"); return "", nil }, verify); err != nil {
				t.Fatal(err)
			}
			after, _ := os.Stat(path)
			if !os.SameFile(before, after) {
				t.Fatal("resume replaced grant")
			}
		})
	}
}

func TestRootOwnerSudoRefusesChangedPolicyWithoutOverwriting(t *testing.T) {
	requireLinuxRoot(t)
	for _, change := range []string{"permissions", "contents"} {
		t.Run(change, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "vpn")
			policy, mode := operatorSudoPolicy, os.FileMode(0440)
			if change == "permissions" {
				mode = 0600
			} else {
				// Keep the same length and safe metadata so only the content
				// check can reject this different authentication policy.
				policy = strings.Replace(policy, "!rootpw", " rootpw", 1)
			}
			if err := os.WriteFile(path, []byte(policy), mode); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			err = provisionOperatorSudo(path,
				func(string, ...string) (string, error) { return "", nil },
				func() error { return nil })
			if err == nil {
				t.Fatal("accepted changed owner policy")
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != policy {
				t.Fatal("changed existing policy contents")
			}
			after, err := os.Stat(path)
			if err != nil || !os.SameFile(before, after) || after.Mode() != before.Mode() {
				t.Fatal("replaced or changed permissions of existing policy")
			}
		})
	}
}

func TestOwnerSudoRuleWithNativeParser(t *testing.T) {
	visudo, err := exec.LookPath("visudo")
	if err != nil {
		if os.Getenv("VPN_REQUIRE_ROOT_TESTS") == "1" {
			t.Fatal(err)
		}
		t.Skip("visudo unavailable")
	}
	path := filepath.Join(t.TempDir(), "owner-policy")
	if err := os.WriteFile(path, []byte(operatorSudoPolicy), 0440); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(visudo, "-cf", path).CombinedOutput(); err != nil {
		t.Fatalf("native sudo parser refused policy: %s %v", out, err)
	}
}
