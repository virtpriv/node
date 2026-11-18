package host

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// Tor serves outgoing traffic and the installer's Tor check. vpn's torrc
// opens the SOCKS port and the control port and no onion service.
func TestTorConfigOpensTwoPortsAndNoOnionService(t *testing.T) {
	content := BuildTorConfig()
	for _, line := range []string{"SOCKSPort 9050", "ControlPort 9051", "CookieAuthentication 1"} {
		if !strings.Contains(content, "\n"+line+"\n") {
			t.Errorf("torrc lacks the line %q", line)
		}
	}
	if strings.Contains(content, "HiddenService") {
		t.Errorf("torrc sets up an onion service:\n%s", content)
	}
}

// The install sets Debian's torrc aside before writing vpn's. If it wrote
// first and the diversion then failed, a resumed install's diversion would
// move vpn's file aside as if it were Debian's, and Debian's copy would be
// gone.
func TestInstallTorConfigIsNotWrittenWhenDiversionFails(t *testing.T) {
	oldDivert, oldWrite := divertDebianTorConfig, writeTorConfigForInstall
	t.Cleanup(func() {
		divertDebianTorConfig, writeTorConfigForInstall = oldDivert, oldWrite
	})
	diverted := false
	divertDebianTorConfig = func() error {
		diverted = true
		return errors.New("dpkg-divert: error: rename involves overwriting")
	}
	writes := 0
	writeTorConfigForInstall = func([]byte) error {
		writes++
		return nil
	}
	if err := WriteTorConfig(); err == nil {
		t.Fatal("install went on although setting Debian's torrc aside failed")
	}
	if !diverted || writes != 0 {
		t.Fatalf("diversion tried=%t, torrc writes=%d, want true and 0", diverted, writes)
	}
}

// systemd must have loaded the retry rule before Tor is restarted, so the rule
// applies from the first crash. A rule that cannot be written or loaded fails
// the step before Tor is touched, so a resume runs it again.
func TestInitialTorOperationLoadsRetryRuleBeforeRestart(t *testing.T) {
	oldWrite, oldReload, oldAction := writeTorRestartRule, reloadSystemdForTor, runTorServiceAction
	t.Cleanup(func() {
		writeTorRestartRule, reloadSystemdForTor, runTorServiceAction = oldWrite, oldReload, oldAction
	})
	for _, tc := range []struct {
		name      string
		writeErr  error
		reloadErr error
		wantSteps []string
	}{
		{"rule written", nil, nil, []string{"write retry rule", "daemon-reload", "enable", "restart"}},
		{"rule not written", errors.New("disk full"), nil, []string{"write retry rule"}},
		{"rule not loaded", nil, errors.New("reload failed"), []string{"write retry rule", "daemon-reload"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var steps []string
			writeTorRestartRule = func() error {
				steps = append(steps, "write retry rule")
				return tc.writeErr
			}
			reloadSystemdForTor = func() error {
				steps = append(steps, "daemon-reload")
				return tc.reloadErr
			}
			runTorServiceAction = func(action string) error {
				steps = append(steps, action)
				return nil
			}
			err := EnableAndRestartTor()
			wantErr := tc.writeErr != nil || tc.reloadErr != nil
			if (err != nil) != wantErr {
				t.Fatalf("err=%v, want error %t", err, wantErr)
			}
			if !reflect.DeepEqual(steps, tc.wantSteps) {
				t.Fatalf("steps=%v want=%v", steps, tc.wantSteps)
			}
		})
	}
}
