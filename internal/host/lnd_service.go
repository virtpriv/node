package host

import (
	"fmt"

	"github.com/virtpriv/node/internal/paths"
)

// LNDServiceUnit renders the LND systemd unit. withUnlock adds
// LND's --wallet-unlock-password-file flag, pointing at the
// root-staged password file, so the wallet unlocks without an
// operator on every service start. Everything else about the
// two variants is identical by construction; they come from
// this one template. Pinned LND natively notifies systemd once
// locked-wallet RPC is available or an auto-unlocked wallet has
// reached RPC_ACTIVE, before chain synchronization. The extended
// normal-start timeout follows LND's upstream unit guidance for
// occasional database work; auto-unlock verification temporarily
// overrides it with the product's bounded one-attempt window.
func LNDServiceUnit(username string, withUnlock bool) string {
	unlockFlag := ""
	if withUnlock {
		unlockFlag = " --wallet-unlock-password-file=" +
			paths.LNDWalletPassword
	}
	return fmt.Sprintf(`[Unit]
Description=LND Lightning Network Daemon
After=bitcoind.service tor.service
Wants=bitcoind.service

[Service]
Type=notify
User=%s
Group=%s
SupplementaryGroups=debian-tor
UMask=0077
ExecStart=/usr/local/bin/lnd --configfile=/etc/lnd/lnd.conf%s
Restart=on-failure
RestartSec=30
TimeoutStartSec=1200
TimeoutStopSec=300
PrivateTmp=true
ProtectSystem=full
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
`, username, username, unlockFlag)
}
