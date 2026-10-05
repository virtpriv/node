package app

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/virtpriv/node/internal/paths"
)

func TestConnectionInfoStagedContractAndIndependentFailures(t *testing.T) {
	for _, target := range []ConnectionTarget{LNDRESTConnection, SyncthingWebConnection} {
		t.Run(map[ConnectionTarget]string{LNDRESTConnection: "LND REST", SyncthingWebConnection: "Syncthing Web UI"}[target], func(t *testing.T) {
			r := NewConnectionInfoReader()
			defer r.Close()
			calls := 0
			helperErr := error(nil)
			payload := `{"lnd_rest_onion":"rest.onion","syncthing_onion":"sync.onion","lnd_grpc_onion":"wrong.onion"}`
			r.call = func(_ context.Context, verb string, params, result any) error {
				calls++
				if verb != "read-node-addresses" || params != nil {
					t.Fatalf("unexpected helper request: %s %v", verb, params)
				}
				if helperErr != nil {
					return helperErr
				}
				return json.Unmarshal([]byte(payload), result)
			}
			fixture := filepath.Join(t.TempDir(), "staged")
			wantPath, secret, host := paths.StateLNDMacaroon, "\x00\xfb\xff\x20synthetic-macaroon", "rest.onion"
			if target == SyncthingWebConnection {
				wantPath, secret, host = paths.StateSyncthingWebPassword, "  synthetic-pass\n", "sync.onion"
			}
			if err := os.WriteFile(fixture, []byte(secret), 0600); err != nil {
				t.Fatal(err)
			}
			r.readFile = func(path string) ([]byte, error) {
				if path != wantPath {
					t.Fatalf("read wrong staged source: %s", path)
				}
				return os.ReadFile(fixture)
			}
			result := r.Read(target)
			if result.Address != host || result.AddressErr != nil || !result.HasCredential() {
				t.Fatal("lost staged endpoint or credential")
			}
			if target == LNDRESTConnection {
				u, err := url.Parse(result.URL("2001:db8::1"))
				if err != nil {
					t.Fatal(err)
				}
				raw, err := base64.RawURLEncoding.DecodeString(u.Query().Get("macaroon"))
				copied, copyErr := hex.DecodeString(result.CredentialText())
				if err != nil || copyErr != nil || string(raw) != secret || string(copied) != secret || u.Scheme != "lndconnect" || u.Host != "[2001:db8::1]:8080" {
					t.Fatal("REST connection encoding changed credential bytes or endpoint")
				}
			} else if result.URL(host) != "http://sync.onion:8384" || result.CredentialText() != "synthetic-pass" {
				t.Fatal("Web UI contract changed")
			}
			for _, value := range []any{result, result.Credential} {
				if diagnostic := fmt.Sprintf("%v %+v %#v", value, value, value); strings.Contains(diagnostic, "synthetic-") || strings.Contains(diagnostic, result.CredentialText()) {
					t.Fatal("default diagnostic exposed credential")
				}
			}
			helperErr = errors.New("socket unavailable")
			result = r.Read(target)
			if !errors.Is(result.AddressErr, helperErr) || result.Address != "" || !result.HasCredential() {
				t.Fatal("endpoint failure discarded independent staged credential")
			}
			helperErr = nil
			if err := os.Remove(fixture); err != nil {
				t.Fatal(err)
			}
			result = r.Read(target)
			if result.AddressErr != nil || result.Address != host || result.HasCredential() || result.CredentialText() != "" || result.CredentialErr == nil {
				t.Fatal("credential failure reused old bytes or lost usable endpoint")
			}
			if target == LNDRESTConnection && result.URL(host) != "" {
				t.Fatal("built pairing URI without credential")
			}
			if target == SyncthingWebConnection && result.URL(host) == "" {
				t.Fatal("password failure hid independent URL")
			}
			if err := os.WriteFile(fixture, nil, 0600); err != nil {
				t.Fatal(err)
			}
			payload = `{}`
			result = r.Read(target)
			if result.AddressErr == nil || result.CredentialErr == nil || calls != 4 {
				t.Fatal("empty sources accepted or hidden retry performed")
			}
		})
	}
}

func TestConnectionInfoDeadlineAndCloseJoin(t *testing.T) {
	for _, closeEarly := range []bool{false, true} {
		t.Run(map[bool]string{false: "deadline", true: "close joins reader"}[closeEarly], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := NewConnectionInfoReader()
				defer r.Close()
				calls := 0
				release := make(chan struct{})
				r.call = func(ctx context.Context, _ string, _, _ any) error {
					calls++
					<-ctx.Done()
					if closeEarly {
						<-release
					}
					return ctx.Err()
				}
				r.readFile = func(string) ([]byte, error) { t.Error("read credential after cancellation"); return nil, nil }
				started := time.Now()
				done := make(chan ConnectionInfo, 1)
				go func() { done <- r.Read(LNDRESTConnection) }()
				want := context.DeadlineExceeded
				if closeEarly {
					want = context.Canceled
					synctest.Wait()
					closed := make(chan struct{})
					go func() { r.Close(); close(closed) }()
					synctest.Wait()
					select {
					case <-closed:
						t.Fatal("Close returned before reader finished")
					default:
					}
					close(release)
					<-closed
				}
				result := <-done
				if !errors.Is(result.AddressErr, want) || !errors.Is(result.CredentialErr, want) || result.HasCredential() {
					t.Fatal("canceled read published usable data")
				}
				if !closeEarly && time.Since(started) != 30*time.Second {
					t.Fatal("read deadline changed")
				}
				r.Close()
				result = r.Read(SyncthingWebConnection)
				if calls != 1 || !errors.Is(result.AddressErr, context.Canceled) {
					t.Fatal("accepted work after shutdown or retried")
				}
			})
		})
	}
}
