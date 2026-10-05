package host

import (
	"encoding/xml"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/system"
)

// Syncthing initializes home paths even for --version. runuser supplies HOME
// from the service account when the root helper has no login environment.
func syncthingVersion() (string, error) {
	return system.RunRootOutputWithTimeout(10*time.Second,
		"runuser", "-u", syncthingUser, "--", paths.SyncthingBinary, "--version")
}

// SyncthingDeviceID reads the certificate identity with the pinned daemon's
// read-only command. Configuration device order does not identify the local node.
// runuser supplies the service HOME required by Syncthing package initialization.
func SyncthingDeviceID() string {
	output, err := system.RunOutputWithTimeout(10*time.Second,
		"runuser", "-u", "syncthing", "--", paths.SyncthingBinary,
		"device-id", "--config="+paths.SyncthingDir,
		"--data="+paths.SyncthingDataDir)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(output)
}

// SyncthingAPIKey reads the private configuration for root-side provisioning.
// Runtime workflows read their staged credentials in the application layer.
func SyncthingAPIKey() (string, error) {
	if os.Geteuid() != 0 {
		return "", fmt.Errorf("read Syncthing API key requires root")
	}
	return syncthingAPIKeyAt(paths.SyncthingConfigXML)
}

func syncthingAPIKeyAt(path string) (string, error) {
	output, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	type guiKey struct {
		APIKey string `xml:"apikey"`
	}
	type cfgFile struct {
		XMLName xml.Name `xml:"configuration"`
		GUI     guiKey   `xml:"gui"`
	}
	var cfg cfgFile
	if err := xml.Unmarshal(output, &cfg); err != nil {
		return "", err
	}
	if cfg.GUI.APIKey == "" {
		return "", fmt.Errorf("no API key found")
	}
	return cfg.GUI.APIKey, nil
}
