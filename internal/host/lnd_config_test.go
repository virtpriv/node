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
	t *testing.T, cfg *config.AppConfig, publicIPv4, restOnion,
	user, password string,
) string {
	t.Helper()
	content, err := BuildLNDConfig(
		cfg, publicIPv4, restOnion, user, password)
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
// addresses defined once in paths. the same constants every
// client dials. and carries no host name anywhere. On this
// node, which disables IPv6, the name localhost can resolve to
// an unusable IPv6 address, so it must never appear.
func TestBuildLNDConfigBindsByAddress(t *testing.T) {
	// Tor-only: every listener on the loopback constants.
	cfg := config.Default()
	torOnly := mustBuildLNDConfig(t, cfg, "", "", "lnd", "secret")
	for _, want := range []string{
		"listen=" + paths.LNDP2PBind,
		"restlisten=" + paths.LNDRESTEndpoint,
		"rpclisten=" + paths.LNDGRPCEndpoint,
	} {
		if !strings.Contains(torOnly, want) {
			t.Errorf("tor-only config missing %q", want)
		}
	}

	// Hybrid: P2P and REST bind all interfaces as computed;
	// gRPC stays on the loopback constant.
	hybrid := config.Default()
	hybrid.P2PMode = "hybrid"
	hybridConf := mustBuildLNDConfig(
		t, hybrid, "203.0.113.7", "", "lnd", "secret")
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
	withOnion := mustBuildLNDConfig(t, cfg, "",
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
		t, config.Default(), "", "", "lnd", "secret")
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
		t, config.Default(), "", "", "lnd", "secret")
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
			content := mustBuildLNDConfig(t, cfg, "", "", "lnd", "secret")
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

func TestBuildLNDConfigRejectsUnknownProfile(t *testing.T) {
	cfg := config.Default()
	cfg.Network = "signet"
	if _, err := BuildLNDConfig(cfg, "", "", "lnd", "secret"); err == nil {
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
