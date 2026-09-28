package update

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/virtualprivatenode/vpn/internal/config"
	"github.com/virtualprivatenode/vpn/internal/host"
	"github.com/virtualprivatenode/vpn/internal/paths"
	"github.com/virtualprivatenode/vpn/internal/release"
	"github.com/virtualprivatenode/vpn/internal/system"
	"github.com/virtualprivatenode/vpn/internal/update/files"
	"github.com/virtualprivatenode/vpn/internal/update/protocol"
)

type prepared struct {
	Review     protocol.Review `json:"review"`
	WorkerHash string          `json:"worker_hash"`
	ConfigHash string          `json:"config_hash"`
	Previous   string          `json:"previous"`
}

func approvalToken(p prepared) (string, error) {
	p.Review.Token = ""
	b, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

func readPrepared(root, token string) (prepared, error) {
	var p prepared
	if !protocol.ValidDigest(token) {
		return p, errors.New("invalid review token")
	}
	if err := readJSON(filepath.Join(root, "reviews", token+".json"), &p); err != nil {
		return p, err
	}
	want, err := approvalToken(p)
	if err != nil || want != token || p.Review.Token != token || !protocol.ValidDigest(p.Review.Digest) {
		return p, errors.New("review record identity mismatch")
	}
	return p, nil
}

func requireUpdateHost() error {
	if os.Geteuid() != 0 {
		return errors.New("managed updates require the root helper")
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return errors.New("managed updates require Debian 13 amd64")
	}
	b, err := os.ReadFile(paths.OSRelease)
	if err != nil {
		return err
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if ok {
			values[k] = strings.Trim(v, "\"")
		}
	}
	if values["ID"] != "debian" || values["VERSION_ID"] != "13" {
		return errors.New("managed updates require Debian 13")
	}
	// Internal worker commands must not create an apparent installation on an
	// uninstalled host. Configuration admission precedes directory creation.
	if _, err := config.Load(); err != nil {
		return err
	}
	return files.Directory(Root, 0700)
}

// Prepare authenticates the release before showing its plan. It never stops a
// service or executes the candidate. The approval identifies the archive bytes.
func Prepare(current, version string) (protocol.Review, error) {
	var none protocol.Review
	if err := requireUpdateHost(); err != nil {
		return none, err
	}
	newer, err := release.Newer(current, version)
	if err != nil || !newer {
		return none, errors.New("select a newer VPN release")
	}
	unlock, err := files.Lock(LockPath)
	if err != nil {
		return none, err
	}
	defer unlock()
	previous, err := loadJob(Root)
	if err != nil {
		return none, err
	}
	failed := ""
	if previous != nil && previous.active() {
		if previous.Phase != "failed" {
			return none, errors.New("finish or resume the current update first")
		}
		failed = previous.Review.Digest
	}
	cfg, err := config.Load()
	if err != nil {
		return none, err
	}
	source, err := host.ObserveUpdateVersions(current, cfg)
	if err != nil {
		return none, err
	}
	work, err := os.MkdirTemp(Root, "prepare-")
	if err != nil {
		return none, err
	}
	defer os.RemoveAll(work)
	archive, err := release.ArchiveName(version)
	if err != nil {
		return none, err
	}
	base := "https://github.com/virtualprivatenode/vpn/releases/download/v" + version + "/"
	for _, name := range []string{archive, "SHA256SUMS", "SHA256SUMS.asc"} {
		if err := system.DownloadRequireTor(base+name, filepath.Join(work, name)); err != nil {
			return none, err
		}
	}
	if err := release.VerifySignature(work); err != nil {
		return none, err
	}
	if err := release.VerifyChecksum(version, work); err != nil {
		return none, err
	}
	if err := host.ExtractUpdateArchive(filepath.Join(work, archive), work, map[string]string{"vpn": "vpn", "update.json": "update.json"}); err != nil {
		return none, fmt.Errorf("release needs a managed update plan: %w", err)
	}
	data, err := os.ReadFile(filepath.Join(work, "update.json"))
	if err != nil {
		return none, err
	}
	m, err := protocol.Decode(data)
	if err != nil {
		return none, err
	}
	if m.Version != version {
		return none, errors.New("signed plan version differs from selected release")
	}
	if err := m.Admit(source, cfg.Network, failed); err != nil {
		return none, err
	}
	id, err := files.Hash(filepath.Join(work, archive))
	if err != nil {
		return none, err
	}
	workerHash, err := files.Hash(filepath.Join(work, "vpn"))
	if err != nil {
		return none, err
	}
	configHash, err := files.Hash(paths.ConfigFile)
	if err != nil {
		return none, err
	}
	p := prepared{Review: protocol.Review{Digest: id, Manifest: m, Source: source, Network: cfg.Network}, WorkerHash: workerHash, ConfigHash: configHash, Previous: failed}
	p.Review.Token, err = approvalToken(p)
	if err != nil {
		return none, err
	}
	if err := writeJSON(filepath.Join(work, "prepared.json"), p); err != nil {
		return none, err
	}
	dest := filepath.Join(Root, id)
	if _, err := os.Lstat(dest); err == nil {
		// Same bytes may be reviewed again, but cannot overwrite a job's
		// retained artifacts or recovery evidence.
		var existing prepared
		if err := readJSON(filepath.Join(dest, "prepared.json"), &existing); err != nil {
			return none, err
		}
		if existing.WorkerHash != workerHash {
			return none, errors.New("retained release differs from verified download")
		}
		if err := verifyHash(filepath.Join(dest, "vpn"), workerHash); err != nil {
			return none, err
		}
	} else if os.IsNotExist(err) {
		if err := os.Rename(work, dest); err != nil {
			return none, err
		}
		if err := files.Sync(Root); err != nil {
			return none, err
		}
	} else {
		return none, err
	}
	if err := files.Directory(filepath.Join(Root, "reviews"), 0700); err != nil {
		return none, err
	}
	if err := writeJSON(filepath.Join(Root, "reviews", p.Review.Token+".json"), p); err != nil {
		return none, err
	}
	return p.Review, nil
}

func Start(current, id string) (protocol.Status, error) {
	if err := requireUpdateHost(); err != nil {
		return protocol.Status{}, err
	}
	if !protocol.ValidDigest(id) {
		return protocol.Status{}, errors.New("invalid reviewed release")
	}
	unlock, err := files.Lock(LockPath)
	if err != nil {
		return protocol.Status{}, err
	}
	err = startLocked(current, id)
	unlock()
	if err != nil {
		return protocol.Status{}, err
	}
	if err := startWorker(); err != nil {
		return protocol.Status{}, err
	}
	return Status()
}

type nodeObservation struct {
	source     protocol.Versions
	config     *config.AppConfig
	configHash string
}

// Admission owns the approved record and its durable successor. Host reads and
// launcher installation are separate boundaries, called under the mutation lock.
type admissionOps struct {
	observe  func() (nodeObservation, error)
	identify func(*job, *config.AppConfig) error
	launcher func() error
}

func startLocked(current, token string) error {
	return acceptUpdate(Root, token, admissionOps{
		observe: func() (nodeObservation, error) {
			cfg, err := config.Load()
			if err != nil {
				return nodeObservation{}, err
			}
			source, err := host.ObserveUpdateVersions(current, cfg)
			if err != nil {
				return nodeObservation{}, err
			}
			hash, err := files.Hash(paths.ConfigFile)
			return nodeObservation{source: source, config: cfg, configHash: hash}, err
		},
		identify: recordUpdateIdentity,
		launcher: func() error {
			// Retain the working executable as the boot/recovery launcher.
			self, err := os.Executable()
			if err != nil {
				return err
			}
			if err := files.Copy(self, filepath.Join(Root, "launcher"), 0700); err != nil {
				return err
			}
			return installWorkerUnit()
		},
	})
}

func recordUpdateIdentity(j *job, cfg *config.AppConfig) error {
	for _, c := range j.Affected {
		if err := host.CheckUpdateProcess(c); err != nil {
			return fmt.Errorf("start %s before updating, or use the saved recovery job: %w", c, err)
		}
	}
	if slices.Contains(j.Affected, protocol.LND) {
		var err error
		j.WalletPresent, err = host.WalletExists(cfg.Network)
		if err != nil {
			return err
		}
	}
	if slices.Contains(j.Affected, protocol.Syncthing) {
		j.SyncthingID = host.SyncthingDeviceID()
		if j.SyncthingID == "" {
			return errors.New("cannot establish Syncthing device identity")
		}
	}
	return nil
}

func acceptUpdate(root, token string, ops admissionOps) error {
	p, err := readPrepared(root, token)
	if err != nil {
		return err
	}
	id := p.Review.Digest
	previous, err := loadJob(root)
	if err != nil {
		return err
	}
	if previous != nil && previous.active() && previous.Review.Digest == id {
		return errors.New("this update is already accepted; use its saved status")
	}
	failed := ""
	if previous != nil && previous.active() {
		if previous.Phase != "failed" {
			return errors.New("another update is unfinished")
		}
		failed = previous.Review.Digest
	}
	if p.Previous != failed {
		return errors.New("update state changed; review the release again")
	}
	node, err := ops.observe()
	if err != nil {
		return err
	}
	source, cfg, configHash := node.source, node.config, node.configHash
	if source != p.Review.Source || configHash != p.ConfigHash {
		return errors.New("node changed since review; review the release again")
	}
	if failed != "" && (cfg.Network != previous.Review.Network || configHash != previous.ConfigHash) {
		return errors.New("node configuration changed during the failed update; resolve that change before recovery")
	}
	if err := p.Review.Manifest.Admit(source, cfg.Network, failed); err != nil {
		return err
	}
	if err := verifyHash(filepath.Join(root, id, "vpn"), p.WorkerHash); err != nil {
		return err
	}
	j := &job{Schema: 1, Review: p.Review, WorkerHash: p.WorkerHash, ConfigHash: configHash, Phase: "accepted", Step: "Update accepted", Started: map[protocol.Component]bool{}, MayHaveRun: map[protocol.Component]bool{}, Completed: map[string]bool{}, BinaryHashes: map[string]string{}, Affected: p.Review.Manifest.Affected(source)}
	j.Previous = failed
	if failed != "" {
		j.WalletPresent, j.SyncthingID = previous.WalletPresent, previous.SyncthingID
		for _, c := range protocol.Components {
			if slices.Contains(previous.Affected, c) && !slices.Contains(j.Affected, c) {
				j.Affected = append(j.Affected, c)
			}
		}
		slices.SortFunc(j.Affected, func(a, b protocol.Component) int {
			return slices.Index(protocol.Components, a) - slices.Index(protocol.Components, b)
		})
		for c, v := range previous.MayHaveRun {
			j.MayHaveRun[c] = v
		}
	} else if err := ops.identify(j, cfg); err != nil {
		return err
	}

	if previous != nil {
		if err := writeJSON(filepath.Join(previous.dir(root), "result.json"), previous); err != nil {
			return err
		}
	}
	if err := ops.launcher(); err != nil {
		return err
	}
	return saveJob(root, j)
}

func installWorkerUnit() error {
	const body = "[Unit]\nDescription=VPN managed update\nAfter=local-fs.target network-online.target tor.service\nWants=network-online.target\n\n[Service]\nType=exec\nExecStart=" + Root + "/launcher update-launch\nRestart=no\nUMask=0077\nPrivateTmp=yes\nProtectHome=read-only\n\n[Install]\nWantedBy=multi-user.target\n"
	if err := files.Write("/etc/systemd/system/"+Service, []byte(body), 0644); err != nil {
		return err
	}
	if err := system.RunRoot("systemctl", "daemon-reload"); err != nil {
		return err
	}
	return system.RunRoot("systemctl", "enable", Service)
}

func startWorker() error { return system.RunRoot("systemctl", "start", "--no-block", Service) }

func Resume(id string) (protocol.Status, error) {
	if err := requireUpdateHost(); err != nil {
		return protocol.Status{}, err
	}
	unlock, err := files.Lock(LockPath)
	if err != nil {
		return protocol.Status{}, err
	}
	err = retryUpdate(Root, id)
	unlock()
	if err != nil {
		return protocol.Status{}, err
	}
	if err := startWorker(); err != nil {
		return protocol.Status{}, err
	}
	return Status()
}

// retryUpdate records explicit consent to start again without erasing evidence
// that an earlier invocation may already have migrated component data.
func retryUpdate(root, id string) error {
	j, err := loadJob(root)
	if err != nil {
		return err
	}
	if j == nil || !j.active() || j.Review.Digest != id {
		return errors.New("update identity changed; refresh its status")
	}
	j.Error = ""
	j.Started = map[protocol.Component]bool{}
	if j.Completed["staged"] {
		j.Phase = "installing"
	} else {
		j.Phase = "accepted"
	}
	return saveJob(root, j)
}

func Status() (protocol.Status, error) {
	if os.Geteuid() != 0 {
		return protocol.Status{}, errors.New("update status requires the root helper")
	}
	j, err := loadJob(Root)
	if err != nil || j == nil {
		return protocol.Status{}, err
	}
	s := protocol.Status{ID: j.Review.Digest, Version: j.Review.Manifest.Version, Phase: j.Phase, Step: j.Step, Error: j.Error, Active: j.active()}
	s.Running = system.IsServiceActive(Service)
	s.Cancellable = j.active() && !j.Completed["staged"] && j.Previous == "" && !s.Running
	if j.Phase == "waiting-unlock" {
		p, err := config.NetworkConfigFromName(j.Review.Network)
		if err != nil {
			return s, err
		}
		s.WalletNetwork = p.LNDNetwork
	}
	return s, nil
}

func verifyHash(path, want string) error {
	got, err := files.Hash(path)
	if err != nil {
		return err
	}
	if !protocol.ValidDigest(want) || got != want {
		return errors.New("retained update file failed integrity verification")
	}
	return nil
}

// Cancel releases maintenance admission only before host changes can begin.
// Downloaded artifacts and the terminal record remain available for inspection.
func Cancel(id string) (protocol.Status, error) {
	if err := requireUpdateHost(); err != nil {
		return protocol.Status{}, err
	}
	unlock, err := files.Lock(LockPath)
	if err != nil {
		return protocol.Status{}, err
	}
	defer unlock()
	j, err := loadJob(Root)
	if err != nil {
		return protocol.Status{}, err
	}
	if j == nil || j.Review.Digest != id {
		return protocol.Status{}, errors.New("update identity changed; refresh its status")
	}
	if err := cancelJob(j); err != nil {
		return protocol.Status{}, err
	}
	if err := saveJob(Root, j); err != nil {
		return protocol.Status{}, err
	}
	return Status()
}

func cancelJob(j *job) error {
	if !j.active() || j.Completed["staged"] || j.Previous != "" {
		return errors.New("host changes may have begun; complete or repair this update")
	}
	j.Phase = "cancelled"
	j.Step = "Update cancelled before host changes"
	j.Error = ""
	return nil
}
