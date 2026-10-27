package host

import (
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"

	"github.com/virtpriv/node/internal/bitcoin"
	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/system"
)

// SetupNodeCLI adds the owner's bitcoin-cli and lncli shortcuts to their
// .bashrc. bitcoin-cli reads its staged RPC password on stdin, and lncli
// uses the staged certificate and macaroon, so neither needs the daemons'
// private files.
func SetupNodeCLI(cfg *config.AppConfig) error {
	return setupNodeCLI(paths.AdminBashrc, paths.AdminUser, paths.AdminUser, cfg)
}

// setupNodeCLI keeps what the file already holds and adds each shortcut it
// lacks. The file is written whole by the safe writer, so it is the owner's
// even when the account came without one.
func setupNodeCLI(path, owner, group string, cfg *config.AppConfig) error {
	existing, err := readOwnerFile(path)
	if err != nil {
		return err
	}
	net, err := cfg.NetworkConfig()
	if err != nil {
		return err
	}

	var content string
	if !strings.Contains(existing, "bitcoin-cli()") {
		btcNetFlag := bitcoinCLINetworkFlag(net)
		content += fmt.Sprintf(`
# Virtual Private Node shortcuts.
# bitcoin-cli reads its RPC password on standard input, so a command
# that reads standard input itself needs /usr/local/bin/bitcoin-cli.
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
	if !strings.Contains(existing, "lncli()") {
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
	return system.WriteFileRootOwned(path, []byte(existing+content), 0o644, owner, group)
}

// readOwnerFile reads a file in the owner's home as root without following
// a link. The owner could otherwise point it at a root-only file and get
// that file's contents copied into one of their own. A missing file reads as
// empty, and anything but a regular file is refused.
func readOwnerFile(path string) (string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read %s, it must be a regular file: %w", path, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("read %s: it is not a regular file", path)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return string(data), nil
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
