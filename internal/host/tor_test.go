package host

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// vpn uses Tor as Debian sets it up, so the install starts only on a torrc
// that sets nothing. A setting in any form refuses, and the first one's line
// is named, never its text.
func TestTorSettingsVerdict(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		line          int // 0 means the file passes
	}{
		{"empty file", "", 0},
		{"comments and empty lines", "## Tor's sample\n\n#SocksPort 9050\n  # indented\n\t\n", 0},
		{"comments with Windows line ends", "# a\r\n\r\n", 0},
		{"a setting after comments", "# a\nSocksPort 9050\n", 2},
		{"an indented setting", "# a\n\n  HiddenServiceDir /var/lib/tor/x/\n", 3},
		{"an include line", "%include /etc/tor/torrc.d/*.conf\n", 1},
		{"a last line without a line end", "# a\n# b\n# c\nExitRelay 1", 4},
		{"two settings", "# a\nUseBridges 1\n# b\nBridge 192.0.2.1:443\n", 2},
	} {
		err := torSettingsVerdict(tc.content)
		if tc.line == 0 {
			if err != nil {
				t.Errorf("%s: refused: %v", tc.name, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: passed, want a refusal", tc.name)
			continue
		}
		if want := fmt.Sprintf("line %d.", tc.line); !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %q does not name %q", tc.name, err, want)
		}
		for _, line := range strings.Split(tc.content, "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") && strings.Contains(err.Error(), line) {
				t.Errorf("%s: the refusal shows the setting's text: %q", tc.name, err)
			}
		}
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
