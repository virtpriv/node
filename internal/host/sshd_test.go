package host

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/virtpriv/node/internal/sshkeys"
)

// realistic excerpt of sshd -T output: lowercase keywords,
// one "keyword value" pair per line, target buried mid-list.
const sshdTFixture = `port 22
addressfamily any
listenaddress [::]:22
listenaddress 0.0.0.0:22
usepam yes
pubkeyauthentication yes
passwordauthentication no
kbdinteractiveauthentication no
permitrootlogin no
x11forwarding no
`

func TestParsePasswordAuth(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    bool
		wantErr bool
	}{
		{"realistic no", sshdTFixture, false, false},
		{"realistic yes", strings.Replace(sshdTFixture, "passwordauthentication no", "passwordauthentication yes", 1), true, false},
		{"single line yes", "passwordauthentication yes\n", true, false},
		{"single line no", "passwordauthentication no\n", false, false},
		{"mixed case", "PasswordAuthentication Yes\n", true, false},
		{"unknown value", "passwordauthentication maybe\n", false, true},
		{"absent", "port 22\nusepam yes\n", false, true},
		{"empty", "", false, true},
		{"substring", "somepasswordauthentication yes\npasswordauthenticationx yes\n", false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parsePasswordAuth(tt.input)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("got %v, error %v; want %v, error %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestSSHApplyValidatesRestoresAndDoesNotRestartOnFailure(t *testing.T) {
	for _, tc := range []struct {
		name                                                     string
		wantEvents                                               string
		exists                                                   bool
		readErr, writeErr, validationErr, restoreErr, restartErr error
	}{
		{name: "success", wantEvents: "read,write,validate,restart", exists: true},
		{name: "read failure", wantEvents: "read", exists: true, readErr: errors.New("denied")},
		{name: "write failure", wantEvents: "read,write", exists: true, writeErr: errors.New("disk full")},
		{name: "restore previous", wantEvents: "read,write,validate,restore", exists: true, validationErr: errors.New("bad config")},
		{name: "remove new", wantEvents: "read,write,validate,remove", validationErr: errors.New("bad config")},
		{name: "restore failure", wantEvents: "read,write,validate,restore", exists: true, validationErr: errors.New("bad config"), restoreErr: errors.New("denied")},
		{name: "restart failure", wantEvents: "read,write,validate,restart", exists: true, restartErr: errors.New("restart failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var events []string
			ops := sshdOps{
				read: func() ([]byte, error) {
					events = append(events, "read")
					if tc.readErr != nil {
						return nil, tc.readErr
					}
					if !tc.exists {
						return nil, os.ErrNotExist
					}
					return []byte("previous"), nil
				},
				write: func(data []byte) error {
					if string(data) == "previous" {
						events = append(events, "restore")
						return tc.restoreErr
					}
					events = append(events, "write")
					return tc.writeErr
				},
				remove: func() error { events = append(events, "remove"); return tc.restoreErr },
				validate: func() (string, error) {
					events = append(events, "validate")
					return "config rejected", tc.validationErr
				},
				restart: func() error { events = append(events, "restart"); return tc.restartErr },
			}
			err := applySSHHardening("no", ops)
			if strings.Join(events, ",") != tc.wantEvents {
				t.Fatalf("operations %v, want %s", events, tc.wantEvents)
			}
			if (err != nil) != (tc.name != "success") {
				t.Fatalf("unexpected result %v", err)
			}
			if tc.restoreErr != nil && !strings.Contains(err.Error(), "restoring") {
				t.Fatal("restore failure hidden")
			}
		})
	}
}

func TestSSHDisableGuardPrecedesHostWrites(t *testing.T) {
	dir := t.TempDir()
	store := sshkeys.Store{Path: filepath.Join(dir, "authorized_keys")}
	valid := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEB"
	zeroModulusRSA := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAAAAA==\n"
	for _, content := range []string{"", "ssh-ed25519 YQ==\n", zeroModulusRSA, "command=\"false\" " + valid + "\n", valid + "\n"} {
		if err := os.WriteFile(store.Path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		applied := false
		err := rebuildSSHConfig(true, store, func(value string) error {
			applied = true
			if value != "no" {
				t.Fatal("wrong desired setting")
			}
			if err := store.WithLock(func() error { t.Fatal("key edit overlapped disabling"); return nil }); err == nil {
				t.Fatal("guard did not hold lock")
			}
			return nil
		}, func() (bool, error) { return false, nil })
		want := content == valid+"\n"
		if applied != want || (err == nil) != want {
			t.Fatalf("content %q applied %v error %v", content, applied, err)
		}
	}
	if err := rebuildSSHConfig(false, store, func(string) error { return nil }, func() (bool, error) { return false, nil }); err == nil {
		t.Fatal("effective mismatch reported success")
	}
	if err := rebuildSSHConfig(false, store, func(string) error { return nil }, func() (bool, error) { return false, errors.New("observation failed") }); err == nil {
		t.Fatal("failed observation reported success")
	}
}

// Use the installed OpenSSH parser: substring checks cannot prove Match scope,
// Include behavior, or precedence against provider settings.
func TestOwnerSSHPolicyWithNativeParser(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("native Linux SSH parser")
	}
	sshd, err := exec.LookPath("sshd")
	if err != nil {
		if os.Getenv("VPN_REQUIRE_ROOT_TESTS") == "1" {
			t.Fatal(err)
		}
		t.Skip("sshd unavailable")
	}
	if _, err := os.Stat("/run/sshd"); err != nil {
		if os.Getenv("VPN_REQUIRE_ROOT_TESTS") == "1" {
			t.Fatal(err)
		}
		t.Skip("sshd runtime directory unavailable")
	}
	dir := t.TempDir()
	key := filepath.Join(dir, "host_key")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("host key fixture: %s %v", out, err)
	}
	policy := filepath.Join(dir, "owner.conf")
	config := filepath.Join(dir, "sshd_config")
	for _, setting := range []string{"yes", "no"} {
		for _, provider := range []string{"yes", "no"} {
			if err := os.WriteFile(policy, []byte(buildHardeningDropIn(setting)), 0600); err != nil {
				t.Fatal(err)
			}
			body := "HostKey " + key + "\nInclude " + policy + "\nUsePAM yes\nPasswordAuthentication " + provider + "\nX11Forwarding yes\n"
			if err := os.WriteFile(config, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			outputs := map[string]string{}
			for _, user := range []string{"vpn", "root", "deployment"} {
				out, err := exec.Command(sshd, "-T", "-f", config, "-C", "user="+user+",host=example,addr=192.0.2.1").CombinedOutput()
				if err != nil {
					t.Fatalf("sshd policy: %v %s", err, out)
				}
				outputs[user] = string(out)
			}
			if err := verifySSHPolicy(setting, outputs["vpn"], outputs["root"]); err != nil {
				t.Fatal(err)
			}
			enabled, err := parsePasswordAuth(outputs["deployment"])
			if err != nil || enabled != (provider == "yes") || !strings.Contains(outputs["deployment"], "x11forwarding yes") {
				t.Fatal("changed deployment account policy")
			}
			// An earlier matching provider rule must cause refusal, not false success.
			conflict := "Match User vpn\n PasswordAuthentication " + map[string]string{"yes": "no", "no": "yes"}[setting] + "\nMatch all\n"
			// Includes restore global parsing state; put the conflict in an earlier include.
			earlier := filepath.Join(dir, "earlier.conf")
			if err := os.WriteFile(earlier, []byte(conflict), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(config, []byte("Include "+earlier+"\n"+body), 0600); err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command(sshd, "-T", "-f", config, "-C", "user=vpn,host=example,addr=192.0.2.1").CombinedOutput()
			if err != nil {
				t.Fatalf("conflict fixture invalid: %s %v", out, err)
			}
			if verifySSHPolicy(setting, string(out), outputs["root"]) == nil {
				t.Fatal("conflicting effective authentication accepted")
			}
		}
	}
}

func TestSSHPolicyRefusesRestrictionsBeforeActivation(t *testing.T) {
	owner := "passwordauthentication yes\npubkeyauthentication yes\npermittty yes\nforcecommand none\nchrootdirectory none\nauthenticationmethods any\n"
	root := "permitrootlogin no\n"
	for _, pair := range [][2]string{{"pubkeyauthentication yes", "pubkeyauthentication no"}, {"permittty yes", "permittty no"}, {"forcecommand none", "forcecommand /bin/false"}, {"chrootdirectory none", "chrootdirectory /srv/jail"}, {"authenticationmethods any", "authenticationmethods publickey,password"}} {
		if verifySSHPolicy("yes", strings.Replace(owner, pair[0], pair[1], 1), root) == nil {
			t.Fatalf("accepted %s", pair[1])
		}
	}
	if verifySSHPolicy("yes", owner, "permitrootlogin yes\n") == nil {
		t.Fatal("accepted root SSH")
	}
}
