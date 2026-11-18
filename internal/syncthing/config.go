package syncthing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
