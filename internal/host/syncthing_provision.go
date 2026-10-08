package host

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/user"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/virtpriv/node/internal/component"
	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/logger"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/syncthing"
	"github.com/virtpriv/node/internal/system"
)

var syncthingResiduePaths = []string{
	paths.SyncthingBinary,
	paths.SyncthingDir,
	paths.SyncthingDataDir,
	paths.SyncthingService,
	paths.StateSyncthingAPIKey,
	paths.StateSyncthingWebPassword,
	paths.TorSyncthing,
	paths.TorSyncthingSync,
	paths.BackupWatchPath,
	paths.BackupExportService,
	paths.BackupCheckTimer,
	paths.LNDBackupStage,
	paths.LNDBackupExport,
}

// syncthingResiduePresent conservatively detects known add-on artifacts before
// a fresh install attempt. It does not classify, adopt, clean, or repair them.
// Any evidence causes refusal without mutation. Retained-state recovery is
// not supported.
func syncthingResiduePresent() (bool, error) {
	for _, path := range syncthingResiduePaths {
		if _, err := os.Lstat(path); err == nil {
			return true, nil
		} else if !os.IsNotExist(err) {
			return false, fmt.Errorf("inspect Syncthing residue %s: %w", path, err)
		}
	}
	if _, err := user.Lookup(syncthingUser); err == nil {
		return true, nil
	} else if _, ok := err.(user.UnknownUserError); !ok {
		return false, fmt.Errorf("inspect Syncthing user: %w", err)
	}
	if _, err := user.LookupGroup(backupGroup); err == nil {
		return true, nil
	} else if _, ok := err.(user.UnknownGroupError); !ok {
		return false, fmt.Errorf("inspect Syncthing backup group: %w", err)
	}
	return false, nil
}

type syncthingDirSpec struct {
	path  string
	owner string
	mode  os.FileMode
}

func syncthingDirSpecs() []syncthingDirSpec {
	return []syncthingDirSpec{
		{paths.SyncthingDir,
			syncthingUser + ":" + syncthingUser, 0700},
		{paths.SyncthingDataDir,
			syncthingUser + ":" + syncthingUser, 0700},
		// The project-owned parent is the only cross-service
		// boundary. Syncthing receives group traversal here, never
		// through its own private state.
		{paths.ExportDir,
			"root:" + backupGroup, 0750},
		// Only the lnd-run publisher can enter private staging.
		{paths.LNDBackupStage,
			lndUser + ":" + lndUser, 0700},
		// The publisher owns the final send-only folder. Syncthing
		// can traverse and read through the backup group but has no
		// write bit on the directory or published file.
		{paths.LNDBackupExport,
			lndUser + ":" + backupGroup, 0750},
		// Syncthing requires a marker in every folder it serves. A
		// custom, project-owned marker lets it validate this folder
		// without receiving the write access used to create .stfolder.
		{paths.LNDBackupExportMarker,
			"root:" + backupGroup, 0750},
	}
}

func createSyncthingDirs() error {
	for _, d := range syncthingDirSpecs() {
		if err := system.RunRoot("mkdir", "-p", d.path); err != nil {
			return err
		}
		if err := system.RunRoot("chown", d.owner, d.path); err != nil {
			return err
		}
		if err := system.RunRoot("chmod",
			fmt.Sprintf("%o", d.mode), d.path); err != nil {
			return err
		}
	}
	return nil
}

// syncthingServiceUnit renders the systemd unit for the pinned
// Syncthing binary. STNOUPGRADE=1 disables the binary's
// self-upgrader (the GitHub release binary is NOT built with
// [noupgrade], verified June 9 2026: this env var plus
// autoUpgradeIntervalH=0 in the config are the two controls).
// STNODEFAULTFOLDER=1 prevents creation of the default ~/Sync
// folder on first run. --no-restart + Restart=on-failure keeps
// lifecycle ownership with systemd (existing posture).
func syncthingServiceUnit() string {
	return fmt.Sprintf(`[Unit]
Description=Syncthing File Synchronization
After=network-online.target tor.service
Wants=network-online.target

[Service]
Type=simple
User=%s
Group=%s
SupplementaryGroups=%s
Environment=STNOUPGRADE=1
Environment=STNODEFAULTFOLDER=1
ExecStart=/usr/local/bin/syncthing serve --no-browser --no-restart --config=/etc/syncthing --data=/var/lib/syncthing
Restart=on-failure
RestartSec=10
SuccessExitStatus=3 4
RestartForceExitStatus=3 4

[Install]
WantedBy=multi-user.target
`, syncthingUser, syncthingUser, backupGroup)
}

func writeSyncthingService() error {
	return system.WriteFileRoot(paths.SyncthingService,
		[]byte(syncthingServiceUnit()), 0644)
}

// initializeSyncthingConfig provisions identity and writes the complete initial
// configuration before first daemon start. It renders the pinned-version
// template with the generated identity and GUI password hash, then verifies it.
// This is initial provisioning, not a password-only update.
func initializeSyncthingConfig(password string) error {
	system.RunRootSilent("chown",
		syncthingUser+":"+syncthingUser, paths.SyncthingDir)

	// 1. Crypto identity only: TLS cert/key + device ID. The
	//    generated config.xml is read for its identity values,
	//    then overwritten by the authored template. Explicit
	//    binary path, never PATH resolution, so a leftover
	//    apt-installed /usr/bin/syncthing can never be the one
	//    that generates the identity. runuser (util-linux)
	//    drops from root to the service user. This box has no
	//    sudo rules to borrow.
	if err := system.RunRoot("runuser", "-u", syncthingUser, "--",
		"/usr/local/bin/syncthing",
		"generate", "--home="+paths.SyncthingDir); err != nil {
		return fmt.Errorf("syncthing generate: %w", err)
	}

	// 2. Extract device ID, device name, and API key from the
	//    generated config. Read by exact path: `generate` can
	//    leave its own .syncthing.tmp.* scratch alongside.
	output, err := system.RunRootOutput("cat",
		paths.SyncthingConfigXML)
	if err != nil {
		return fmt.Errorf("read generated config: %w", err)
	}

	type genDevice struct {
		ID   string `xml:"id,attr"`
		Name string `xml:"name,attr"`
	}
	type genGUI struct {
		APIKey string `xml:"apikey"`
	}
	type genConfig struct {
		XMLName xml.Name    `xml:"configuration"`
		Devices []genDevice `xml:"device"`
		GUI     genGUI      `xml:"gui"`
	}
	var gen genConfig
	if err := xml.Unmarshal([]byte(output), &gen); err != nil {
		return fmt.Errorf("parse generated config: %w", err)
	}
	if len(gen.Devices) == 0 || gen.Devices[0].ID == "" {
		return fmt.Errorf("generated config has no device ID")
	}
	if gen.GUI.APIKey == "" {
		return fmt.Errorf("generated config has no API key")
	}

	// 3. Hash the GUI password.
	hash, err := bcrypt.GenerateFromPassword(
		[]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	// 4. Author the complete config and write it atomically.
	rendered := renderSyncthingConfig(
		gen.Devices[0].ID, gen.Devices[0].Name,
		gen.GUI.APIKey, string(hash))
	if err := system.WriteFileRoot(paths.SyncthingConfigXML,
		[]byte(rendered), 0640); err != nil {
		return err
	}
	if err := system.RunRoot("chown",
		syncthingUser+":"+syncthingUser,
		paths.SyncthingConfigXML); err != nil {
		return err
	}

	// 5. Self-verify gate: the daemon must never start with a
	//    config we have not verified.
	return verifySyncthingConfig()
}

// verifySyncthingConfig is the pre-start self-verify gate.
// Gate (a): version tripwire: the installed binary must be the
// pinned version and the written config must carry the pinned
// schema version. When the pinned version is bumped in a future
// release, this fails until the template is re-reviewed against
// the new version's generate output: deliberately.
// Gate (b): field check: every privacy field must be PRESENT
// with its exact intended value, with single <gui>/<options>
// blocks and a single listen address. An absent field means the
// schema assumption broke. Refusing to start converts a silent
// leak into a loud install failure.
func verifySyncthingConfig() error {
	// (a) binary version, using the same service environment as update review.
	verOut, err := syncthingVersion()
	if err != nil {
		return fmt.Errorf("syncthing --version: %w", err)
	}
	if !strings.Contains(verOut, "syncthing v"+component.SyncthingVersion+" ") {
		return fmt.Errorf(
			"version tripwire: installed Syncthing is not the "+
				"pinned v%s: %q", component.SyncthingVersion,
			strings.TrimSpace(verOut))
	}

	// (b) written config
	content, err := system.RunRootOutput("cat",
		paths.SyncthingConfigXML)
	if err != nil {
		return fmt.Errorf("re-read config: %w", err)
	}

	if !strings.Contains(content,
		`<configuration version="`+syncthingConfigSchema+`"`) {
		return fmt.Errorf(
			"version tripwire: config schema does not match "+
				"the pinned schema version %s",
			syncthingConfigSchema)
	}

	required := []string{
		"<globalAnnounceEnabled>false</globalAnnounceEnabled>",
		"<localAnnounceEnabled>false</localAnnounceEnabled>",
		"<relaysEnabled>false</relaysEnabled>",
		"<natEnabled>false</natEnabled>",
		"<announceLANAddresses>false</announceLANAddresses>",
		"<crashReportingEnabled>false</crashReportingEnabled>",
		"<autoUpgradeIntervalH>0</autoUpgradeIntervalH>",
		"<urAccepted>-1</urAccepted>",
		"<listenAddress>tcp://0.0.0.0:22000</listenAddress>",
	}
	for _, want := range required {
		if !strings.Contains(content, want) {
			return fmt.Errorf(
				"config self-verify failed: %s missing or wrong, "+
					"refusing to start Syncthing", want)
		}
	}

	for tag, n := range map[string]int{
		"<gui ":           1,
		"<options>":       1,
		"<listenAddress>": 1,
	} {
		if got := strings.Count(content, tag); got != n {
			return fmt.Errorf(
				"config self-verify failed: %d %s blocks, want %d, "+
					"refusing to start Syncthing", got, tag, n)
		}
	}

	logger.Install("Syncthing config self-verify passed " +
		"(pinned version, schema, privacy fields)")
	return nil
}

func setupChannelBackupWatcher(cfg *config.AppConfig) error {
	network := cfg.Network
	profile, err := config.NetworkConfigFromName(network)
	if err != nil {
		return fmt.Errorf("configure LND backup publisher: %w", err)
	}

	pathUnit, exportService, checkTimer, err := channelBackupUnits(network)
	if err != nil {
		return err
	}
	if err := system.WriteFileRoot(paths.BackupWatchPath,
		[]byte(pathUnit), 0644); err != nil {
		return err
	}

	if err := system.WriteFileRoot(paths.BackupExportService,
		[]byte(exportService), 0644); err != nil {
		return err
	}
	if err := system.WriteFileRoot(paths.BackupCheckTimer,
		[]byte(checkTimer), 0644); err != nil {
		return err
	}

	if err := system.RunRoot("systemctl", "daemon-reload"); err != nil {
		return err
	}
	if err := system.RunRoot("systemctl", "enable",
		"lnd-backup-watch.path"); err != nil {
		return err
	}
	if err := system.RunRoot("systemctl", "start",
		"lnd-backup-watch.path"); err != nil {
		return err
	}

	// If a backup already exists, copy it through the same
	// least-privilege unit that handles future changes. A node
	// without a wallet/channel backup yet is a normal no-op.
	if _, err := os.Stat(paths.ChannelBackup(profile.LNDNetwork)); err == nil {
		if err := system.RunRoot("systemctl", "start",
			"lnd-backup-export.service"); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect LND channel backup: %w", err)
	}
	return system.RunRoot("systemctl", "enable", "--now",
		"lnd-backup-check.timer")
}

// channelBackupUnits renders the watcher, the timer and the only
// service allowed to cross from LND state into the project-owned export.
// The normal lnd.service has no backup-group access, and Syncthing
// has no access to /var/lib/lnd or the private staging directory.
// The service accepts only the closed network selector. Every
// filesystem path is fixed inside the publisher implementation.
//
// The watcher exports a change at once, but systemd drops its watch during
// a daemon-reload and while the export runs, so it can miss a change. The
// timer starts the same export every minute, which compares before it
// copies. The export pulls the watcher back in when it is not active.
// Changes can arrive in bursts, one per channel event, so the export has no
// start limit: hitting one would leave the watcher failed. A run that finds
// nothing to do stays out of the journal: only notices and worse are kept,
// and the publisher's own line about a real publication is a notice.
func channelBackupUnits(network string) (
	pathUnit, exportService, checkTimer string, err error,
) {
	profile, err := config.NetworkConfigFromName(network)
	if err != nil {
		return "", "", "", fmt.Errorf(
			"render LND backup units: %w", err)
	}
	backupSource := paths.ChannelBackup(profile.LNDNetwork)
	pathUnit = fmt.Sprintf(`[Unit]
Description=Watch LND channel backup

[Path]
PathChanged=%s
Unit=lnd-backup-export.service

[Install]
WantedBy=multi-user.target
`, backupSource)

	exportService = fmt.Sprintf(`[Unit]
Description=Export LND channel backup
Wants=lnd-backup-watch.path
StartLimitIntervalSec=0

[Service]
Type=oneshot
User=%s
Group=%s
SupplementaryGroups=%s
UMask=0027
SyslogLevel=notice
LogLevelMax=notice
ExecStart=%s publish-lnd-backup %s
`, lndUser, lndUser, backupGroup, paths.BinaryPath, network)

	checkTimer = `[Unit]
Description=Check the LND channel backup copy every minute

[Timer]
OnCalendar=minutely
AccuracySec=1s
Unit=lnd-backup-export.service

[Install]
WantedBy=timers.target
`
	return pathUnit, exportService, checkTimer, nil
}

// backupExportTimeout bounds one export requested by VPN itself. The
// publisher copies one small file.
const backupExportTimeout = 30 * time.Second

var (
	backupExportUnitPath = paths.BackupExportService
	startBackupExport    = func() error {
		_, err := system.RunRootOutputWithTimeout(backupExportTimeout,
			"systemctl", "start", "--no-ask-password",
			"lnd-backup-export.service")
		return err
	}
)

// RefreshLNDBackupExport runs one export after VPN reloaded systemd at a
// moment when LND writes its backup, because the watcher cannot see a change
// made during the reload. A node without the Syncthing add-on has no export.
func RefreshLNDBackupExport() error {
	if _, err := os.Lstat(backupExportUnitPath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect LND backup export unit: %w", err)
	}
	return startBackupExport()
}

var syncthingServiceCommand = system.RunRoot

// stopSyncthingFailSafe attempts both stop and disable so an unverified daemon
// cannot silently return on reboot. Failures remain explicit. Retained residue
// is not repaired by retrying installation.
func stopSyncthingFailSafe() error {
	stopErr := syncthingServiceCommand("systemctl", "stop", "syncthing")
	disableErr := syncthingServiceCommand("systemctl", "disable", "syncthing")
	return errors.Join(stopErr, disableErr)
}

func failSyncthingPrivacy(cause error) error {
	if err := stopSyncthingFailSafe(); err != nil {
		return errors.Join(cause, fmt.Errorf("could not confirm Syncthing stop/disable, administrator action is required: %w", err))
	}
	return fmt.Errorf("syncthing stopped and disabled: %w", cause)
}

func startSyncthing() error {
	if err := system.RunRoot("systemctl", "daemon-reload"); err != nil {
		return err
	}
	if err := system.RunRoot("systemctl", "enable", "syncthing"); err != nil {
		return err
	}
	if err := system.RunRoot("systemctl", "start", "syncthing"); err != nil {
		return failSyncthingPrivacy(err)
	}

	// Health and privacy reads use the same loopback-only transport as runtime.
	ready := false
	probe := syncthing.NewClient("")
	for i := 0; i < 30; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, err := probe.Request(ctx, http.MethodGet, "/rest/noauth/health", "")
		cancel()
		if err == nil {
			ready = true
			break
		}
		time.Sleep(1 * time.Second)
	}
	if !ready {
		return failSyncthingPrivacy(errors.New("syncthing did not become ready"))
	}

	// Verify the running daemon accepted the authored privacy settings.
	return verifySyncthingPrivacyOrStop()
}

// verifySyncthingPrivacyOrStop checks effective network, reporting and GUI
// settings after startup. Any error attempts to stop and disable the service.
// Failures of those protective actions are included in the returned error.
func verifySyncthingPrivacyOrStop() error {
	apiKey, err := SyncthingAPIKey()
	if err == nil {
		err = syncthing.NewClient(apiKey).ConfirmPrivacy(context.Background())
	}
	if err != nil {
		return failSyncthingPrivacy(fmt.Errorf("privacy confirmation failed: %w", err))
	}
	logger.Install("Syncthing privacy confirmed on running daemon")
	return nil
}

// ── Syncthing Backup Folder Registration ─────────────────

// registerBackupFolder adds the lnd-backup directory as a
// Send Only folder in Syncthing so it can be shared with
// paired devices.
func registerBackupFolder() error {
	apiKey, err := SyncthingAPIKey()
	if err != nil {
		return fmt.Errorf("get API key: %w", err)
	}
	client := syncthing.NewClient(apiKey)

	// Check for the exact folder ID. Searching the raw JSON for
	// a substring can confuse an unrelated label or longer ID
	// for the required folder.
	existing, err := client.Request(context.Background(), http.MethodGet, "/rest/config/folders", "")
	if err != nil {
		return fmt.Errorf("list folders: %w", err)
	}
	registered, err := backupFolderRegistered(existing)
	if err != nil {
		return fmt.Errorf("parse folders: %w", err)
	}
	if registered {
		return nil
	}

	// Get local device ID to include in folder config
	localID := SyncthingDeviceID()
	if localID == "" {
		return fmt.Errorf("cannot determine local device ID")
	}

	folder := renderBackupFolderConfig(localID)

	if _, err := client.Request(context.Background(), http.MethodPost, "/rest/config/folders", folder); err != nil {
		return fmt.Errorf("register folder: %w", err)
	}

	logger.Install("Registered lnd-backup folder in Syncthing")
	return nil
}

func renderBackupFolderConfig(localID string) string {
	return fmt.Sprintf(`{
        "id": "lnd-backup",
        "label": "LND Channel Backup",
        "path": %q,
        "type": "sendonly",
        "markerName": %q,
        "rescanIntervalS": 10,
        "fsWatcherEnabled": true,
        "fsWatcherDelayS": 1,
        "devices": [{"deviceID": %q}]
    }`, paths.LNDBackupExport, paths.ExportReadyMarkerName, localID)
}

func backupFolderRegistered(foldersJSON string) (bool, error) {
	var folders []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(foldersJSON), &folders); err != nil {
		return false, err
	}
	for _, folder := range folders {
		if folder.ID == "lnd-backup" {
			return true, nil
		}
	}
	return false, nil
}
