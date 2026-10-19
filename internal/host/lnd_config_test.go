package host

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/paths"
)

func mustBuildLNDConfig(
	t *testing.T, cfg *config.AppConfig, restOnion, user, password string,
) string {
	t.Helper()
	content, err := BuildLNDConfig(cfg, restOnion, user, password)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func activeLNDConfigValues(content string) map[string][]string {
	values := make(map[string][]string)
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") ||
			strings.HasPrefix(line, "[") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		values[key] = append(values[key], strings.TrimSpace(value))
	}
	return values
}

// The generated lnd.conf binds by the literal loopback
// addresses defined once in paths, the same constants every
// client dials, and carries no host name anywhere. On this
// node, which disables IPv6, the name localhost can resolve to
// an unusable IPv6 address, so it must never appear.
func TestBuildLNDConfigBindsByAddress(t *testing.T) {
	// Tor-only, every listener on the loopback constants.
	cfg := config.Default()
	torOnly := mustBuildLNDConfig(t, cfg, "", "lnd", "secret")
	for _, want := range []string{
		"listen=" + paths.LNDP2PBind,
		"restlisten=" + paths.LNDRESTEndpoint,
		"rpclisten=" + paths.LNDGRPCEndpoint,
	} {
		if !strings.Contains(torOnly, want) {
			t.Errorf("tor-only config missing %q", want)
		}
	}

	// Hybrid, P2P and REST bind all interfaces with the saved
	// address. gRPC stays on the loopback constant.
	hybrid := config.Default()
	hybrid.P2PMode, hybrid.P2PAddress = "hybrid", "203.0.113.7"
	hybridConf := mustBuildLNDConfig(t, hybrid, "", "lnd", "secret")
	for _, want := range []string{
		"listen=0.0.0.0:9735",
		"restlisten=0.0.0.0:8080",
		"rpclisten=" + paths.LNDGRPCEndpoint,
		"externalhosts=203.0.113.7:9735",
		"tlsextraip=203.0.113.7",
	} {
		if !strings.Contains(hybridConf, want) {
			t.Errorf("hybrid config missing %q", want)
		}
	}

	// No host name in either variant, with or without a REST
	// onion for tlsextradomain.
	withOnion := mustBuildLNDConfig(t, cfg,
		"exampleonionaddress.onion", "lnd", "secret")
	for name, conf := range map[string]string{
		"tor-only": torOnly,
		"hybrid":   hybridConf,
		"onion":    withOnion,
	} {
		if strings.Contains(conf, "localhost") {
			t.Errorf("%s config contains the name localhost", name)
		}
	}
	if !strings.Contains(withOnion,
		"tlsextradomain=exampleonionaddress.onion") {
		t.Error("onion config missing tlsextradomain")
	}
}

func TestValidateLNDRESTOnion(t *testing.T) {
	onion := strings.Repeat("a", 56) + ".onion"
	if err := validateLNDRESTOnion(onion); err != nil {
		t.Fatalf("valid v3 onion rejected: %v", err)
	}
	for _, invalid := range []string{
		"", "short.onion", strings.Repeat("A", 56) + ".onion",
		strings.Repeat("0", 56) + ".onion", strings.Repeat("a", 56),
	} {
		if err := validateLNDRESTOnion(invalid); err == nil {
			t.Errorf("invalid onion accepted: %q", invalid)
		}
	}
}

func TestVerifyCertificateDNSName(t *testing.T) {
	onion := strings.Repeat("b", 56) + ".onion"
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "LND"},
		DNSNames:     []string{onion},
		IPAddresses:  []net.IP{net.ParseIP("203.0.113.7")},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(
		rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{
		Type: "CERTIFICATE", Bytes: der,
	})
	if err := verifyCertificateDNSName(certPEM, onion); err != nil {
		t.Fatalf("certificate with onion SAN rejected: %v", err)
	}
	if err := verifyCertificateDNSName(
		certPEM, strings.Repeat("c", 56)+".onion"); err == nil {
		t.Error("certificate missing the requested onion SAN was accepted")
	}
	if err := verifyCertificateDNSName([]byte("not pem"), onion); err == nil {
		t.Error("malformed certificate was accepted")
	}
	if err := verifyCertificateIPAddress(
		certPEM, "203.0.113.7"); err != nil {
		t.Fatalf("certificate with IP SAN rejected: %v", err)
	}
	if err := verifyCertificateIPAddress(
		certPEM, "203.0.113.8"); err == nil {
		t.Error("certificate missing the requested IP SAN was accepted")
	}
	if err := verifyCertificateIPAddress(certPEM, "not-an-ip"); err == nil {
		t.Error("invalid requested IP address was accepted")
	}
}

func TestBuildLNDConfigUsesIndependentBitcoindRPCIdentity(t *testing.T) {
	content := mustBuildLNDConfig(
		t, config.Default(), "", "lnd", "secret")
	for _, want := range []string{
		"bitcoind.rpcuser=lnd",
		"bitcoind.rpcpass=secret",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("LND config missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"bitcoind.rpccookie=",
		"bitcoind.dir=",
		"bitcoind.config=",
	} {
		if strings.Contains(content, forbidden) {
			t.Errorf("LND config still carries %q", forbidden)
		}
	}
}

func TestBuildLNDConfigPinsTorAndHealthPolicy(t *testing.T) {
	content := mustBuildLNDConfig(
		t, config.Default(), "", "lnd", "secret")
	values := activeLNDConfigValues(content)
	want := map[string]string{
		"lnddir":                              "/var/lib/lnd",
		"db.backend":                          "sqlite",
		"db.use-native-sql":                   "true",
		"tor.privatekeypath":                  "/var/lib/lnd/v3_onion_private_key",
		"tor.encryptkey":                      "false",
		"tor.skip-proxy-for-clearnet-targets": "false",
		"protocol.simple-taproot-chans":       "true",
		"protocol.no-onion-messages":          "false",
		"protocol.rbf-coop-close":             "true",
		"healthcheck.tls.attempts":            "0",
		"healthcheck.torconnection.interval":  "1m",
		"healthcheck.torconnection.timeout":   "5s",
		"healthcheck.torconnection.backoff":   "1m",
		"healthcheck.torconnection.attempts":  "10",
		"healthcheck.diskspace.diskrequired":  "0.10",
		"healthcheck.diskspace.attempts":      "2",
		"healthcheck.diskspace.interval":      "12h",
		"allow-circular-route":                "false",
	}
	for key, wantValue := range want {
		got := values[key]
		if len(got) != 1 || got[0] != wantValue {
			t.Errorf("config values for %q = %q, want exactly [%q]",
				key, got, wantValue)
		}
	}
	for _, forbidden := range []string{
		"db.bolt.", "db.sqlite.", "skip-native-sql-migration",
	} {
		if strings.Contains(content, forbidden) {
			t.Errorf("config contains unsupported database setting %q", forbidden)
		}
	}
}

func TestBuildLNDConfigNetworkProfiles(t *testing.T) {
	tests := []struct {
		network string
		flag    string
		rpc     string
		zmqB    string
		zmqT    string
	}{
		{config.NetworkMainnet, "bitcoin.mainnet=true", "8332", "28332", "28333"},
		{config.NetworkTestnet4, "bitcoin.testnet4=true", "48332", "28334", "28335"},
		{config.NetworkPublicSignet, "bitcoin.signet=true", "38332", "28336", "28337"},
	}
	for _, tt := range tests {
		t.Run(tt.network, func(t *testing.T) {
			cfg := config.Default()
			cfg.Network = tt.network
			content := mustBuildLNDConfig(t, cfg, "", "lnd", "secret")
			for _, want := range []string{
				tt.flag,
				"bitcoind.rpchost=127.0.0.1:" + tt.rpc,
				"bitcoind.zmqpubrawblock=tcp://127.0.0.1:" + tt.zmqB,
				"bitcoind.zmqpubrawtx=tcp://127.0.0.1:" + tt.zmqT,
			} {
				if !strings.Contains(content, want) {
					t.Errorf("config missing %q", want)
				}
			}
			for _, other := range []string{
				"bitcoin.mainnet=true", "bitcoin.testnet4=true", "bitcoin.signet=true",
			} {
				if other != tt.flag && strings.Contains(content, other) {
					t.Errorf("config contains foreign selector %q", other)
				}
			}
			for _, forbidden := range []string{
				"bitcoin.signetchallenge=", "bitcoin.signetseednode=",
				"bitcoind.rpccookie=", "/public-signet/",
			} {
				if strings.Contains(content, forbidden) {
					t.Errorf("config contains %q", forbidden)
				}
			}
		})
	}
}

// lnd.conf is generated only from a valid node configuration. A Hybrid mode
// without its reviewed address must not quietly produce a Tor-only file, and a
// Tor node must not publish an address.
func TestBuildLNDConfigRefusesAddressThatDoesNotFitTheMode(t *testing.T) {
	for name, set := range map[string]func(*config.AppConfig){
		"hybrid without address": func(c *config.AppConfig) { c.P2PMode = "hybrid" },
		"hybrid private address": func(c *config.AppConfig) { c.P2PMode, c.P2PAddress = "hybrid", "10.1.2.3" },
		"tor with an address":    func(c *config.AppConfig) { c.P2PAddress = "203.0.113.7" },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := config.Default()
			set(cfg)
			if content, err := BuildLNDConfig(cfg, "", "lnd", "secret"); err == nil {
				t.Fatalf("generated lnd.conf:\n%s", content)
			}
		})
	}
}

func TestBuildLNDConfigRejectsUnknownProfile(t *testing.T) {
	cfg := config.Default()
	cfg.Network = "signet"
	if _, err := BuildLNDConfig(cfg, "", "lnd", "secret"); err == nil {
		t.Fatal("raw signet profile generated an LND config")
	}
}

func TestParseLNDBitcoindRPCPassword(t *testing.T) {
	password, err := parseLNDBitcoindRPCPassword(
		"[Bitcoind]\nbitcoind.rpcuser=lnd\n" +
			"bitcoind.rpcpass=secret\n")
	if err != nil || password != "secret" {
		t.Fatalf("parse password: got %q, %v", password, err)
	}
	for name, content := range map[string]string{
		"missing": "bitcoind.rpcuser=lnd\n",
		"empty":   "bitcoind.rpcpass=\n",
		"duplicate": "bitcoind.rpcpass=one\n" +
			"bitcoind.rpcpass=two\n",
	} {
		if _, err := parseLNDBitcoindRPCPassword(content); err == nil {
			t.Errorf("%s password accepted", name)
		}
	}
}

// The update step lnd-config-v1 writes lnd.conf again from vpn's settings. It
// takes LND's Core password from the current file and the onion address from
// Tor, and refuses, before anything changes, a current file that disagrees
// with the node configuration. Hand edits elsewhere in the file are not kept.
func TestRewrittenLNDConfig(t *testing.T) {
	onion := strings.Repeat("a", 56) + ".onion"
	other := strings.Repeat("b", 56) + ".onion"
	tor := config.Default()
	tor.Network = config.NetworkPublicSignet
	hybrid := config.Default()
	hybrid.Network = config.NetworkPublicSignet
	hybrid.P2PMode, hybrid.P2PAddress = "hybrid", "203.0.113.7"
	testnet := config.Default()
	testnet.Network = config.NetworkTestnet4
	otherAddress := *hybrid
	otherAddress.P2PAddress = "203.0.113.8"

	for _, network := range config.SupportedNetworks() {
		for _, mode := range []string{"tor", "hybrid"} {
			node := config.Default()
			node.Network = network
			if mode == "hybrid" {
				node.P2PMode, node.P2PAddress = "hybrid", "203.0.113.7"
			}
			t.Run("keeps settings on a "+network+" "+mode+" node", func(t *testing.T) {
				keepsSettings(t, node, onion)
			})
		}
	}

	torFile := mustBuildLNDConfig(t, tor, onion, "lnd", "secret")
	hybridFile := mustBuildLNDConfig(t, hybrid, onion, "lnd", "secret")
	for _, tc := range []struct {
		name    string
		node    *config.AppConfig
		current string
		refused string
	}{
		{"one hybrid address line on a tor node", tor, torFile + "externalhosts=203.0.113.7:9735\n", "Hybrid lines"},
		{"open REST listener on a tor node", tor, strings.Replace(torFile, "restlisten="+paths.LNDRESTEndpoint, "restlisten=0.0.0.0:8080", 1), "Hybrid lines"},
		{"hybrid file without its certificate address", hybrid, strings.Replace(hybridFile, "tlsextraip=203.0.113.7\n", "", 1), "Hybrid address 203.0.113.7"},
		{"hybrid REST listener limited by hand", hybrid, strings.Replace(hybridFile, "restlisten=0.0.0.0:8080", "restlisten="+paths.LNDRESTEndpoint, 1), "Hybrid address 203.0.113.7"},
		{"two onion addresses", tor, torFile + "tlsextradomain=" + onion + "\n", "onion address"},
		{"another network switched off", tor, torFile + "bitcoin.testnet4=false\n", "public-signet network"},
		{"hybrid lines on a tor node", tor, hybridFile, "Hybrid lines"},
		{"tor file on a hybrid node", hybrid, torFile, "Hybrid address 203.0.113.7"},
		{"another hybrid address", hybrid, mustBuildLNDConfig(t, &otherAddress, onion, "lnd", "secret"), "Hybrid address 203.0.113.7"},
		{"another onion address", tor, mustBuildLNDConfig(t, tor, other, "lnd", "secret"), "onion address"},
		{"no onion address", tor, mustBuildLNDConfig(t, tor, "", "lnd", "secret"), "onion address"},
		{"another network", tor, mustBuildLNDConfig(t, testnet, onion, "lnd", "secret"), "public-signet network"},
		{"no password", tor, strings.Replace(torFile, "bitcoind.rpcpass=secret\n", "", 1), "rpcpass"},
		{"two passwords", tor, torFile + "bitcoind.rpcpass=other\n", "rpcpass"},
		{"public P2P listener on a tor node", tor, strings.Replace(torFile, "listen="+paths.LNDP2PBind, "listen=0.0.0.0:9735", 1), "Hybrid lines"},
		{"one certificate address on a tor node", tor, torFile + "tlsextraip=203.0.113.7\n", "Hybrid lines"},
		{"hybrid P2P listener on loopback", hybrid, strings.Replace(hybridFile, "listen=0.0.0.0:9735", "listen="+paths.LNDP2PBind, 1), "Hybrid address 203.0.113.7"},
		{"hybrid file without its announced address", hybrid, strings.Replace(hybridFile, "externalhosts=203.0.113.7:9735\n", "", 1), "Hybrid address 203.0.113.7"},
		{"no network selected", tor, strings.Replace(torFile, "bitcoin.signet=true\n", "", 1), "public-signet network"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := rewrittenLNDConfig(tc.current, tc.node, onion)
			if err == nil || !strings.Contains(err.Error(), tc.refused) {
				t.Fatalf("got %v, want a refusal naming %q, file:\n%s", err, tc.refused, got)
			}
		})
	}
}

// keepsSettings rewrites a file with the node's settings, an older value of
// allow-circular-route and a hand edit. It expects vpn's template with the
// node's settings, and the same bytes from a second run.
func keepsSettings(t *testing.T, node *config.AppConfig, onion string) {
	t.Helper()
	current := mustBuildLNDConfig(t, node, onion, "lnd", "secret")
	current = strings.Replace(current, "allow-circular-route=false", "allow-circular-route=true", 1)
	current += "alias=hand-edited\n"
	got, err := rewrittenLNDConfig(current, node, onion)
	if err != nil {
		t.Fatal(err)
	}
	if want := mustBuildLNDConfig(t, node, onion, "lnd", "secret"); got != want {
		t.Fatalf("rewritten file differs from vpn's template:\n%s", got)
	}
	again, err := rewrittenLNDConfig(got, node, onion)
	if err != nil || again != got {
		t.Fatalf("a second rewrite changed the file or failed: %v", err)
	}
}
