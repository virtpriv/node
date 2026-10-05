package syncthing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"

	"github.com/virtpriv/node/internal/paths"
)

// ConfirmPrivacy requires explicit fields; missing values are not evidence of
// disabled discovery or reporting. It observes configuration without repairing it.
func (c *Client) ConfirmPrivacy(ctx context.Context) error {
	var opts map[string]json.RawMessage
	if err := c.read(ctx, "/rest/config/options", &opts); err != nil {
		return err
	}
	for _, key := range []string{"globalAnnounceEnabled", "localAnnounceEnabled", "relaysEnabled", "natEnabled", "announceLANAddresses", "crashReportingEnabled"} {
		var value *bool
		if json.Unmarshal(opts[key], &value) != nil || value == nil || *value {
			return fmt.Errorf("syncthing privacy setting %s is missing or enabled", key)
		}
	}
	for key, want := range map[string]int{"autoUpgradeIntervalH": 0, "urAccepted": -1} {
		var value *int
		if json.Unmarshal(opts[key], &value) != nil || value == nil || *value != want {
			return fmt.Errorf("syncthing privacy setting %s is missing or changed", key)
		}
	}
	var addresses []string
	if json.Unmarshal(opts["listenAddresses"], &addresses) != nil || len(addresses) != 1 || addresses[0] != "tcp://0.0.0.0:22000" {
		return errors.New("syncthing sync listener is not the expected direct TCP listener")
	}
	var gui struct {
		Address  string `json:"address"`
		Enabled  *bool  `json:"enabled"`
		TLS      *bool  `json:"useTLS"`
		User     string `json:"user"`
		Password string `json:"password"`
		Metrics  *bool  `json:"metricsWithoutAuth"`
	}
	if err := c.read(ctx, "/rest/config/gui", &gui); err != nil {
		return err
	}
	if gui.Address != "127.0.0.1:8384" || gui.Enabled == nil || !*gui.Enabled || gui.TLS == nil || *gui.TLS || gui.User == "" || gui.Password == "" || gui.Metrics == nil || *gui.Metrics {
		return errors.New("syncthing Web UI privacy/authentication configuration has changed")
	}
	return nil
}

// BackupFolder keeps unmodeled per-device fields intact when PATCH replaces the
// devices array. In particular, encryption passwords and introduction metadata
// must survive pairing another receiver.
type BackupFolder struct {
	ID      string            `json:"id"`
	Path    string            `json:"path"`
	Type    string            `json:"type"`
	Marker  string            `json:"markerName"`
	Devices []json.RawMessage `json:"devices"`
}

func (f BackupFolder) HasDevice(id string) bool {
	for _, raw := range f.Devices {
		var d struct {
			ID string `json:"deviceID"`
		}
		if json.Unmarshal(raw, &d) == nil && d.ID == id {
			return true
		}
	}
	return false
}
func (c *Client) BackupFolder(ctx context.Context, localID string) (BackupFolder, error) {
	var f BackupFolder
	if err := c.read(ctx, "/rest/config/folders/lnd-backup", &f); err != nil {
		return f, err
	}
	return f, f.validate(localID)
}

func (f BackupFolder) validate(localID string) error {
	if f.ID != "lnd-backup" || filepath.Clean(f.Path) != paths.LNDBackupExport || f.Type != "sendonly" || f.Marker != paths.ExportReadyMarkerName || !f.HasDevice(localID) {
		return errors.New("backup folder boundary differs from the expected send-only export; maintenance inspection is required")
	}
	for _, raw := range f.Devices {
		var d struct {
			ID string `json:"deviceID"`
		}
		if json.Unmarshal(raw, &d) != nil || d.ID == "" {
			return errors.New("invalid backup folder device entry")
		}
	}
	return nil
}
func (c *Client) ShareBackup(ctx context.Context, f BackupFolder, id string) error {
	if f.HasDevice(id) {
		return nil
	}
	raw, _ := json.Marshal(struct {
		ID string `json:"deviceID"`
	}{id})
	devices := append(append([]json.RawMessage(nil), f.Devices...), raw)
	body, err := json.Marshal(struct {
		Devices []json.RawMessage `json:"devices"`
	}{devices})
	if err != nil {
		return err
	}
	_, err = c.Request(ctx, http.MethodPatch, "/rest/config/folders/lnd-backup", string(body))
	return err
}
func (c *Client) AddDevice(ctx context.Context, id string) error {
	// Explicitly disable trust-expanding defaults even if Web UI defaults changed.
	body, _ := json.Marshal(map[string]any{"deviceID": id, "name": "local-backup", "addresses": []string{"dynamic"}, "autoAcceptFolders": false, "introducer": false, "skipIntroductionRemovals": false, "introducedBy": ""})
	_, err := c.Request(ctx, http.MethodPost, "/rest/config/devices", string(body))
	return err
}
func (c *Client) RemoveDevice(ctx context.Context, id string) error {
	_, err := c.Request(ctx, http.MethodDelete, "/rest/config/devices/"+url.PathEscape(id), "")
	return err
}
