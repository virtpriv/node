package host

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"
)

func stagingCertificate(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(
		rand.Reader, &tmpl, &tmpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(
		&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestStageTLSRequiresConsecutiveStableCertificate(t *testing.T) {
	first, final := stagingCertificate(t), stagingCertificate(t)
	for name, interruption := range map[string][]byte{
		"truncated": first[:len(first)/2], "invalid": []byte("not PEM"), "empty": nil,
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				// A complete read before the interruption must not count toward
				// stability afterward. A subsequent cert rotation also resets it.
				reads := [][]byte{first, interruption, first, final, final}
				count, published := 0, 0
				start := time.Now()
				err := stageLNDTLSCert("cert", "key", func(path string) ([]byte, error) {
					if path == "key" {
						return []byte("-----BEGIN EC PRIVATE KEY-----\n"), nil
					}
					if count >= len(reads) {
						t.Fatal("stable source was not published")
					}
					data := reads[count]
					count++
					return data, nil
				}, func() error { return nil }, func(data []byte) error {
					published++
					if !bytes.Equal(data, final) || time.Since(start) < 2*time.Second {
						t.Fatal("published an unstable or invalid certificate")
					}
					return nil
				})
				if err != nil || published != 1 {
					t.Fatalf("published=%d err=%v", published, err)
				}
			})
		})
	}
}

// LND writes its key just after its certificate, both without a sync. A copy
// published while the key is empty or missing would outlive LND's own files
// after a power cut, so the stager waits for the key and syncs first.
func TestStageTLSWaitsForLNDKeyAndSyncsBeforePublishing(t *testing.T) {
	cert := stagingCertificate(t)
	key := []byte("-----BEGIN EC PRIVATE KEY-----\nAAAA\n-----END EC PRIVATE KEY-----\n")
	for name, notReady := range map[string]func() ([]byte, error){
		"empty":   func() ([]byte, error) { return nil, nil },
		"missing": func() ([]byte, error) { return nil, os.ErrNotExist },
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				synced, published := 0, 0
				err := stageLNDTLSCert("cert", "key", func(path string) ([]byte, error) {
					if path == "cert" {
						return cert, nil
					}
					if time.Since(start) < 2*time.Second {
						return notReady()
					}
					return key, nil
				}, func() error {
					if time.Since(start) < 2*time.Second {
						t.Fatal("synced before the key was written")
					}
					synced++
					return nil
				}, func(data []byte) error {
					if synced != 1 {
						t.Fatal("published before LND's files were synced")
					}
					published++
					return nil
				})
				if err != nil || published != 1 {
					t.Fatalf("published=%d err=%v", published, err)
				}
			})
		})
	}
}

func TestCredentialReadTimeoutNeverPublishes(t *testing.T) {
	noSync := func() error { return nil }
	for name, stage := range map[string]func(string, func(string) ([]byte, error), func([]byte) error) error{
		"TLS": func(src string, read func(string) ([]byte, error), publish func([]byte) error) error {
			return stageLNDTLSCert(src, src, read, noSync, publish)
		},
		"macaroon": stageLNDMacaroon,
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				err := stage("missing", func(string) ([]byte, error) { return nil, os.ErrNotExist },
					func([]byte) error { t.Fatal("published unavailable source"); return nil })
				if err == nil {
					t.Fatal("unavailable source reported success")
				}
			})
		})
	}
}

func TestMacaroonWaitsForNonemptySourceAndPropagatesPublicationFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		failure := errors.New("publication failed")
		start := time.Now()
		calls := 0
		err := stageLNDMacaroon("macaroon", func(string) ([]byte, error) {
			if time.Since(start) < time.Second {
				return nil, nil
			}
			return []byte("credential"), nil
		}, func(data []byte) error {
			calls++
			if string(data) != "credential" {
				t.Fatal("published empty or changed credential")
			}
			return failure
		})
		if !errors.Is(err, failure) || calls != 1 {
			t.Fatalf("calls=%d err=%v", calls, err)
		}
	})
}

func TestSyncthingAPIKeySource(t *testing.T) {
	for name, source := range map[string]string{
		"valid":      `<configuration><gui><apikey>private-key</apikey></gui></configuration>`,
		"wrong root": `<other><gui><apikey>private-key</apikey></gui></other>`,
		"missing":    `<configuration><gui/></configuration>`,
		"empty":      `<configuration><gui><apikey/></gui></configuration>`,
		"truncated":  `<configuration><gui><apikey>private-key`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.xml")
			if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
				t.Fatal(err)
			}
			key, err := syncthingAPIKeyAt(path)
			if name == "valid" {
				if err != nil || key != "private-key" {
					t.Fatalf("key=%q err=%v", key, err)
				}
			} else if err == nil || key != "" {
				t.Fatalf("invalid source yielded key=%q err=%v", key, err)
			}
		})
	}
}
