package host

import (
	"fmt"
	"os"
	"strings"

	"github.com/virtpriv/node/internal/bitcoin"
	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/paths"
)

// SetupNodeCLI installs the operator's unprivileged recovery commands.
// bitcoin-cli reads its staged RPC password on stdin; lncli uses the staged
// certificate and macaroon. Neither wrapper needs the daemon's private files.
func SetupNodeCLI(cfg *config.AppConfig) error {
	bashrc := paths.AdminBashrc
	data, _ := os.ReadFile(bashrc)
	existing := string(data)
	net, err := cfg.NetworkConfig()
	if err != nil {
		return err
	}

	var content string

	// bitcoin-cli wrapper
	if !strings.Contains(existing, "bitcoin-cli()") {
		btcNetFlag := bitcoinCLINetworkFlag(net)
		content += fmt.Sprintf(`
# -- Virtual Private Node --
# RPC password comes from the staged credential file on stdin;
# commands that themselves read stdin should be run with
# explicit flags instead of this wrapper.
bitcoin-cli() {
    /usr/local/bin/bitcoin-cli \
        -rpcconnect=127.0.0.1 \
        -rpcport=%d \
        -rpcuser=%s \
        -stdinrpcpass \%s
        "$@" < %s
}
export -f bitcoin-cli
`, net.RPCPort, bitcoin.RPCUser, btcNetFlag,
			paths.StateBitcoindRPCPass)
	}

	// lncli wrapper; always set up now that LND is part of
	// the initial install
	if cfg.HasLND() &&
		!strings.Contains(existing, "lncli()") {
		lndNetFlag := lncliNetworkFlag(net)
		content += fmt.Sprintf(`
lncli() {
    /usr/local/bin/lncli \
        --rpcserver=%s \%s
        --macaroonpath=%s \
        --tlscertpath=%s \
        "$@"
}
export -f lncli
`, paths.LNDGRPCEndpoint, lndNetFlag,
			paths.StateLNDMacaroon, paths.StateLNDTLSCert)
	}

	if content == "" {
		return nil
	}

	f, err := os.OpenFile(bashrc,
		os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(content)
	return err
}

func bitcoinCLINetworkFlag(net *config.NetworkConfig) string {
	if net.BitcoinCLIFlag == "" {
		return ""
	}
	return "\n        " + net.BitcoinCLIFlag + " \\"
}

func lncliNetworkFlag(net *config.NetworkConfig) string {
	if net.Name == config.NetworkMainnet {
		return ""
	}
	return fmt.Sprintf("\n        --network=%s \\", net.LNDNetwork)
}
