package host

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/update/files"
)

// StageLNDTLSCert copies LND's TLS certificate (the public
// half only, tls.key never moves) to the board. LND rewrites
// the cert during startup when its parameters change, so this
// polls until the file parses as a stable certificate. Two
// consecutive reads, half a second apart, must agree and parse.
// LND writes its pair without a sync, so the pair is synced
// before the copy is published, and a copy never outlives the
// files it came from after a power cut.
func StageLNDTLSCert() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("credential staging requires root")
	}
	return stageLNDTLSCert(paths.LNDTLSCert, paths.LNDTLSKey, os.ReadFile,
		func() error { return syncLNDFiles(paths.LNDTLSCert, paths.LNDTLSKey) },
		func(data []byte) error {
			return writeBoard(paths.StateLNDTLSCert, data)
		})
}

func stageLNDTLSCert(cert, key string, read func(string) ([]byte, error), sync func() error, publish func([]byte) error) error {
	data, err := waitLNDTLSFiles(cert, key, read, 60*time.Second)
	if err != nil {
		return err
	}
	if err := sync(); err != nil {
		return err
	}
	return publish(data)
}

// waitLNDTLSFiles waits until LND has written its whole TLS pair and returns
// the certificate. The certificate must parse and read the same twice, half a
// second apart. The key must pass LND's own test before it loads the pair.
func waitLNDTLSFiles(cert, key string, read func(string) ([]byte, error), timeout time.Duration) ([]byte, error) {
	deadline := time.Now().Add(timeout)
	var prev []byte
	for {
		data, err := read(cert)
		certReady := err == nil && certParses(data)
		keyData, keyErr := read(key)
		keyReady := keyErr == nil && lndKeyWritten(keyData)
		if certReady && keyReady && prev != nil && bytes.Equal(prev, data) {
			return data, nil
		}
		if certReady {
			prev = data
		} else {
			prev = nil
		}
		if time.Now().After(deadline) {
			if certReady {
				return nil, fmt.Errorf(
					"LND TLS key at %s not written after %ds",
					key, int(timeout/time.Second))
			}
			return nil, fmt.Errorf(
				"LND TLS certificate at %s not readable/stable "+
					"after %ds", cert, int(timeout/time.Second))
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// certParses reports whether data holds a parseable PEM
// certificate. It guards against copying a half-written file.
func certParses(data []byte) bool {
	block, _ := pem.Decode(data)
	if block == nil {
		return false
	}
	_, err := x509.ParseCertificate(block.Bytes)
	return err == nil
}

// lndKeyWritten is LND's own test of its TLS key before it loads the pair. A
// plain PEM key passes. LND takes any other key, an empty one too, for an
// encrypted key and refuses to start.
func lndKeyWritten(data []byte) bool {
	return bytes.HasPrefix(data, []byte("-----BEGIN "))
}

// syncLNDFiles puts files LND wrote in one folder on disk, then the folder.
// LND writes its TLS pair in place without a sync, so a power cut soon after
// can leave both files empty, and LND then refuses every start. A sync from
// here commits what LND wrote.
func syncLNDFiles(names ...string) error {
	for _, name := range names {
		if err := files.Sync(name); err != nil {
			return fmt.Errorf("sync %s: %w", name, err)
		}
	}
	return syncDir(filepath.Dir(names[0]))
}

// StageLNDMacaroon copies the admin macaroon to the board. It
// requires the macaroon to exist. The callers are the moments
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
