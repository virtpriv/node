package host

import (
	"os"
	"strings"

	"github.com/virtpriv/node/internal/paths"
)

// OnionAddresses contains the fixed Tor hostname observations used for display.
// Empty fields mean unavailable, including missing or unreadable files.
type OnionAddresses struct {
	BitcoinP2P string
	LNDGRPC    string
	LNDREST    string
	Syncthing  string
}

// ReadOnionAddresses reads current display facts without staging or mutation.
// Provisioning separately validates required hostnames before using them.
func ReadOnionAddresses() OnionAddresses {
	return OnionAddresses{
		BitcoinP2P: readOnionHostname(paths.TorBitcoinP2P + "/hostname"),
		LNDGRPC:    readOnionHostname(paths.TorLNDGRPC + "/hostname"),
		LNDREST:    readOnionHostname(paths.TorLNDRESTHostname),
		Syncthing:  readOnionHostname(paths.TorSyncthingHostname),
	}
}

func readOnionHostname(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// ValidV3OnionHostname checks the syntax of a native Tor v3 hostname.
func ValidV3OnionHostname(hostname string) bool {
	const suffix = ".onion"
	if len(hostname) != 56+len(suffix) ||
		!strings.HasSuffix(hostname, suffix) {
		return false
	}
	for _, c := range strings.TrimSuffix(hostname, suffix) {
		if (c < 'a' || c > 'z') && (c < '2' || c > '7') {
			return false
		}
	}
	return true
}
