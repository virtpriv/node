package syncthing

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// HTTP rejection must never become a successful device-management result.
func TestSyncthingAPI(t *testing.T) {
	var gotMethod, gotKey, gotType, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			gotMethod = r.Method
			gotKey = r.Header.Get("X-API-Key")
			gotType = r.Header.Get("Content-Type")
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
			switch r.URL.Path {
			case "/ok":
				w.Write([]byte(`{"ok":true}`))
			case "/empty":
				w.WriteHeader(http.StatusNoContent)
			case "/forbidden":
				w.WriteHeader(http.StatusForbidden)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
	defer srv.Close()
	client := newClient(srv.URL, "key123")

	// Success: body through, headers set, request body sent.
	body, err := client.Request(context.Background(), "POST", "/ok", `{"a":1}`)
	if err != nil {
		t.Fatalf("POST /ok: %v", err)
	}
	if body != `{"ok":true}` {
		t.Errorf("POST /ok body: got %q", body)
	}
	if gotMethod != "POST" || gotKey != "key123" ||
		gotType != "application/json" || gotBody != `{"a":1}` {
		t.Errorf("request not as sent: method=%q key=%q type=%q body=%q",
			gotMethod, gotKey, gotType, gotBody)
	}

	// A bodyless 2xx (some DELETE/POST answers) is success.
	if _, err := client.Request(context.Background(),
		"DELETE", "/empty", ""); err != nil {
		t.Errorf("DELETE /empty: %v", err)
	}
	if gotBody != "" || gotType != "" {
		t.Errorf("empty-body request carried body=%q type=%q",
			gotBody, gotType)
	}

	// The load-bearing case: an HTTP error IS an error, and a
	// 403 names the API-key remedy.
	_, err = client.Request(context.Background(), "GET", "/forbidden", "")
	if err == nil {
		t.Fatal("403 answer reported as success")
	}
	if !strings.Contains(err.Error(), "HTTP 403") ||
		!strings.Contains(err.Error(), "API key") {
		t.Errorf("403 error lacks status or hint: %v", err)
	}

	// Any other error status also fails, naming the call.
	_, err = client.Request(context.Background(), "GET", "/missing", "")
	if err == nil {
		t.Fatal("404 answer reported as success")
	}
	if !strings.Contains(err.Error(), "HTTP 404") ||
		!strings.Contains(err.Error(), "GET /missing") {
		t.Errorf("404 error lacks status or call name: %v", err)
	}
}

// A daemon that is not there is a transport error, not a
// silent success.
func TestSyncthingAPIDaemonDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // deliberately: the address now refuses
	client := newClient(srv.URL, "key123")

	if _, err := client.Request(context.Background(), "GET", "/ok", ""); err == nil {
		t.Error("unreachable daemon reported as success")
	}
}

func TestParseSyncthingDevicesUsesCurrentDaemonFacts(t *testing.T) {
	raw := []byte(`[
  {"deviceID":"LOCAL","name":"this node"},
  {"deviceID":"B","name":" Laptop "},
  {"deviceID":"A","name":""},
  {"deviceID":"B","name":"stale duplicate"},
  {"deviceID":" ","name":"invalid"}
]`)
	devices, err := parseDevices(raw, "LOCAL")
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 2 {
		t.Fatalf("devices = %+v", devices)
	}
	if devices[0].DeviceID != "B" || devices[0].Name != "Laptop" ||
		devices[1].DeviceID != "A" || devices[1].Name != "Syncthing device" {
		t.Fatalf("unexpected current device view: %+v", devices)
	}
	if _, err := parseDevices([]byte(`{}`), "LOCAL"); err == nil {
		t.Fatal("malformed device list accepted")
	}
}

// Delivery is judged only from what Syncthing knows now. A device that is not
// connected is never called up to date, whatever it held when last seen.
func TestBackupDeliveryStates(t *testing.T) {
	failDevice, localNeed := "", "0"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/system/connections":
			w.Write([]byte(`{"connections":{
				"PHONE":{"connected":true},"OFF":{"connected":false},"NEW":{"connected":true},
				"PAUSED":{"connected":false,"paused":true},"SLOW":{"connected":true},
				"DEL":{"connected":true},"IDX":{"connected":true},"OTHER":{"connected":true}},
				"total":{}}`))
		case "/rest/stats/device":
			w.Write([]byte(`{"OFF":{"lastSeen":"2026-10-08T13:55:00Z"},"PHONE":{"lastSeen":"2026-10-08T14:00:00Z"}}`))
		case "/rest/db/completion":
			device := r.URL.Query().Get("device")
			if r.URL.Query().Get("folder") != "lnd-backup" || (device != "" && device == failDevice) {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			if device == "" {
				w.Write([]byte(`{"completion":100,"needBytes":0,"needItems":` + localNeed + `,"needDeletes":0,"remoteState":"valid"}`))
				return
			}
			w.Write([]byte(map[string]string{
				"PHONE": `{"completion":100,"needBytes":0,"needItems":0,"needDeletes":0,"remoteState":"valid"}`,
				"OFF":   `{"completion":100,"needBytes":0,"needItems":0,"needDeletes":0,"remoteState":"unknown"}`,
				"NEW":   `{"completion":0,"needBytes":45,"needItems":1,"needDeletes":0,"remoteState":"notSharing"}`,
				"SLOW":  `{"completion":40.5,"needBytes":30,"needItems":1,"needDeletes":0,"remoteState":"valid"}`,
				"DEL":   `{"completion":95,"needBytes":0,"needItems":0,"needDeletes":1,"remoteState":"valid"}`,
				"IDX":   `{"completion":100,"needBytes":0,"needItems":0,"needDeletes":0,"remoteState":"unknown"}`,
			}[device]))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	client := newClient(srv.URL, "key")
	var devices []Device
	for _, id := range []string{"PHONE", "OFF", "NEW", "PAUSED", "SLOW", "DEL", "IDX"} {
		devices = append(devices, Device{DeviceID: id, BackupKnown: true, BackupShared: true})
	}
	devices = append(devices, Device{DeviceID: "OTHER", BackupKnown: true})

	got, err := client.BackupDelivery(context.Background(), devices)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]DeliveryState{
		"PHONE": DeliveryUpToDate, "OFF": DeliveryNotConnected, "NEW": DeliveryNotAccepted,
		"PAUSED": DeliveryPaused, "SLOW": DeliveryReceiving, "DEL": DeliveryReceiving,
		"IDX": DeliveryChecking, "OTHER": DeliveryNotShared,
	}
	for id, state := range want {
		if got[id].State != state {
			t.Errorf("%s: state %v, want %v", id, got[id].State, state)
		}
	}
	if got["SLOW"].Percent != 40 {
		t.Errorf("receiving percent %d, want 40", got["SLOW"].Percent)
	}
	if !got["OFF"].LastSeen.Equal(time.Date(2026, 10, 8, 13, 55, 0, 0, time.UTC)) {
		t.Errorf("last seen %v", got["OFF"].LastSeen)
	}

	// A device changed the file. The node's send only copy is then not the
	// newest version, and a device matching that newer version does not hold
	// the node's backup.
	localNeed = "1"
	got, err = client.BackupDelivery(context.Background(), devices)
	if err != nil {
		t.Fatal(err)
	}
	for id, state := range map[string]DeliveryState{
		"PHONE": DeliveryOutOfSync, "SLOW": DeliveryOutOfSync, "DEL": DeliveryOutOfSync,
		"OFF": DeliveryNotConnected, "NEW": DeliveryNotAccepted, "OTHER": DeliveryNotShared,
	} {
		if got[id].State != state {
			t.Errorf("node behind, %s: state %v, want %v", id, got[id].State, state)
		}
	}
	localNeed = "0"

	failDevice = "SLOW"
	if got, err := client.BackupDelivery(context.Background(), devices); err == nil {
		t.Fatalf("a failed read gave a partial answer: %v", got)
	}
	failDevice = ""
	devices[0].BackupKnown = false
	if got, err := client.BackupDelivery(context.Background(), devices); err == nil {
		t.Fatalf("unknown folder membership gave an answer: %v", got)
	}
}
