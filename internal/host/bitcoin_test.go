package host

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/virtpriv/node/internal/bitcoin"
	"github.com/virtpriv/node/internal/config"
)

func mustBuildBitcoinConfig(
	t *testing.T, cfg *config.AppConfig, lines ...string,
) string {
	t.Helper()
	content, err := buildBitcoinConfig(cfg, lines...)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func TestBitcoinConfigMainnet(t *testing.T) {
	cfg := config.Default()
	content := mustBuildBitcoinConfig(t, cfg, "")

	required := []string{
		"server=1",
		"disablewallet=1",
		"norpccookiefile=1",
		"prune=25000",
		"proxy=127.0.0.1:9050",
		"rpcport=8332",
		"zmqpubrawblock=tcp://127.0.0.1:28332",
		"zmqpubrawtx=tcp://127.0.0.1:28333",
		"listen=1",
		"listenonion=1",
		"dbcache=512",
		"maxmempool=300",
		"bind=127.0.0.1",
		"rpcbind=127.0.0.1",
		"rpcallowip=127.0.0.1",
	}
	for _, req := range required {
		if !strings.Contains(content, req) {
			t.Errorf("mainnet config missing %q", req)
		}
	}

	if strings.Contains(content, "testnet4=1") {
		t.Error("mainnet config should not contain testnet4 flag")
	}
}

func TestBitcoinConfigTestnet4(t *testing.T) {
	cfg := &config.AppConfig{
		Network:   "testnet4",
		PruneSize: 25,
		P2PMode:   "tor",
	}
	content := mustBuildBitcoinConfig(t, cfg, "")

	required := []string{
		"testnet4=1",
		"prune=25000",
		"rpcport=48332",
		"zmqpubrawblock=tcp://127.0.0.1:28334",
		"zmqpubrawtx=tcp://127.0.0.1:28335",
		"[testnet4]",
	}
	for _, req := range required {
		if !strings.Contains(content, req) {
			t.Errorf("testnet4 config missing %q", req)
		}
	}
}

func TestBitcoinConfigPublicSignet(t *testing.T) {
	cfg := config.Default()
	cfg.Network = config.NetworkPublicSignet
	content := mustBuildBitcoinConfig(t, cfg,
		"rpcauth=vpn:aabb$ccdd", "rpcauth=lnd:eeff$0011")
	for _, want := range []string{
		"signet=1", "[signet]", "rpcport=38332",
		"zmqpubrawblock=tcp://127.0.0.1:28336",
		"zmqpubrawtx=tcp://127.0.0.1:28337",
		"norpccookiefile=1",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("public-signet config missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"signetchallenge=", "signetseednode=", "testnet4=1",
		".cookie",
	} {
		if strings.Contains(content, forbidden) {
			t.Errorf("public-signet config contains %q", forbidden)
		}
	}
}

func TestBitcoinConfigDisablesUnusedCookieForEveryProfile(t *testing.T) {
	for _, network := range config.SupportedNetworks() {
		cfg := config.Default()
		cfg.Network = network
		content := mustBuildBitcoinConfig(t, cfg)
		if strings.Count(content, "norpccookiefile=1") != 1 {
			t.Errorf("%s cookie disable count is %d", network,
				strings.Count(content, "norpccookiefile=1"))
		}
		if strings.Contains("\n"+content, "\nrpccookiefile=") {
			t.Errorf("%s config enables an RPC cookie path", network)
		}
		for _, forbidden := range []string{".cookie"} {
			if strings.Contains(content, forbidden) {
				t.Errorf("%s config contains %q", network, forbidden)
			}
		}
	}
}

func TestBitcoinConfigRejectsUnknownProfile(t *testing.T) {
	cfg := config.Default()
	cfg.Network = "signet"
	if _, err := buildBitcoinConfig(cfg); err == nil {
		t.Fatal("raw signet profile generated a Bitcoin config")
	}
}

// The rpcauth credential line must land in the GLOBAL section:
// auth options are not network-scoped, and on testnet4 a line
// appended at the end would fall inside [testnet4].
func TestBitcoinConfigRPCAuthPlacement(t *testing.T) {
	lines := []string{"rpcauth=vpn:aabb$ccdd", "rpcauth=lnd:eeff$0011"}
	for _, network := range config.SupportedNetworks() {
		t.Run(network, func(t *testing.T) {
			cfg := config.Default()
			cfg.Network = network
			content := mustBuildBitcoinConfig(t, cfg, lines...)
			section := strings.Index(content, "\n[")
			for _, line := range lines {
				index := strings.Index(content, line+"\n")
				if index < 0 || (section >= 0 && index > section) {
					t.Errorf("RPC credential is missing or network-scoped: %s", line)
				}
			}
		})
	}
}

func TestBitcoindServiceUsesDedicatedIdentityAndTorGroup(t *testing.T) {
	unit := bitcoindServiceUnit(bitcoinUser)
	for key, want := range map[string]string{
		"User":                "bitcoin",
		"Group":               "bitcoin",
		"SupplementaryGroups": "debian-tor",
		"UMask":               "0077",
	} {
		assertSingleUnitDirective(t, unit, "Service", key, want)
	}
	if strings.Contains(unit, backupGroup) {
		t.Error("normal bitcoind unit has channel-backup access")
	}
}

func TestValidateBitcoinIdentityForEveryProfile(t *testing.T) {
	for _, network := range config.SupportedNetworks() {
		profile, err := config.NetworkConfigFromName(network)
		if err != nil {
			t.Fatal(err)
		}
		identity := bitcoin.BlockchainIdentity{
			Chain:           profile.CoreNetwork,
			Genesis:         profile.ExpectedGenesis,
			SignetChallenge: profile.ExpectedSignetChallenge,
		}
		if err := validateBitcoinIdentity(profile, identity); err != nil {
			t.Errorf("%s identity rejected: %v", network, err)
		}
		wrong := identity
		wrong.Genesis = "wrong"
		if err := validateBitcoinIdentity(profile, wrong); err == nil {
			t.Errorf("%s wrong genesis accepted", network)
		}
	}
}

func TestPublicSignetIdentityRejectsCustomChallenge(t *testing.T) {
	profile, err := config.NetworkConfigFromName(config.NetworkPublicSignet)
	if err != nil {
		t.Fatal(err)
	}
	identity := bitcoin.BlockchainIdentity{
		Chain: "signet", Genesis: config.PublicSignetGenesis,
		SignetChallenge: "51",
	}
	if err := validateBitcoinIdentity(profile, identity); err == nil {
		t.Fatal("custom signet challenge accepted as public signet")
	}
}

func TestWaitForBitcoinIdentityRetriesOnlyUnavailability(t *testing.T) {
	profile, err := config.NetworkConfigFromName(config.NetworkMainnet)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	err = waitForBitcoinIdentity(profile,
		func(port int) (bitcoin.BlockchainIdentity, error) {
			calls++
			if port != profile.RPCPort {
				t.Fatalf("probe port %d, want %d", port, profile.RPCPort)
			}
			if calls < 3 {
				return bitcoin.BlockchainIdentity{}, fmt.Errorf("starting")
			}
			return bitcoin.BlockchainIdentity{
				Chain: profile.CoreNetwork, Genesis: profile.ExpectedGenesis,
			}, nil
		}, func(time.Duration) {}, 3, time.Second)
	if err != nil || calls != 3 {
		t.Fatalf("wait result calls=%d err=%v", calls, err)
	}
}

func TestWaitForBitcoinIdentityRejectsWrongChainWithoutRetry(t *testing.T) {
	profile, err := config.NetworkConfigFromName(config.NetworkPublicSignet)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	sleeps := 0
	err = waitForBitcoinIdentity(profile,
		func(port int) (bitcoin.BlockchainIdentity, error) {
			calls++
			if port != profile.RPCPort {
				t.Fatalf("probe port %d, want %d", port, profile.RPCPort)
			}
			return bitcoin.BlockchainIdentity{
				Chain: "main", Genesis: profile.ExpectedGenesis,
				SignetChallenge: profile.ExpectedSignetChallenge,
			}, nil
		}, func(time.Duration) { sleeps++ }, 3, time.Second)
	if err == nil || !strings.Contains(err.Error(),
		`Bitcoin Core reports chain "main", want "signet"`) {
		t.Fatalf("wrong-chain result: %v", err)
	}
	if calls != 1 || sleeps != 0 {
		t.Fatalf("wrong-chain calls=%d sleeps=%d, want 1 and 0", calls, sleeps)
	}
}

func TestWaitForBitcoinIdentityFailsAfterRPCUnavailable(t *testing.T) {
	profile, err := config.NetworkConfigFromName(config.NetworkPublicSignet)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	sleeps := 0
	err = waitForBitcoinIdentity(profile,
		func(port int) (bitcoin.BlockchainIdentity, error) {
			calls++
			if port != profile.RPCPort {
				t.Fatalf("probe port %d, want %d", port, profile.RPCPort)
			}
			return bitcoin.BlockchainIdentity{}, fmt.Errorf("connection refused")
		}, func(time.Duration) { sleeps++ }, 3, time.Second)
	if err == nil || !strings.Contains(err.Error(),
		"did not expose verifiable public-signet identity on RPC port 38332") ||
		!strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("unavailable result: %v", err)
	}
	if calls != 3 || sleeps != 2 {
		t.Fatalf("unavailable calls=%d sleeps=%d, want 3 and 2", calls, sleeps)
	}
}
