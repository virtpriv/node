package host

import (
	"fmt"
	"os"
	"strings"

	"github.com/virtpriv/node/internal/paths"
)

// OnionAddresses contains the fixed Tor hostname observations used for display.
// Empty fields mean unavailable, including missing or unreadable files.
type OnionAddresses struct {
	LNDGRPC   string
	LNDREST   string
	Syncthing string
}

// ReadOnionAddresses reads current display facts without staging or mutation.
// Provisioning separately validates required hostnames before using them.
func ReadOnionAddresses() OnionAddresses {
	return OnionAddresses{
		LNDGRPC:   readOnionHostname(paths.TorLNDGRPC + "/hostname"),
		LNDREST:   readOnionHostname(paths.TorLNDRESTHostname),
		Syncthing: readOnionHostname(paths.TorSyncthingHostname),
	}
}

func readOnionHostname(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// readRequiredOnion reads the onion address Tor made for a service, for a
// config file that names it. A missing, unreadable or malformed hostname is an
// error, so no file is written without the address or with a wrong one. name
// is the service as the error names it.
func readRequiredOnion(hostnameFile, name string) (string, error) {
	data, err := os.ReadFile(hostnameFile)
	if err != nil {
		return "", fmt.Errorf("read %s onion hostname: %w", name, err)
	}
	onion := strings.TrimSpace(string(data))
	if !ValidV3OnionHostname(onion) {
		return "", fmt.Errorf("invalid %s onion hostname %q", name, onion)
	}
	return onion, nil
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
