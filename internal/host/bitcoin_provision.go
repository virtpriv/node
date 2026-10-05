package host

import (
	"fmt"
	"os"
	"time"

	"github.com/virtpriv/node/internal/bitcoin"
	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/system"
)

func createBitcoinDirs(username string) error {
	dirs := []struct {
		path  string
		owner string
		mode  os.FileMode
	}{
		{paths.BitcoinDir, "root:" + username, 0750},
		{paths.BitcoinDataDir, username + ":" + username, 0750},
	}
	for _, d := range dirs {
		if err := system.RunRoot("mkdir", "-p", d.path); err != nil {
			return fmt.Errorf("mkdir %s: %w", d.path, err)
		}
		if err := system.RunRoot("chown", d.owner, d.path); err != nil {
			return err
		}
		if err := system.RunRoot("chmod",
			fmt.Sprintf("%o", d.mode), d.path); err != nil {
			return fmt.Errorf("chmod %s: %w", d.path, err)
		}
	}
	return nil
}

func bitcoindServiceUnit(username string) string {
	return fmt.Sprintf(`[Unit]
Description=Bitcoin Core
After=network-online.target tor.service
Wants=network-online.target

[Service]
Type=simple
User=%s
Group=%s
SupplementaryGroups=debian-tor
UMask=0077
ExecStart=/usr/local/bin/bitcoind -conf=/etc/bitcoin/bitcoin.conf -datadir=/var/lib/bitcoin
Restart=on-failure
RestartSec=30
TimeoutStopSec=600
PrivateTmp=true
ProtectSystem=full
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
`, username, username)
}

// WriteBitcoindService installs the unit for the dedicated Bitcoin identity.
func WriteBitcoindService() error {
	return system.WriteFileRoot(paths.BitcoindService,
		[]byte(bitcoindServiceUnit(bitcoinUser)), 0644)
}

// EnableAndRestartBitcoind enables Core, restarts it and verifies its chain
// identity. Restart applies the unit and configuration written by this lifecycle
// even when Core is already running during an interrupted-install resume.
func EnableAndRestartBitcoind(cfg *config.AppConfig) error {
	if err := system.RunRoot("systemctl", "daemon-reload"); err != nil {
		return err
	}
	if err := system.RunRoot("systemctl", "enable", "bitcoind"); err != nil {
		return err
	}
	if err := system.RunRoot("systemctl", "restart", "bitcoind"); err != nil {
		return err
	}
	profile, err := cfg.NetworkConfig()
	if err != nil {
		return err
	}
	return waitForBitcoinIdentity(
		profile, bitcoin.GetBlockchainIdentity, time.Sleep, 60, 2*time.Second)
}

func validateBitcoinIdentity(
	profile *config.NetworkConfig, identity bitcoin.BlockchainIdentity,
) error {
	if identity.Chain != profile.CoreNetwork {
		return fmt.Errorf("Bitcoin Core reports chain %q, want %q for profile %q",
			identity.Chain, profile.CoreNetwork, profile.Name)
	}
	if identity.Genesis != profile.ExpectedGenesis {
		return fmt.Errorf("Bitcoin Core genesis is %q, want %q for profile %q",
			identity.Genesis, profile.ExpectedGenesis, profile.Name)
	}
	if identity.SignetChallenge != profile.ExpectedSignetChallenge {
		return fmt.Errorf(
			"Bitcoin Core signet challenge is %q, want %q for profile %q",
			identity.SignetChallenge, profile.ExpectedSignetChallenge,
			profile.Name)
	}
	return nil
}

func waitForBitcoinIdentity(
	profile *config.NetworkConfig,
	probe func(int) (bitcoin.BlockchainIdentity, error),
	sleep func(time.Duration), attempts int, interval time.Duration,
) error {
	if attempts < 1 {
		return fmt.Errorf("Bitcoin Core identity verification has no attempts")
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		identity, err := probe(profile.RPCPort)
		if err == nil {
			if err := validateBitcoinIdentity(profile, identity); err != nil {
				return err
			}
			return nil
		}
		lastErr = err
		if i+1 < attempts {
			sleep(interval)
		}
	}
	return fmt.Errorf(
		"Bitcoin Core did not expose verifiable %s identity on RPC port %d: %w",
		profile.Name, profile.RPCPort, lastErr)
}
