package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/paths"
)

func TestSyncthingServiceIdentityBoundary(t *testing.T) {
	unit := syncthingServiceUnit()
	for _, want := range []string{
		"User=syncthing",
		"Group=syncthing",
		"SupplementaryGroups=vpn-lnd-backup",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("Syncthing unit missing %q", want)
		}
	}
	if strings.Contains(unit, "debian-tor") {
		t.Error("Syncthing unit has Tor control-cookie access")
	}
}

func TestSyncthingDirectoryIdentityBoundary(t *testing.T) {
	specs := make(map[string]syncthingDirSpec)
	for _, spec := range syncthingDirSpecs() {
		specs[spec.path] = spec
	}
	for path, want := range map[string]syncthingDirSpec{
		paths.SyncthingDir: {
			owner: "syncthing:syncthing", mode: 0700,
		},
		paths.SyncthingDataDir: {
			owner: "syncthing:syncthing", mode: 0700,
		},
		paths.ExportDir: {
			owner: "root:vpn-lnd-backup", mode: 0750,
		},
		paths.LNDBackupStage: {
			owner: "lnd:lnd", mode: 0700,
		},
		paths.LNDBackupExport: {
			owner: "lnd:vpn-lnd-backup", mode: 0750,
		},
		paths.LNDBackupExportMarker: {
			owner: "root:vpn-lnd-backup", mode: 0750,
		},
	} {
		got, ok := specs[path]
		if !ok {
			t.Errorf("missing directory spec for %s", path)
			continue
		}
		if got.owner != want.owner || got.mode != want.mode {
			t.Errorf("%s: got %s %04o, want %s %04o",
				path, got.owner, got.mode, want.owner, want.mode)
		}
	}
}

func TestChannelBackupUnitIdentityBoundary(t *testing.T) {
	source := paths.ChannelBackup("mainnet")
	pathUnit, exportUnit, _, err := channelBackupUnits("mainnet")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pathUnit, "PathChanged="+source) {
		t.Error("path unit watches the wrong source")
	}
	if !strings.Contains(pathUnit,
		"Unit=lnd-backup-export.service") {
		t.Error("path unit does not target the backup export service")
	}
	for _, want := range []string{
		"User=lnd",
		"Group=lnd",
		"SupplementaryGroups=vpn-lnd-backup",
		"UMask=0027",
		"ExecStart=" + paths.BinaryPath +
			" publish-lnd-backup mainnet",
	} {
		if !strings.Contains(exportUnit, want) {
			t.Errorf("backup export unit missing %q", want)
		}
	}
	if strings.Contains(exportUnit, "debian-tor") {
		t.Error("backup export unit has Tor control-cookie access")
	}
	for _, forbidden := range []string{
		"/usr/bin/install", "/usr/bin/mv", source,
		paths.LNDBackupStage, paths.LNDBackupExport,
	} {
		if strings.Contains(exportUnit, forbidden) {
			t.Errorf("backup unit exposes implementation path/command %q",
				forbidden)
		}
	}
	if got := strings.Count(exportUnit, "UMask=0027"); got != 1 {
		t.Errorf("backup unit has %d publisher umasks, want 1", got)
	}
	if strings.Contains(exportUnit, "UMask=0077") {
		t.Error("publisher inherited the private-daemon umask")
	}
	if _, _, _, err := channelBackupUnits("mainnet /tmp/source"); err == nil {
		t.Error("backup unit accepted a caller-supplied path as a network")
	}
}

func TestChannelBackupUnitsMapProfileToLNDState(t *testing.T) {
	for _, network := range config.SupportedNetworks() {
		profile, err := config.NetworkConfigFromName(network)
		if err != nil {
			t.Fatal(err)
		}
		pathUnit, exportUnit, _, err := channelBackupUnits(network)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(pathUnit,
			"PathChanged="+paths.ChannelBackup(profile.LNDNetwork)) {
			t.Errorf("%s watcher does not use LND %s directory",
				network, profile.LNDNetwork)
		}
		if !strings.Contains(exportUnit,
			"publish-lnd-backup "+network) {
			t.Errorf("%s publisher loses immutable profile ID", network)
		}
		if network == config.NetworkPublicSignet &&
			strings.Contains(pathUnit, "/public-signet/") {
			t.Fatal("public-signet profile used a non-existent LND directory")
		}
	}
}

func TestBackupFolderRegisteredRequiresExactID(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		want bool
	}{
		"exact":      {`[{"id":"lnd-backup","label":"Backup"}]`, true},
		"label only": {`[{"id":"documents","label":"old lnd-backup files"}]`, false},
		"longer ID":  {`[{"id":"lnd-backup-old"}]`, false},
		"absent":     {`[{"id":"documents"}]`, false},
	} {
		got, err := backupFolderRegistered(tc.body)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got != tc.want {
			t.Errorf("%s: got %v, want %v", name, got, tc.want)
		}
	}
	if _, err := backupFolderRegistered(`not json`); err == nil {
		t.Error("malformed folder response accepted")
	}
}

func TestBackupFolderConfigUsesReadOnlyProjectExport(t *testing.T) {
	var folder struct {
		ID      string `json:"id"`
		Path    string `json:"path"`
		Type    string `json:"type"`
		Marker  string `json:"markerName"`
		Devices []struct {
			DeviceID string `json:"deviceID"`
		} `json:"devices"`
	}
	if err := json.Unmarshal(
		[]byte(renderBackupFolderConfig("LOCAL-ID")), &folder); err != nil {
		t.Fatal(err)
	}
	if folder.ID != "lnd-backup" ||
		folder.Path != paths.LNDBackupExport ||
		folder.Type != "sendonly" ||
		folder.Marker != paths.ExportReadyMarkerName ||
		len(folder.Devices) != 1 ||
		folder.Devices[0].DeviceID != "LOCAL-ID" {
		t.Errorf("unexpected folder config: %+v", folder)
	}
	if strings.HasPrefix(folder.Path, paths.SyncthingDataDir+"/") {
		t.Errorf("folder path %q is below Syncthing private state",
			folder.Path)
	}
}

func TestSyncthingFailSafeAttemptsStopAndDisableAndReportsFailure(t *testing.T) {
	original := syncthingServiceCommand
	t.Cleanup(func() { syncthingServiceCommand = original })
	for _, failAt := range []string{"stop", "disable", ""} {
		var actions []string
		syncthingServiceCommand = func(name string, args ...string) error {
			if name != "systemctl" || len(args) != 2 || args[1] != "syncthing" {
				t.Fatalf("unexpected service operation %s %v", name, args)
			}
			actions = append(actions, args[0])
			if args[0] == failAt {
				return fmt.Errorf("injected %s failure", failAt)
			}
			return nil
		}
		err := failSyncthingPrivacy(errors.New("privacy mismatch"))
		if strings.Join(actions, ",") != "stop,disable" {
			t.Fatal("stop failure prevented disable attempt")
		}
		if failAt != "" && !strings.Contains(err.Error(), "administrator action is required") {
			t.Fatal("stop/disable failure reported as safely stopped:", err)
		}
		if failAt == "" && !strings.Contains(err.Error(), "stopped and disabled") {
			t.Fatal("successful fail-safe omitted:", err)
		}
	}
}

// A node without the Syncthing add-on has no export unit. Asking for an
// export there must do nothing, so an update on such a node is not disturbed.
func TestRefreshLNDBackupExportOnlyActsWhereTheExportIsInstalled(t *testing.T) {
	unit := filepath.Join(t.TempDir(), "lnd-backup-export.service")
	started := 0
	injected := errors.New("injected start failure")
	oldPath, oldStart := backupExportUnitPath, startBackupExport
	t.Cleanup(func() { backupExportUnitPath, startBackupExport = oldPath, oldStart })
	backupExportUnitPath = unit
	startBackupExport = func() error { started++; return injected }

	if err := RefreshLNDBackupExport(); err != nil || started != 0 {
		t.Fatalf("no add-on: started %d times, err=%v", started, err)
	}
	if err := os.WriteFile(unit, []byte("[Unit]\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := RefreshLNDBackupExport(); !errors.Is(err, injected) || started != 1 {
		t.Fatalf("installed add-on: started %d times, err=%v", started, err)
	}
}
