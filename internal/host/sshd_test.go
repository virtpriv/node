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
				t.Fatalf("got %v, error %v, want %v, error %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

// The screens' read counts keyboard-interactive as password login: Debian's
// keyboard-interactive asks PAM for the same password.
func TestOwnerPasswordLogin(t *testing.T) {
	for _, tc := range []struct {
		name    string
		output  string
		want    bool
		wantErr bool
	}{
		{"both off", sshdTFixture, false, false},
		{"password on", strings.Replace(sshdTFixture, "passwordauthentication no", "passwordauthentication yes", 1), true, false},
		{"keyboard-interactive on", strings.Replace(sshdTFixture, "kbdinteractiveauthentication no", "kbdinteractiveauthentication yes", 1), true, false},
		{"keyboard-interactive on without PAM", strings.Replace(strings.Replace(sshdTFixture, "kbdinteractiveauthentication no", "kbdinteractiveauthentication yes", 1), "usepam yes", "usepam no", 1), false, false},
		{"keyboard-interactive missing", strings.Replace(sshdTFixture, "kbdinteractiveauthentication no\n", "", 1), false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ownerPasswordLogin(tc.output)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("got %v, error %v, want %v, error %v", got, err, tc.want, tc.wantErr)
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
		// notConfirmed: the new setting may be on disk, so sshd may use
		// it now or at its next start.
		notConfirmed bool
	}{
		{name: "success", wantEvents: "read,write,validate,restart", exists: true},
		{name: "read failure", wantEvents: "read", exists: true, readErr: errors.New("denied")},
		{name: "write failure", wantEvents: "read,write", exists: true, writeErr: errors.New("disk full"), notConfirmed: true},
		{name: "restore previous", wantEvents: "read,write,validate,restore", exists: true, validationErr: errors.New("bad config")},
		{name: "remove new", wantEvents: "read,write,validate,remove", validationErr: errors.New("bad config")},
		{name: "restore failure", wantEvents: "read,write,validate,restore", exists: true, validationErr: errors.New("bad config"), restoreErr: errors.New("denied"), notConfirmed: true},
		{name: "restart failure", wantEvents: "read,write,validate,restart", exists: true, restartErr: errors.New("restart failed"), notConfirmed: true},
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
			if tc.restoreErr != nil && !strings.Contains(err.Error(), tc.restoreErr.Error()) {
				t.Fatal("restore failure hidden")
			}
			if errors.Is(err, ErrSSHSettingNotConfirmed) != tc.notConfirmed {
				t.Fatalf("not confirmed %v, want %v: %v", !tc.notConfirmed, tc.notConfirmed, err)
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
			// Includes restore global parsing state, so each conflict goes in an earlier include.
			// A list of accounts refuses wherever it stands.
			earlier := filepath.Join(dir, "earlier.conf")
			for _, conflict := range []struct{ earlier, later string }{
				{earlier: "Match User vpn\n PasswordAuthentication " + map[string]string{"yes": "no", "no": "yes"}[setting] + "\nMatch all\n"},
				{earlier: "Match User vpn\n KbdInteractiveAuthentication yes\nMatch all\n"},
				{earlier: "Match all\n X11Forwarding yes\n"},
				{later: "AllowUsers root\n"},
			} {
				if err := os.WriteFile(earlier, []byte(conflict.earlier), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(config, []byte("Include "+earlier+"\n"+body+conflict.later), 0600); err != nil {
					t.Fatal(err)
				}
				out, err := exec.Command(sshd, "-T", "-f", config, "-C", "user=vpn,host=example,addr=192.0.2.1").CombinedOutput()
				if err != nil {
					t.Fatalf("conflict fixture invalid: %s %v", out, err)
				}
				if verifySSHPolicy(setting, string(out), outputs["root"]) == nil {
					t.Fatalf("conflicting effective policy accepted: %q", conflict)
				}
			}
		}
	}
}

func TestSSHPolicyRefusesRestrictionsBeforeActivation(t *testing.T) {
	owner := "passwordauthentication yes\nkbdinteractiveauthentication no\npubkeyauthentication yes\npermittty yes\nx11forwarding no\nforcecommand none\nchrootdirectory none\nauthenticationmethods any\n"
	root := "permitrootlogin no\n"
	if err := verifySSHPolicy("yes", owner, root); err != nil {
		t.Fatalf("refused the policy vpn writes: %v", err)
	}
	for _, pair := range [][2]string{{"pubkeyauthentication yes", "pubkeyauthentication no"}, {"permittty yes", "permittty no"}, {"forcecommand none", "forcecommand /bin/false"}, {"chrootdirectory none", "chrootdirectory /srv/jail"}, {"authenticationmethods any", "authenticationmethods publickey,password"},
		{"kbdinteractiveauthentication no", "kbdinteractiveauthentication yes"}, {"x11forwarding no", "x11forwarding yes"},
		{"authenticationmethods any", "authenticationmethods any\nallowusers root"}, {"authenticationmethods any", "authenticationmethods any\ndenyusers vpn"},
		{"authenticationmethods any", "authenticationmethods any\nallowgroups sudo"}, {"authenticationmethods any", "authenticationmethods any\ndenygroups users"}} {
		if verifySSHPolicy("yes", strings.Replace(owner, pair[0], pair[1], 1), root) == nil {
			t.Fatalf("accepted %s", pair[1])
		}
	}
	if verifySSHPolicy("yes", owner, "permitrootlogin yes\n") == nil {
		t.Fatal("accepted root SSH")
	}
}

// The screen says nothing changed only when that is certain: before the
// write, or when vpn's file was put back and sshd was not restarted. After a
// failed write, a failed put back, a failed restart or a restart whose answer
// does not match, sshd may use the new setting.
func TestSSHSwitchSaysNothingChangedOnlyWhenCertain(t *testing.T) {
	valid := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEB\n"
	for _, tc := range []struct {
		name      string
		keys      string
		applyErr  error
		observed  bool
		readErr   error
		unchanged bool
	}{
		{name: "no key", keys: "", unchanged: true},
		{name: "no SSH folder yet", keys: "missing", unchanged: true},
		{name: "key file cannot be read", keys: "unreadable", unchanged: true},
		{name: "refused and put back", keys: valid, applyErr: errors.New("sshd did not confirm vpn's SSH settings"), unchanged: true},
		{name: "may be written", keys: valid, applyErr: notConfirmed{errors.New("restart sshd: failed")}},
		{name: "restarted, answer does not match", keys: valid, observed: true},
		{name: "restarted, answer cannot be read", keys: valid, readErr: errors.New("sshd -T failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := sshkeys.Store{Path: filepath.Join(t.TempDir(), "authorized_keys")}
			switch tc.keys {
			case "missing":
				// A fresh install has no ~/.ssh until the owner adds a key.
				store.Path = filepath.Join(t.TempDir(), ".ssh", "authorized_keys")
			case "unreadable":
				// A folder where the key file should be cannot be read as one.
				if err := os.Mkdir(store.Path, 0700); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.WriteFile(store.Path, []byte(tc.keys), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := rebuildSSHConfig(true, store, func(string) error { return tc.applyErr }, func() (bool, error) { return tc.observed, tc.readErr })
			if err == nil {
				t.Fatal("reported success")
			}
			if errors.Is(err, ErrNothingChanged) != tc.unchanged {
				t.Fatalf("nothing changed %v, want %v: %v", !tc.unchanged, tc.unchanged, err)
			}
		})
	}
	// Another change holds the key lock, so this one never ran.
	store := sshkeys.Store{Path: filepath.Join(t.TempDir(), "authorized_keys")}
	if err := store.WithLock(func() error {
		err := rebuildSSHConfig(true, store, func(string) error { return nil }, func() (bool, error) { return false, nil })
		if !errors.Is(err, ErrNothingChanged) {
			t.Errorf("a busy lock reads as %v, want nothing changed", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
