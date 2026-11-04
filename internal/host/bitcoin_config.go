package host

import (
	"fmt"
	"strings"

	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/system"
)

// bitcoinOnionBindPort is where Core listens for peers that come through its
// onion: the P2P port plus one, Core's own default for its onion bind. Tor
// forwards the onion's P2P port to it.
func bitcoinOnionBindPort(net *config.NetworkConfig) int {
	return net.P2PPort + 1
}

// buildBitcoinConfig renders the selected immutable network profile. RPC
// authentication belongs in the global section, before any network section.
// onion is the address Tor made for Core.
func buildBitcoinConfig(
	cfg *config.AppConfig, onion string, rpcauthLines ...string,
) (string, error) {
	net, err := cfg.NetworkConfig()
	if err != nil {
		return "", err
	}
	pruneMB := cfg.PruneSize * 1000

	var auth string
	for _, line := range rpcauthLines {
		if line != "" {
			auth += line + "\n"
		}
	}

	var b strings.Builder
	b.WriteString("# Virtual Private Node, Bitcoin Core configuration\n")
	b.WriteString("server=1\n")
	b.WriteString("disablewallet=1\n")
	// VPN has two explicit rpcauth identities. Keeping Core's independent
	// session cookie would add a third credential that no supported client
	// consumes.
	b.WriteString("norpccookiefile=1\n")
	if net.BitcoinFlag != "" {
		b.WriteString(net.BitcoinFlag)
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "prune=%d\n", pruneMB)
	fmt.Fprintf(&b, "dbcache=%d\n", cfg.DbCacheMB())
	b.WriteString("maxmempool=300\n")
	b.WriteString("proxy=127.0.0.1:9050\n")
	b.WriteString("listen=1\n")
	// Tor makes Core's onion from vpn's torrc, so Core does not make one
	// through Tor's control port.
	b.WriteString("listenonion=0\n")
	b.WriteString(auth)
	if net.CoreNetwork != "main" {
		fmt.Fprintf(&b, "\n[%s]\n", net.CoreNetwork)
	}
	// Core listens only where Tor forwards its onion. Core reads bind only
	// from the network's own section. The onion tag makes Core treat these
	// peers as Tor peers, and externalip is the address it tells them.
	fmt.Fprintf(&b, "bind=127.0.0.1:%d=onion\n", bitcoinOnionBindPort(net))
	fmt.Fprintf(&b, "externalip=%s\n", onion)
	b.WriteString("rpcbind=127.0.0.1\n")
	fmt.Fprintf(&b, "rpcport=%d\n", net.RPCPort)
	b.WriteString("rpcallowip=127.0.0.1\n")
	fmt.Fprintf(&b, "zmqpubrawblock=tcp://127.0.0.1:%d\n", net.ZMQBlockPort)
	fmt.Fprintf(&b, "zmqpubrawtx=tcp://127.0.0.1:%d\n", net.ZMQTxPort)
	return b.String(), nil
}

// WriteInitialNodeRPCConfig rotates both RPC credentials and writes both daemon
// configurations. Only the operator's password is staged. LND's password stays
// in lnd.conf, owned by root and the lnd group. Writes are sequential, not a transaction. The installer
// reruns the whole incomplete btc group after failure, before starting Core.
// This operation is for initial provisioning, not a completed-node rewrite.
func WriteInitialNodeRPCConfig(cfg *config.AppConfig) error {
	// Read before any credential is made, so a missing address changes
	// nothing.
	onion, err := readRequiredOnion(paths.TorBitcoinP2PHostname, "Bitcoin Core")
	if err != nil {
		return err
	}
	creds, err := writeRPCAuthCredentials()
	if err != nil {
		return err
	}
	content, err := buildBitcoinConfig(cfg, onion, creds.lines...)
	if err != nil {
		return err
	}
	if err := system.WriteFileRootOwned(
		paths.BitcoinConf, []byte(content), 0640, "root", bitcoinUser); err != nil {
		return err
	}
	return WriteLNDConfigWithRPCPassword(cfg, creds.lndPassword)
}
