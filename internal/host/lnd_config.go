package host

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
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

// BuildLNDConfig generates lnd.conf content from a valid node
// configuration. Pure logic with no side effects. Hybrid uses the
// address saved in the configuration, so the file, the certificate
// and config.json hold the same one. LND binds by the literal
// loopback addresses defined once in paths, the same constants
// every client dials, so the two ends of each connection cannot
// disagree, and the name localhost appears nowhere in the file (on
// this node, which disables IPv6, that name can resolve to an
// unusable IPv6 address).
func BuildLNDConfig(
	cfg *config.AppConfig, restOnion,
	bitcoindRPCUser, bitcoindRPCPass string,
) (string, error) {
	if err := cfg.Validate(); err != nil {
		return "", err
	}
	net, err := cfg.NetworkConfig()
	if err != nil {
		return "", err
	}

	listenLine := "listen=" + paths.LNDP2PBind
	restListenLine := "restlisten=" + paths.LNDRESTEndpoint
	externalLine := ""
	tlsExtraIP := ""
	if cfg.P2PMode == "hybrid" {
		listenLine = "listen=0.0.0.0:9735"
		restListenLine = "restlisten=0.0.0.0:8080"
		externalLine = fmt.Sprintf("externalhosts=%s:9735", cfg.P2PAddress)
		tlsExtraIP = fmt.Sprintf("tlsextraip=%s", cfg.P2PAddress)
	}

	tlsExtraDomain := ""
	if restOnion != "" {
		tlsExtraDomain = fmt.Sprintf("tlsextradomain=%s", restOnion)
	}

	return fmt.Sprintf(`# Virtual Private Node, LND configuration
# vpn writes this file from its own settings. An update can
# write it again, so changes made by hand are not kept.
[Application Options]
lnddir=/var/lib/lnd
%s
rpclisten=%s
%s
debuglevel=info
%s
%s
%s

# LND manages its own TLS certificate. At startup it
# replaces an expired certificate, and with tlsautorefresh
# also one whose names or addresses set here changed, for
# example when Hybrid adds tlsextraip. It does not renew a
# certificate before it expires. With tlsdisableautofill
# the certificate holds only the names and addresses set
# here, not the machine's own addresses.
tlsautorefresh=1
tlsdisableautofill=1

# Accept keysend (spontaneous) and AMP (multi-path)
# payments. Many Lightning apps depend on keysend.
accept-keysend=true
accept-amp=true

# Auto-delete canceled invoices to prevent database
# bloat on long-running nodes.
gc-canceled-invoices-on-the-fly=true

# Do not forward a payment that arrives and leaves on the
# same channel, LND's default. The node's own payments,
# rebalancing included, are not affected.
allow-circular-route=false

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
# Keep the key of the node's onion address where LND
# already keeps it. It stays unencrypted in LND's private
# folder (lnd:lnd 0750, the key 0600). Encrypting it with
# the wallet adds little protection and ties the address
# to the wallet for recovery.
tor.privatekeypath=/var/lib/lnd/v3_onion_private_key
tor.encryptkey=false
# Never bypass Tor, also for a clearnet address. Written
# here so it does not depend on LND's default.
tor.skip-proxy-for-clearnet-targets=false

[protocol]
# Taproot channels give smaller, cheaper cooperative
# closes with better privacy on chain (MuSig2 key spend).
protocol.simple-taproot-chans=true
# Keep onion-message forwarding available in the background.
# This is distinct from Tor onion-service routing.
protocol.no-onion-messages=false
# LND requires RBF cooperative close support whenever final
# Taproot channels are enabled. Keep that dependency explicit.
protocol.rbf-coop-close=true
# Accept channels larger than 0.16 BTC.
protocol.wumbo-channels=true
# A private channel can be named by an alias instead of
# its funding transaction, so invoices do not reveal it.
protocol.option-scid-alias=true

[db]
# LND keeps its data in SQLite with its native SQL stores.
# This cannot change for the life of the node. SQLite's
# own settings stay at LND's defaults.
db.backend=sqlite
db.use-native-sql=true

[healthcheck]
# LND's check of Bitcoin Core keeps LND's defaults. After
# three failed attempts, roughly 4 to 6.5 minutes, LND stops
# itself with exit status 0, which systemd does not restart
# (LND issue 5625). Start LND again from the System tab.
# The TLS check stays off. It only notices an expired
# certificate and stops LND the same way. LND replaces the
# certificate at startup.
healthcheck.tls.attempts=0
# After Tor restarts, LND reconnects to it and brings back
# the same onion address. Ten attempts at LND's usual pace
# allow roughly 9 to 11 minutes.
healthcheck.torconnection.interval=1m
healthcheck.torconnection.timeout=5s
healthcheck.torconnection.backoff=1m
healthcheck.torconnection.attempts=10
# LND stops itself when free disk space falls to 10%%.
# On a 90 GB disk that keeps about 9 GB free.
healthcheck.diskspace.diskrequired=0.10
healthcheck.diskspace.attempts=2
healthcheck.diskspace.interval=12h
`, listenLine, paths.LNDGRPCEndpoint, restListenLine, externalLine,
		tlsExtraDomain, tlsExtraIP,
		net.LNDBitcoinFlag, bitcoindRPCUser, bitcoindRPCPass,
		net.RPCPort, net.ZMQBlockPort, net.ZMQTxPort), nil
}

func WriteLNDConfig(cfg *config.AppConfig) error {
	password, err := readLNDBitcoindRPCPassword()
	if err != nil {
		return err
	}
	return WriteLNDConfigWithRPCPassword(cfg, password)
}

func WriteLNDConfigWithRPCPassword(cfg *config.AppConfig, password string) error {
	if password == "" {
		return fmt.Errorf("refusing to write lnd.conf with an empty " +
			"bitcoind RPC password")
	}
	restOnion, err := readRequiredLNDRESTOnion()
	if err != nil {
		return err
	}
	content, err := BuildLNDConfig(cfg, restOnion,
		LNDBitcoindRPCUser, password)
	if err != nil {
		return err
	}
	return system.WriteFileRootOwned(paths.LNDConf, []byte(content), 0640, "root", lndUser)
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

// rewriteLNDConfig writes lnd.conf again from this release's template and the
// node's settings, for the update step lnd-config-v1. LND must be stopped.
// Running it again gives the same file, so a Retry is safe.
func rewriteLNDConfig(cfg *config.AppConfig) error {
	content, err := readLNDConfigRewrite(cfg)
	if err != nil {
		return err
	}
	return system.WriteFileRootOwned(paths.LNDConf, []byte(content), 0640, "root", lndUser)
}

// readLNDConfigRewrite returns the new lnd.conf, or why this node's current
// file cannot be rewritten. It changes nothing.
func readLNDConfigRewrite(cfg *config.AppConfig) (string, error) {
	current, err := os.ReadFile(paths.LNDConf)
	if err != nil {
		return "", fmt.Errorf("read lnd.conf: %w", err)
	}
	restOnion, err := readRequiredLNDRESTOnion()
	if err != nil {
		return "", err
	}
	return rewrittenLNDConfig(string(current), cfg, restOnion)
}

// rewrittenLNDConfig returns the new lnd.conf for a node whose current file is
// current. The network and Hybrid address come from the node configuration,
// the onion address from Tor and LND's Core password from the current file.
// A current file that disagrees with any of them is refused, because the
// update would otherwise change how the node is reached without the owner
// asking. Other lines, hand edits among them, are not carried over.
func rewrittenLNDConfig(current string, cfg *config.AppConfig, restOnion string) (string, error) {
	password, err := parseLNDBitcoindRPCPassword(current)
	if err != nil {
		return "", err
	}
	if err := lndConfigAgrees(current, cfg, restOnion); err != nil {
		return "", fmt.Errorf("lnd.conf does not match the node configuration: %w", err)
	}
	return BuildLNDConfig(cfg, restOnion, LNDBitcoindRPCUser, password)
}

// lndConfigAgrees compares the lines that carry the node's settings.
func lndConfigAgrees(current string, cfg *config.AppConfig, restOnion string) error {
	node, err := cfg.NetworkConfig()
	if err != nil {
		return err
	}
	values := map[string][]string{}
	for _, line := range strings.Split(current, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "[") {
			continue
		}
		if key, value, ok := strings.Cut(line, "="); ok {
			key = strings.TrimSpace(key)
			values[key] = append(values[key], strings.TrimSpace(value))
		}
	}
	is := func(key, want string) bool {
		return len(values[key]) == 1 && values[key][0] == want
	}
	// The node's own network is selected once, every other one not at all.
	selected, _, _ := strings.Cut(node.LNDBitcoinFlag, "=")
	for _, name := range config.SupportedNetworks() {
		profile, err := config.NetworkConfigFromName(name)
		if err != nil {
			return err
		}
		key, _, _ := strings.Cut(profile.LNDBitcoinFlag, "=")
		if (key == selected && !is(key, "true")) || (key != selected && len(values[key]) > 0) {
			return fmt.Errorf("it does not select the %s network", cfg.Network)
		}
	}
	if cfg.P2PMode == "hybrid" {
		if !is("externalhosts", cfg.P2PAddress+":9735") || !is("tlsextraip", cfg.P2PAddress) ||
			!is("listen", "0.0.0.0:9735") || !is("restlisten", "0.0.0.0:8080") {
			return fmt.Errorf("it does not announce the Hybrid address %s", cfg.P2PAddress)
		}
	} else if len(values["externalhosts"]) > 0 || len(values["tlsextraip"]) > 0 ||
		!is("listen", paths.LNDP2PBind) || !is("restlisten", paths.LNDRESTEndpoint) {
		return errors.New("it has Hybrid lines but the node uses Tor only")
	}
	if !is("tlsextradomain", restOnion) {
		return errors.New("its onion address differs from Tor's")
	}
	return nil
}
