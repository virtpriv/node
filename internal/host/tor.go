package host

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/system"
)

// BuildTorConfig generates the complete torrc content from config state.
// Pure logic: no side effects.
// Note: HiddenServiceDir paths are hardcoded strings because they are
// torrc config content read by Tor, not Go logic paths.
func BuildTorConfig(cfg *config.AppConfig) (string, error) {
	net, err := cfg.NetworkConfig()
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString("# Virtual Private Node — Tor Configuration\n")
	b.WriteString("SOCKSPort 9050\n")

	// Control port: always emitted. Two consumers: the install-path
	// Tor routing gate (torgate.go reads bootstrap progress here,
	// unconditionally) and LND's P2P onion management. Loopback-only,
	// cookie-authenticated; emitting it without LND adds no exposure.
	b.WriteString("\n# Control port (install routing gate + LND onion management)\n")
	b.WriteString("ControlPort 9051\n")
	b.WriteString("CookieAuthentication 1\n")
	b.WriteString("CookieAuthFileGroupReadable 1\n")

	b.WriteString(fmt.Sprintf(`
# Bitcoin Core P2P (static onion address for peers)
HiddenServiceDir /var/lib/tor/bitcoin-p2p/
HiddenServicePort %d 127.0.0.1:%d
`, net.P2PPort, net.P2PPort))

	if cfg.HasLND() {
		b.WriteString(`
# LND gRPC (wallet connections over Tor)
HiddenServiceDir /var/lib/tor/lnd-grpc/
HiddenServicePort 10009 127.0.0.1:10009

# LND REST (wallet connections over Tor)
HiddenServiceDir /var/lib/tor/lnd-rest/
HiddenServicePort 8080 127.0.0.1:8080
`)
	}

	if cfg.SyncthingEnabled {
		b.WriteString(`
# Syncthing web UI (Tor only, HTTP)
HiddenServiceDir /var/lib/tor/syncthing/
HiddenServicePort 8384 127.0.0.1:8384
`)
		// Sync protocol (port 22000) goes over clearnet.
		// No hidden service needed: Syncthing uses mutual TLS
		// with explicit device approval for authentication.
	}

	return b.String(), nil
}

// WriteTorConfig writes the torrc to disk.
func WriteTorConfig(cfg *config.AppConfig) error {
	content, err := BuildTorConfig(cfg)
	if err != nil {
		return err
	}
	return writeTorConfig([]byte(content))
}

func writeTorConfig(content []byte) error {
	if err := system.WriteFileRoot(paths.Torrc, content, 0640); err != nil {
		return err
	}
	return system.RunRoot("chown", "root:debian-tor", paths.Torrc)
}

func validateTorConfig(content []byte) error {
	tmp, err := os.CreateTemp("", "vpn-torrc-")
	if err != nil {
		return fmt.Errorf("create Tor validation file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return fmt.Errorf("write Tor validation file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close Tor validation file: %w", err)
	}
	if err := system.RunRoot(
		"tor",
		"--defaults-torrc", "/usr/share/tor/tor-service-defaults-torrc",
		"-f", tmpPath,
		"--RunAsDaemon", "0",
		"--verify-config",
	); err != nil {
		return fmt.Errorf("validate Tor configuration: %w", err)
	}
	return nil
}

var (
	torBinaryPresentForAddon = func() bool {
		_, err := exec.LookPath("tor")
		return err == nil
	}
	torServiceEnabledForAddon = func() bool {
		return system.RunSilent(
			"systemctl", "is-enabled", "--quiet", "tor") == nil
	}
	torServiceActiveForAddon = func() bool {
		return system.IsServiceActive("tor")
	}
	readTorConfigForAddon      = os.ReadFile
	readSyncthingOnionForAddon = os.ReadFile
	sleepForTorAddon           = time.Sleep
	validateTorConfigForAddon  = validateTorConfig
	writeTorConfigForAddon     = writeTorConfig
	runTorServiceAction        = func(action string) error {
		return system.RunRoot("systemctl", action, "tor")
	}
)

// EnableAndRestartTor belongs to initial installation. That operation owns
// establishing Tor's boot persistence as part of the base node.
func EnableAndRestartTor() error {
	if err := runTorServiceAction("enable"); err != nil {
		return err
	}
	return runTorServiceAction("restart")
}

// verifySyncthingTorPrerequisite refuses add-on mutation unless Tor is the
// installed, enabled, active base service and its project-owned torrc still
// matches the authoritative configuration. Syncthing does not silently repair
// a disabled service or overwrite unexplained base-config divergence.
func verifySyncthingTorPrerequisite(cfg *config.AppConfig) error {
	if !torBinaryPresentForAddon() {
		return fmt.Errorf("Tor is not installed — refusing Syncthing installation")
	}
	if !torServiceEnabledForAddon() {
		return fmt.Errorf("Tor is not enabled — refusing Syncthing installation")
	}
	if !torServiceActiveForAddon() {
		return fmt.Errorf("Tor is not active — refusing Syncthing installation")
	}
	current, err := readTorConfigForAddon(paths.Torrc)
	if err != nil {
		return fmt.Errorf("read current Tor configuration: %w", err)
	}
	expected, err := BuildTorConfig(cfg)
	if err != nil {
		return fmt.Errorf("build expected Tor configuration: %w", err)
	}
	if !bytes.Equal(current, []byte(expected)) {
		return fmt.Errorf(
			"Tor configuration does not match the expected base node — " +
				"refusing Syncthing installation")
	}
	return nil
}

var requireActiveFirewallForAddon = RequireActiveFirewall

// verifySyncthingInstallPrerequisites is the root helper's before-mutation
// gate. UFW and Tor must already be healthy base-node facilities; the optional
// add-on never installs, enables, or globally repairs either one.
func verifySyncthingInstallPrerequisites(cfg *config.AppConfig) error {
	if err := requireActiveFirewallForAddon(); err != nil {
		return err
	}
	if err := verifySyncthingTorPrerequisite(cfg); err != nil {
		return err
	}

	// Validate the complete proposed add-on configuration before any
	// Syncthing installation step mutates the  The transaction below
	// validates it again immediately before the authoritative write.
	proposedCfg := *cfg
	proposedCfg.SyncthingEnabled = true
	proposed, err := BuildTorConfig(&proposedCfg)
	if err != nil {
		return fmt.Errorf("build proposed Syncthing Tor configuration: %w", err)
	}
	if err := validateTorConfigForAddon([]byte(proposed)); err != nil {
		return fmt.Errorf("validate proposed Syncthing Tor configuration: %w", err)
	}
	return nil
}

func waitForSyncthingOnion() error {
	var lastErr error
	for i := 0; i < 60; i++ {
		data, err := readSyncthingOnionForAddon(
			paths.TorSyncthingHostname)
		if err == nil {
			hostname := strings.TrimSpace(string(data))
			if ValidV3OnionHostname(hostname) {
				return nil
			}
			lastErr = fmt.Errorf(
				"invalid Syncthing onion hostname %q", hostname)
		} else {
			lastErr = err
		}
		sleepForTorAddon(time.Second)
	}
	return fmt.Errorf(
		"Syncthing onion hostname was not created after Tor reload: %w",
		lastErr)
}

func rollbackSyncthingTorConfig(previous []byte) error {
	if err := writeTorConfigForAddon(previous); err != nil {
		return fmt.Errorf("restore previous Tor configuration: %w", err)
	}
	if err := runTorServiceAction("reload"); err != nil {
		return fmt.Errorf("reload restored Tor configuration: %w", err)
	}
	if !torServiceActiveForAddon() {
		return fmt.Errorf("Tor is not active after configuration rollback")
	}
	return nil
}

func syncthingTorFailure(previous []byte, cause error) error {
	if rollbackErr := rollbackSyncthingTorConfig(previous); rollbackErr != nil {
		return fmt.Errorf("apply Syncthing Tor configuration: %w; rollback failed: %v",
			cause, rollbackErr)
	}
	return fmt.Errorf(
		"apply Syncthing Tor configuration: %w; previous configuration restored",
		cause)
}

// configureAndReloadTorForSyncthing validates the complete proposed torrc,
// writes it atomically, and applies it with a SIGHUP-backed systemd reload.
// Reload preserves LND's controller connection and dynamic onion registration.
// Reload or post-reload readiness failures restore and reload the exact prior
// base config. A failure to write the proposed config returns without rollback.
// This add-on operation deliberately does not enable or restart Tor.
func configureAndReloadTorForSyncthing(cfg *config.AppConfig) error {
	baseCfg := *cfg
	baseCfg.SyncthingEnabled = false
	if err := verifySyncthingTorPrerequisite(&baseCfg); err != nil {
		return err
	}

	previous, err := readTorConfigForAddon(paths.Torrc)
	if err != nil {
		return fmt.Errorf("read Tor configuration for rollback: %w", err)
	}
	expectedBase, err := BuildTorConfig(&baseCfg)
	if err != nil {
		return fmt.Errorf("build expected base Tor configuration: %w", err)
	}
	if !bytes.Equal(previous, []byte(expectedBase)) {
		return fmt.Errorf(
			"Tor configuration changed during prerequisite validation — " +
				"refusing Syncthing installation")
	}

	proposed, err := BuildTorConfig(cfg)
	if err != nil {
		return fmt.Errorf("build Syncthing Tor configuration: %w", err)
	}
	proposedBytes := []byte(proposed)
	if err := validateTorConfigForAddon(proposedBytes); err != nil {
		return err
	}
	if err := writeTorConfigForAddon(proposedBytes); err != nil {
		return fmt.Errorf("write Syncthing Tor configuration: %w", err)
	}
	if err := runTorServiceAction("reload"); err != nil {
		return syncthingTorFailure(previous, err)
	}
	if !torServiceActiveForAddon() {
		return syncthingTorFailure(previous,
			fmt.Errorf("Tor is not active after configuration reload"))
	}
	if err := waitForSyncthingOnion(); err != nil {
		return syncthingTorFailure(previous, err)
	}
	return nil
}
