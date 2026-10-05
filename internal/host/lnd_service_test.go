package host

import (
	"strings"
	"testing"

	"github.com/virtpriv/node/internal/paths"
)

// The LND unit template: one source for both variants, so they
// can never drift apart in anything but the unlock flag.
func TestLNDServiceUnit(t *testing.T) {
	plain := LNDServiceUnit("lnd", false)
	unlock := LNDServiceUnit("lnd", true)

	unlockFlag := "--wallet-unlock-password-file=" +
		paths.LNDWalletPassword
	if strings.Contains(plain, unlockFlag) {
		t.Error("plain unit carries the unlock flag")
	}
	if got := strings.Count(unlock, unlockFlag); got != 1 {
		t.Errorf("unlock unit has %d unlock flags, want 1", got)
	}

	// The only difference between the variants is the flag.
	if strings.Replace(unlock, " "+unlockFlag, "", 1) != plain {
		t.Error("variants differ beyond the unlock flag")
	}

	for _, unit := range []string{plain, unlock} {
		for _, want := range []string{
			"After=bitcoind.service tor.service",
			"Wants=bitcoind.service",
			"Type=notify",
			"User=lnd",
			"Group=lnd",
			"SupplementaryGroups=debian-tor",
			"UMask=0077",
			"ExecStart=/usr/local/bin/lnd " +
				"--configfile=/etc/lnd/lnd.conf",
			"Restart=on-failure",
			"TimeoutStartSec=1200",
			"TimeoutStopSec=300",
			"WantedBy=multi-user.target",
		} {
			if !strings.Contains(unit, want) {
				t.Errorf("unit lacks %q", want)
			}
		}
		if got := strings.Count(unit, "UMask=0077"); got != 1 {
			t.Errorf("unit has %d private umasks, want 1", got)
		}
		if strings.Contains(unit, "vpn-lnd-backup") {
			t.Error("normal lnd unit has channel-backup export access")
		}
		for _, forbidden := range []string{
			"Wants=tor.service", "Restart=always", "Restart=on-success",
		} {
			if strings.Contains(unit, forbidden) {
				t.Errorf("unit unexpectedly contains %q", forbidden)
			}
		}
	}

	// The unlock flag rides the ExecStart line, not a line of
	// its own; systemd would ignore a bare flag line.
	for _, line := range strings.Split(unlock, "\n") {
		if strings.Contains(line, unlockFlag) &&
			!strings.HasPrefix(line, "ExecStart=") {
			t.Errorf("unlock flag off the ExecStart line: %q", line)
		}
	}
}
