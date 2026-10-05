package host

import (
	"fmt"
	"strings"

	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/system"
)

// buildBitcoinConfig renders the selected immutable network profile. RPC
// authentication belongs in the global section, before any network section.
func buildBitcoinConfig(
	cfg *config.AppConfig, rpcauthLines ...string,
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
	b.WriteString("# Virtual Private Node — Bitcoin Core\n")
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
	b.WriteString("listenonion=1\n")
	b.WriteString(auth)
	if net.CoreNetwork != "main" {
		fmt.Fprintf(&b, "\n[%s]\n", net.CoreNetwork)
	}
	b.WriteString("bind=127.0.0.1\n")
	b.WriteString("rpcbind=127.0.0.1\n")
	fmt.Fprintf(&b, "rpcport=%d\n", net.RPCPort)
	b.WriteString("rpcallowip=127.0.0.1\n")
	fmt.Fprintf(&b, "zmqpubrawblock=tcp://127.0.0.1:%d\n", net.ZMQBlockPort)
	fmt.Fprintf(&b, "zmqpubrawtx=tcp://127.0.0.1:%d\n", net.ZMQTxPort)
	return b.String(), nil
}

// WriteInitialNodeRPCConfig rotates both RPC credentials and writes both daemon
// configurations. Only the operator's password is staged; LND's password stays
// in root:lnd lnd.conf. Writes are sequential, not a transaction. The installer
// reruns the whole incomplete btc group after failure, before starting Core.
// This operation is for initial provisioning, not a completed-node rewrite.
func WriteInitialNodeRPCConfig(cfg *config.AppConfig) error {
	creds, err := writeRPCAuthCredentials()
	if err != nil {
		return err
	}
	content, err := buildBitcoinConfig(cfg, creds.lines...)
	if err != nil {
		return err
	}
	if err := system.WriteFileRoot(paths.BitcoinConf, []byte(content), 0640); err != nil {
		return err
	}
	if err := system.RunRoot(
		"chown", "root:"+bitcoinUser, paths.BitcoinConf); err != nil {
		return err
	}
	return WriteLNDConfigWithRPCPassword(cfg, "", creds.lndPassword)
}
