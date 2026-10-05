package syncthing

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/virtpriv/node/internal/paths"
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
func TestSharePreservesCompleteEntriesAndOnlyPatchesDevices(t *testing.T) {
	existing := json.RawMessage(`{"deviceID":"REMOTE","introducedBy":"INTRODUCER","encryptionPassword":"test-secret","future":{"keep":true}}`)
	local := json.RawMessage(`{"deviceID":"LOCAL"}`)
	var patched map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/config/folders/lnd-backup" {
			t.Errorf("wrong folder: %s", r.URL.Path)
		}
		if r.Method == "GET" {
			json.NewEncoder(w).Encode(BackupFolder{ID: "lnd-backup", Path: paths.LNDBackupExport, Type: "sendonly", Marker: paths.ExportReadyMarkerName, Devices: []json.RawMessage{local, existing}})
			return
		}
		if r.Method != "PATCH" {
			t.Errorf("method=%s", r.Method)
		}
		json.NewDecoder(r.Body).Decode(&patched)
	}))
	defer srv.Close()
	c := newClient(srv.URL, "key")
	folder, err := c.BackupFolder(context.Background(), "LOCAL")
	if err != nil {
		t.Fatal(err)
	}
	if err = c.ShareBackup(context.Background(), folder, "NEW"); err != nil {
		t.Fatal(err)
	}
	if len(patched) != 1 {
		t.Fatalf("patch changed other settings: %v", patched)
	}
	var entries []json.RawMessage
	if err = json.Unmarshal(patched["devices"], &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("lost existing shares: %s", patched["devices"])
	}
	for i, expected := range []json.RawMessage{local, existing, json.RawMessage(`{"deviceID":"NEW"}`)} {
		var want, got any
		if err := json.Unmarshal(expected, &want); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(entries[i], &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("share %d: got %s, want %s", i, entries[i], expected)
		}
	}
	// An already shared device requires no replacement write.
	patched = nil
	if err = c.ShareBackup(context.Background(), folder, "REMOTE"); err != nil || patched != nil {
		t.Fatalf("duplicate share writes configuration: %v", err)
	}
}
func TestBackupBoundaryRejectsDrift(t *testing.T) {
	f := BackupFolder{ID: "lnd-backup", Path: paths.LNDBackupExport, Type: "sendonly", Marker: paths.ExportReadyMarkerName, Devices: []json.RawMessage{json.RawMessage(`{"deviceID":"LOCAL"}`)}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(f) }))
	defer srv.Close()
	c := newClient(srv.URL, "k")
	for _, mutate := range []func(){func() { f.ID = "other" }, func() { f.Path = "/var/lib/lnd" }, func() { f.Type = "sendreceive" }, func() { f.Marker = ".stfolder" }, func() { f.Devices = nil }} {
		original := f
		mutate()
		if _, err := c.BackupFolder(context.Background(), "LOCAL"); err == nil {
			t.Fatal("unsafe backup folder accepted")
		}
		f = original
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
func TestMutationLockAcrossProcesses(t *testing.T) {
	if dir := os.Getenv("VPN_SYNC_LOCK_TEST"); dir != "" {
		called := false
		err := withMutationLock(dir, func() error { called = true; return nil })
		if err == nil || called {
			t.Fatal("contending process entered mutation")
		}
		return
	}
	dir := t.TempDir()
	err := withMutationLock(dir, func() error {
		cmd := exec.Command(os.Args[0], "-test.run=^TestMutationLockAcrossProcesses$")
		cmd.Env = append(os.Environ(), "VPN_SYNC_LOCK_TEST="+dir)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("subprocess: %w: %s", err, output)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = withMutationLock(dir, func() error { return nil }); err != nil {
		t.Fatal("lock not released:", err)
	}
}
func TestIdentityUsesStatusAndDaemonParser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/system/status":
			fmt.Fprint(w, `{"myID":"LOCAL"}`)
		case "/rest/config":
			fmt.Fprint(w, `{"devices":[{"deviceID":"AAA","name":"first"},{"deviceID":"LOCAL"}],"folders":[]}`)
		case "/rest/svc/deviceid":
			if r.URL.Query().Get("id") == "invalid" {
				fmt.Fprint(w, `{"error":"bad checksum"}`)
			} else {
				fmt.Fprint(w, `{"id":"CANONICAL"}`)
			}
		default:
			t.Fatalf("unexpected call %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	c := newClient(srv.URL, "k")
	devices, err := c.ListDevices(context.Background())
	if err != nil || len(devices) != 1 || devices[0].DeviceID != "AAA" {
		t.Fatalf("identity depends on order: %v %v", devices, err)
	}
	if id, err := c.CanonicalID(context.Background(), "input"); err != nil || id != "CANONICAL" {
		t.Fatalf("canonical ID: %s %v", id, err)
	}
	if _, err := c.CanonicalID(context.Background(), "invalid"); err == nil {
		t.Fatal("daemon checksum rejection ignored")
	}
}

func TestDeviceObservationDistinguishesBackupMembershipAndBoundaryDrift(t *testing.T) {
	folder := BackupFolder{ID: "lnd-backup", Path: paths.LNDBackupExport, Type: "sendonly", Marker: paths.ExportReadyMarkerName,
		Devices: []json.RawMessage{json.RawMessage(`{"deviceID":"LOCAL"}`), json.RawMessage(`{"deviceID":"A"}`)}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Error("observation mutated configuration")
		}
		switch r.URL.Path {
		case "/rest/system/status":
			fmt.Fprint(w, `{"myID":"LOCAL"}`)
		case "/rest/config":
			json.NewEncoder(w).Encode(map[string]any{
				"devices": []map[string]string{{"deviceID": "B", "name": "unshared"}, {"deviceID": "LOCAL"}, {"deviceID": "A", "name": "shared"}},
				"folders": []BackupFolder{folder},
			})
		default:
			t.Errorf("devices and membership must come from one config read: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := newClient(srv.URL, "key")
	devices, err := c.ListDevices(t.Context())
	if err != nil || len(devices) != 2 {
		t.Fatalf("read failed: %v %v", devices, err)
	}
	for _, d := range devices {
		if !d.BackupKnown || d.BackupShared != (d.DeviceID == "A") {
			t.Fatalf("device existence confused with sharing: %+v", d)
		}
	}
	folder.Path = "/var/lib/lnd"
	devices, err = c.ListDevices(t.Context())
	if err != nil || len(devices) != 2 {
		t.Fatalf("folder drift hid configured devices: %v", err)
	}
	for _, d := range devices {
		if d.BackupKnown || d.BackupShared {
			t.Fatal("unsafe folder reported as configured backup sharing")
		}
	}
	for _, raw := range []string{"null", ""} {
		if _, err := parseDevices([]byte(raw), "LOCAL"); err == nil {
			t.Fatal("unknown list became zero devices")
		}
	}
}
