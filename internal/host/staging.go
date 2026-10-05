package host

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"time"

	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/paths"
)

// StageLNDTLSCert copies LND's TLS certificate (the public
// half only; tls.key never moves) to the board. LND rewrites
// the cert during startup when its parameters change, so this
// polls until the file parses as a stable certificate: two
// consecutive reads, half a second apart, must agree and parse.
func StageLNDTLSCert() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("credential staging requires root")
	}
	return stageLNDTLSCert(paths.LNDTLSCert, os.ReadFile, func(data []byte) error {
		return writeBoard(paths.StateLNDTLSCert, data)
	})
}

func stageLNDTLSCert(src string, read func(string) ([]byte, error), publish func([]byte) error) error {
	deadline := time.Now().Add(60 * time.Second)
	var prev []byte
	for {
		data, err := read(src)
		if err == nil && certParses(data) {
			if prev != nil && bytes.Equal(prev, data) {
				return publish(data)
			}
			prev = data
		} else {
			prev = nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf(
				"LND TLS certificate at %s not readable/stable "+
					"after 60s", src)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// certParses reports whether data holds a parseable PEM
// certificate; the guard against copying a half-written file.
func certParses(data []byte) bool {
	block, _ := pem.Decode(data)
	if block == nil {
		return false
	}
	_, err := x509.ParseCertificate(block.Bytes)
	return err == nil
}

// StageLNDMacaroon copies the admin macaroon to the board. It
// requires the macaroon to exist: the callers are the moments
// that create it (wallet creation) or that follow its known
// existence. For the install-time case where no wallet exists
// yet, the installer skips this operation.
func StageLNDMacaroon() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("credential staging requires root")
	}
	network, err := macaroonNetworkDir()
	if err != nil {
		return err
	}
	return stageLNDMacaroon(paths.LNDMacaroon(network), os.ReadFile, func(data []byte) error {
		return writeBoard(paths.StateLNDMacaroon, data)
	})
}

func stageLNDMacaroon(src string, read func(string) ([]byte, error), publish func([]byte) error) error {
	deadline := time.Now().Add(30 * time.Second)
	for {
		data, err := read(src)
		if err == nil && len(data) > 0 {
			return publish(data)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf(
				"admin macaroon at %s not readable after 30s "+
					"(%v)", src, err)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// macaroonNetworkDir resolves the network directory in LND's
// macaroon path from the node's config.
func macaroonNetworkDir() (string, error) {
	cfg, err := config.Load()
	if err != nil {
		return "", fmt.Errorf("read node config: %w", err)
	}
	profile, err := cfg.NetworkConfig()
	if err != nil {
		return "", fmt.Errorf("resolve node network profile: %w", err)
	}
	return profile.LNDNetwork, nil
}

// StageSyncthingAPIKey extracts the GUI API key from
// Syncthing's config and stages it. With the key on the board,
// every runtime device operation (pair, unpair, folder
// sharing) is plain localhost REST with no privilege at all.
func StageSyncthingAPIKey() error {
	key, err := SyncthingAPIKey()
	if err != nil {
		return fmt.Errorf("read Syncthing API key: %w", err)
	}
	return writeBoard(
		paths.StateSyncthingAPIKey, []byte(key+"\n"))
}
