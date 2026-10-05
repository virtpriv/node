package host

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/system"
)

// LNDBitcoindRPCUser is LND's independent Bitcoin RPC identity.
const LNDBitcoindRPCUser = "lnd"

// BuildLNDConfig generates lnd.conf content. Pure logic with no
// side effects. LND binds by the literal loopback addresses
// defined once in paths, the same constants every client
// dials, so the two ends of each connection cannot disagree,
// and the name localhost appears nowhere in the file (on this
// node, which disables IPv6, that name can resolve to an
// unusable IPv6 address).
func BuildLNDConfig(
	cfg *config.AppConfig, publicIPv4, restOnion,
	bitcoindRPCUser, bitcoindRPCPass string,
) (string, error) {
	net, err := cfg.NetworkConfig()
	if err != nil {
		return "", err
	}

	listenLine := "listen=" + paths.LNDP2PBind
	restListenLine := "restlisten=" + paths.LNDRESTEndpoint
	externalLine := ""
	tlsExtraIP := ""
	if cfg.P2PMode == "hybrid" && publicIPv4 != "" {
		listenLine = "listen=0.0.0.0:9735"
		restListenLine = "restlisten=0.0.0.0:8080"
		externalLine = fmt.Sprintf("externalhosts=%s:9735", publicIPv4)
		tlsExtraIP = fmt.Sprintf("tlsextraip=%s", publicIPv4)
	}

	tlsExtraDomain := ""
	if restOnion != "" {
		tlsExtraDomain = fmt.Sprintf("tlsextradomain=%s", restOnion)
	}

	return fmt.Sprintf(`# Virtual Private Node. LND
[Application Options]
lnddir=/var/lib/lnd
%s
rpclisten=%s
%s
debuglevel=info
%s
%s
%s

# Let LND own its TLS cert lifecycle. At startup, LND
# replaces an expired cert; tlsautorefresh also replaces
# it when configured SAN inputs change (e.g. tlsextraip is
# added during a P2P upgrade). It does not renew an
# unchanged cert before expiry. tlsdisableautofill keeps
# the cert deterministic; it contains only what we set
# explicitly here, not autodetected interface IPs.
# This is the same pattern used by Raspiblitz.
tlsautorefresh=1
tlsdisableautofill=1

# Accept keysend (spontaneous) and AMP (multi-path)
# payments. Many Lightning apps depend on keysend.
accept-keysend=true
accept-amp=true

# Auto-delete canceled invoices to prevent database
# bloat on long-running nodes.
gc-canceled-invoices-on-the-fly=true

# Allow routing payments that arrive and depart on the
# same channel. Required for circular rebalancing.
allow-circular-route=true

[Bitcoin]
%s
bitcoin.node=bitcoind

[Bitcoind]
bitcoind.rpcuser=%s
bitcoind.rpcpass=%s
bitcoind.rpchost=127.0.0.1:%d
bitcoind.zmqpubrawblock=tcp://127.0.0.1:%d
bitcoind.zmqpubrawtx=tcp://127.0.0.1:%d

[Tor]
tor.active=true
tor.socks=127.0.0.1:9050
tor.control=127.0.0.1:9051
tor.targetipaddress=127.0.0.1
tor.v3=true
tor.streamisolation=true
# Pin the durable dynamic P2P onion identity to LND's
# existing derived path. Keep the key plaintext under the
# lnd:lnd 0750 directory, key mode 0600, and UMask 0077:
# wallet-seed encryption adds little protection on this
# auto-unlocked appliance and couples recovery to the
# matching wallet keyring.
tor.privatekeypath=/var/lib/lnd/v3_onion_private_key
tor.encryptkey=false
# Tor-only policy is explicit rather than inherited from
# an upstream default: never bypass the SOCKS proxy for a
# clearnet target.
tor.skip-proxy-for-clearnet-targets=false

[protocol]
# Taproot channels: smaller, cheaper cooperative closes
# with better on-chain privacy (MuSig2 key spend).
protocol.simple-taproot-chans=true
# Keep onion-message forwarding available in the background.
# This is distinct from Tor onion-service routing.
protocol.no-onion-messages=false
# LND requires RBF cooperative close support whenever final
# Taproot channels are enabled. Keep that dependency explicit;
# VPN's operator-driven fee-bump workflow remains deferred.
protocol.rbf-coop-close=true
# Accept channels larger than 0.16 BTC.
protocol.wumbo-channels=true
# Channels referenced by alias instead of on-chain UTXO
# for better privacy in gossip.
protocol.option-scid-alias=true

[db]
# Fresh v0.7.0 nodes use LND's local SQLite backend and its native SQL
# stores. These settings are permanent for the lifetime of the node.
# Leave SQLite durability, locking, WAL, checkpoint, and vacuum behavior
# at the pinned LND release's defaults.
db.backend=sqlite
db.use-native-sql=true

[healthcheck]
# Keep LND's chain-backend check at its upstream defaults.
# Three attempts can stop LND after roughly 4-6.5 minutes.
# A failed check currently requests graceful exit status 0,
# which Restart=on-failure does not restart. Re-evaluate the
# budget and upstream issue 5625 / PR 10944 on each upgrade.
# Keep TLS checking disabled explicitly. It only detects an
# already-expired cert and requests the same graceful stop;
# startup, not this check, performs certificate replacement.
healthcheck.tls.attempts=0
# Reconnect LND's controller and recreate the same dynamic
# onion after Tor restarts. Ten attempts retain the upstream
# cadence while allowing roughly 9-11 minutes for recovery.
healthcheck.torconnection.interval=1m
healthcheck.torconnection.timeout=5s
healthcheck.torconnection.backoff=1m
healthcheck.torconnection.attempts=10
# Graceful shutdown if filesystem free space falls to 10%%.
# On a 90GB filesystem this preserves roughly 9GB and allows
# about 81GB of operational usage before the safety stop.
healthcheck.diskspace.diskrequired=0.10
healthcheck.diskspace.attempts=2
healthcheck.diskspace.interval=12h
`, listenLine, paths.LNDGRPCEndpoint, restListenLine, externalLine,
		tlsExtraDomain, tlsExtraIP,
		net.LNDBitcoinFlag, bitcoindRPCUser, bitcoindRPCPass,
		net.RPCPort, net.ZMQBlockPort, net.ZMQTxPort), nil
}

func WriteLNDConfig(cfg *config.AppConfig, publicIPv4 string) error {
	password, err := readLNDBitcoindRPCPassword()
	if err != nil {
		return err
	}
	return WriteLNDConfigWithRPCPassword(cfg, publicIPv4, password)
}

func WriteLNDConfigWithRPCPassword(
	cfg *config.AppConfig, publicIPv4, password string,
) error {
	if password == "" {
		return fmt.Errorf("refusing to write lnd.conf with an empty " +
			"bitcoind RPC password")
	}
	restOnion, err := readRequiredLNDRESTOnion()
	if err != nil {
		return err
	}
	content, err := BuildLNDConfig(cfg, publicIPv4, restOnion,
		LNDBitcoindRPCUser, password)
	if err != nil {
		return err
	}
	if err := system.WriteFileRoot(paths.LNDConf, []byte(content), 0640); err != nil {
		return err
	}
	return system.RunRoot("chown", "root:lnd", paths.LNDConf)
}

// readRequiredLNDRESTOnion enforces the dependency between Tor's
// hidden-service identity and LND's TLS configuration. This node
// requests v3 onions, whose host label is 56 lowercase base32
// characters. Missing, unreadable, or malformed state is never
// converted into an lnd.conf without tlsextradomain.
func readRequiredLNDRESTOnion() (string, error) {
	data, err := os.ReadFile(paths.TorLNDRESTHostname)
	if err != nil {
		return "", fmt.Errorf("read LND REST onion hostname: %w", err)
	}
	onion := strings.TrimSpace(string(data))
	if err := validateLNDRESTOnion(onion); err != nil {
		return "", err
	}
	return onion, nil
}

func validateLNDRESTOnion(onion string) error {
	if !ValidV3OnionHostname(onion) {
		return fmt.Errorf("invalid LND REST onion hostname %q", onion)
	}
	return nil
}

// VerifyLNDTLSOnionSAN waits for LND's startup-time TLS
// generation/refresh and proves that the certificate it wrote
// contains the exact REST onion DNS SAN configured above.
func VerifyLNDTLSOnionSAN() error {
	onion, err := readRequiredLNDRESTOnion()
	if err != nil {
		return err
	}
	var lastErr error
	for i := 0; i < 60; i++ {
		certPEM, readErr := os.ReadFile(paths.LNDTLSCert)
		if readErr == nil {
			lastErr = verifyCertificateDNSName(certPEM, onion)
			if lastErr == nil {
				return nil
			}
		} else {
			lastErr = readErr
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf(
		"LND TLS certificate does not contain REST onion %s: %w",
		onion, lastErr)
}

func verifyCertificateDNSName(certPEM []byte, name string) error {
	foundCertificate := false
	for len(certPEM) > 0 {
		var block *pem.Block
		block, certPEM = pem.Decode(certPEM)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		foundCertificate = true
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return fmt.Errorf("parse LND TLS certificate: %w", err)
		}
		for _, dnsName := range cert.DNSNames {
			if dnsName == name {
				return nil
			}
		}
	}
	if foundCertificate {
		return fmt.Errorf("DNS SAN %q is missing", name)
	}
	return fmt.Errorf("no certificate PEM block found")
}

// verifyLNDTLSIPSAN waits for startup-time tlsautorefresh to replace LND's
// certificate after the hybrid-P2P tlsextraip change and proves that the exact
// public IP is present before the helper stages it or publishes the mode.
func verifyLNDTLSIPSAN(publicIPv4 string) error {
	var lastErr error
	for i := 0; i < 60; i++ {
		certPEM, readErr := os.ReadFile(paths.LNDTLSCert)
		if readErr == nil {
			lastErr = verifyCertificateIPAddress(certPEM, publicIPv4)
			if lastErr == nil {
				return nil
			}
		} else {
			lastErr = readErr
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf(
		"LND TLS certificate does not contain IP SAN %s: %w",
		publicIPv4, lastErr)
}

func verifyCertificateIPAddress(certPEM []byte, address string) error {
	want := net.ParseIP(address)
	if want == nil {
		return fmt.Errorf("invalid IP address %q", address)
	}
	foundCertificate := false
	for len(certPEM) > 0 {
		var block *pem.Block
		block, certPEM = pem.Decode(certPEM)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		foundCertificate = true
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return fmt.Errorf("parse LND TLS certificate: %w", err)
		}
		for _, got := range cert.IPAddresses {
			if got.Equal(want) {
				return nil
			}
		}
	}
	if foundCertificate {
		return fmt.Errorf("IP SAN %q is missing", address)
	}
	return fmt.Errorf("no certificate PEM block found")
}

// readLNDBitcoindRPCPassword preserves LND's independent RPC
// credential when a later operation rewrites lnd.conf (for
// example a P2P-mode change). Duplicate or empty entries fail
// closed rather than choosing an ambiguous credential.
func readLNDBitcoindRPCPassword() (string, error) {
	data, err := os.ReadFile(paths.LNDConf)
	if err != nil {
		return "", fmt.Errorf("read LND bitcoind RPC credential: %w", err)
	}
	return parseLNDBitcoindRPCPassword(string(data))
}

func parseLNDBitcoindRPCPassword(content string) (string, error) {
	var password string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "bitcoind.rpcpass=") {
			continue
		}
		value := strings.TrimPrefix(line, "bitcoind.rpcpass=")
		if value == "" || password != "" {
			return "", fmt.Errorf(
				"lnd.conf has an empty or duplicate bitcoind.rpcpass")
		}
		password = value
	}
	if password == "" {
		return "", fmt.Errorf("lnd.conf has no bitcoind.rpcpass")
	}
	return password, nil
}
