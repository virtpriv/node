package syncthing

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func privateOptions() map[string]any {
	return map[string]any{
		"globalAnnounceEnabled": false, "localAnnounceEnabled": false, "relaysEnabled": false, "natEnabled": false,
		"announceLANAddresses": false, "crashReportingEnabled": false, "autoUpgradeIntervalH": 0, "urAccepted": -1,
		"listenAddresses": []string{"tcp://0.0.0.0:22000"},
	}
}
func privateGUI() map[string]any {
	return map[string]any{"address": "127.0.0.1:8384", "enabled": true, "useTLS": false, "user": "admin", "password": "test-hash", "metricsWithoutAuth": false}
}
func TestPrivacyRejectsMissingAndChangedFields(t *testing.T) {
	opts, gui := privateOptions(), privateGUI()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Error("privacy check mutated configuration")
		}
		if r.URL.Path == "/rest/config/options" {
			json.NewEncoder(w).Encode(opts)
		} else {
			json.NewEncoder(w).Encode(gui)
		}
	}))
	defer srv.Close()
	c := newClient(srv.URL, "key")
	if err := c.ConfirmPrivacy(context.Background()); err != nil {
		t.Fatal(err)
	}
	for name, fields := range map[string]map[string]any{"options": opts, "gui": gui} {
		for key, original := range fields {
			for _, mutation := range []string{"missing", "null", "wrong"} {
				t.Run(name+"/"+key+"/"+mutation, func(t *testing.T) {
					switch mutation {
					case "missing":
						delete(fields, key)
					case "null":
						fields[key] = nil
					case "wrong":
						switch v := original.(type) {
						case bool:
							fields[key] = !v
						case int:
							fields[key] = v + 1
						case string:
							fields[key] = ""
						default:
							fields[key] = []string{"default"}
						}
					}
					if err := c.ConfirmPrivacy(context.Background()); err == nil {
						t.Fatal("unsafe/missing privacy field accepted")
					}
					fields[key] = original
				})
			}
		}
	}
}
func TestTransportRefusesRedirectAndOversizedResponse(t *testing.T) {
	leaked := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
			return
		}
		fmt.Fprint(w, strings.Repeat("x", responseLimit+1))
	}))
	defer srv.Close()
	c := newClient(srv.URL, "secret")
	for _, path := range []string{"/redirect", "/large"} {
		if _, err := c.Request(context.Background(), "GET", path, ""); err == nil {
			t.Fatalf("accepted %s", path)
		}
	}
	if leaked {
		t.Fatal("followed credential-bearing redirect")
	}
	if c.http.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("transport consults proxy settings")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Request(ctx, "POST", "/cancel", `{}`); err == nil {
		t.Fatal("canceled request succeeded")
	}
}

func TestIdentityUsesStatus(t *testing.T) {
	id := `{"myID":"LOCAL"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/system/status" {
			t.Fatalf("unexpected call %s", r.URL.Path)
		}
		fmt.Fprint(w, id)
	}))
	defer srv.Close()
	c := newClient(srv.URL, "k")
	if got, err := c.LocalID(context.Background()); err != nil || got != "LOCAL" {
		t.Fatalf("local ID: %s %v", got, err)
	}
	id = `{}`
	if _, err := c.LocalID(context.Background()); err == nil {
		t.Fatal("a status without an ID became an identity")
	}
}
