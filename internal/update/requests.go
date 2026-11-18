package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/host"
	"github.com/virtpriv/node/internal/logger"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/release"
	"github.com/virtpriv/node/internal/system"
	"github.com/virtpriv/node/internal/update/files"
	"github.com/virtpriv/node/internal/update/protocol"
)

type prepared struct {
	Review     protocol.Review `json:"review"`
	WorkerHash string          `json:"worker_hash"`
	PlanHash   string          `json:"plan_hash"`
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
	if err := readJSON(filepath.Join(root, "reviews", token+".json"), &p); errors.Is(err, os.ErrNotExist) {
		return p, errors.New("this review is no longer saved, review the release again")
	} else if err != nil {
		return p, err
	}
	want, err := approvalToken(p)
	if err != nil || want != token || p.Review.Token != token || !protocol.ValidDigest(p.Review.Digest) {
		return p, errors.New("review record identity mismatch")
	}
	return p, nil
}

func requireRoot() error {
	if os.Geteuid() != 0 {
		return errors.New("managed updates require root")
	}
	return nil
}

// requireUpdateHost gates the start of an update and the worker. The launcher,
// Retry and Cancel act on a job that already exists and only require root: a
// later worker may change the system or the configuration layout part way
// through, and it must still be handed the job afterwards.
func requireUpdateHost() error {
	if err := requireRoot(); err != nil {
		return err
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
// It runs as a job of the vpn API and does not hold the operations lock: its
// unfinished record keeps every other change out, Start included.
func Prepare(current, version string) (protocol.Review, error) {
	var none protocol.Review
	if err := requireUpdateHost(); err != nil {
		return none, err
	}
	newer, err := release.Newer(current, version)
	if err != nil || !newer {
		return none, errors.New("select a newer VPN release")
	}
	previous, err := loadRecord(Root)
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
	// A new review replaces every earlier one.
	onRecord := ""
	if previous != nil {
		onRecord = previous.Review.Digest
	}
	removeEarlierReviews(Root, onRecord)
	source := protocol.Versions{VPN: current}
	work, err := os.MkdirTemp(Root, "prepare-")
	if err != nil {
		return none, err
	}
	defer os.RemoveAll(work)
	assets, err := release.AssetsFor(version)
	if err != nil {
		return none, err
	}
	archive := assets.Archive
	// The owner's files for this version are used in place of a download,
	// and get every check a download gets.
	fromFolder, folderKey, err := copyFromReleasesFolder(version, work)
	if err != nil {
		return none, err
	}
	if !fromFolder {
		base := "https://github.com/virtpriv/node/releases/download/v" + version + "/"
		for _, name := range []string{archive, assets.Manifest, assets.Signature} {
			if err := system.DownloadRequireTor(base+name, filepath.Join(work, name)); err != nil {
				return none, err
			}
		}
	}
	// A file the owner is still copying fails a check, and the refusal says so.
	stillCopying := func(err error) error {
		if (fromFolder && !errors.Is(err, release.ErrKeyUnavailable)) || errors.Is(err, release.ErrFolderKeyUnavailable) {
			return fmt.Errorf("%w. If a file is still copying, try again when it ends.", err)
		}
		return err
	}
	keyCheck, err := release.VerifySignature(version, work, folderKey)
	if err != nil {
		return none, stillCopying(err)
	}
	if err := release.VerifyChecksum(version, work); err != nil {
		return none, stillCopying(err)
	}
	if err := host.ExtractUpdateArchive(filepath.Join(work, archive), work, map[string]string{"vpn": "vpn", "update.json": "update.json"}); err != nil {
		return none, stillCopying(fmt.Errorf("release needs a managed update plan: %w", err))
	}
	data, err := os.ReadFile(filepath.Join(work, "update.json"))
	if err != nil {
		return none, err
	}
	m, first, err := protocol.Admission(data, version, current, source, failed)
	if err != nil {
		return none, err
	}
	if first != "" {
		return protocol.Review{UpdateFirst: first}, nil
	}
	planHash, err := files.Hash(filepath.Join(work, "update.json"))
	if err != nil {
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
	p := prepared{Review: protocol.Review{Digest: id, Manifest: m, Source: source, KeyServerUnreachable: keyCheck.WebsiteUnreachable, FromFolder: fromFolder}, WorkerHash: workerHash, PlanHash: planHash, ConfigHash: configHash, Previous: failed}
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

// Start accepts a reviewed update as the Node Update job jobID. busy is the
// vpn API's busy rule for other jobs, checked under the operations lock.
func Start(current, id, jobID string, busy func() error) (protocol.Status, error) {
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
	err = busy()
	if err == nil {
		err = startLocked(current, id, jobID)
	}
	unlock()
	if err != nil {
		return protocol.Status{}, err
	}
	if err := startWorker(); err != nil {
		return protocol.Status{}, err
	}
	return Status()
}

// ReadReview reads a review Prepare saved, for the screen that shows it.
func ReadReview(token string) (protocol.Review, error) {
	if err := requireRoot(); err != nil {
		return protocol.Review{}, err
	}
	p, err := readPrepared(Root, token)
	return p.Review, err
}

type nodeObservation struct {
	source     protocol.Versions
	configHash string
}

// Admission owns the approved record and its durable successor. Host reads and
// launcher installation are separate boundaries, called under the mutation lock.
type admissionOps struct {
	observe  func() (nodeObservation, error)
	launcher func() error
}

func startLocked(current, token, jobID string) error {
	return acceptUpdate(Root, token, jobID, admissionOps{
		observe: func() (nodeObservation, error) {
			if _, err := config.Load(); err != nil {
				return nodeObservation{}, err
			}
			hash, err := files.Hash(paths.ConfigFile)
			return nodeObservation{source: protocol.Versions{VPN: current}, configHash: hash}, err
		},
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

func acceptUpdate(root, token, jobID string, ops admissionOps) error {
	p, err := readPrepared(root, token)
	if err != nil {
		return err
	}
	id := p.Review.Digest
	previous, err := loadRecord(root)
	if err != nil {
		return err
	}
	if previous != nil && previous.active() && previous.Review.Digest == id {
		return errors.New("this update is already accepted, use its saved status")
	}
	failed := ""
	if previous != nil && previous.active() {
		if previous.Phase != "failed" {
			return errors.New("another update is unfinished")
		}
		failed = previous.Review.Digest
	}
	if p.Previous != failed {
		return errors.New("update state changed, review the release again")
	}
	node, err := ops.observe()
	if err != nil {
		return err
	}
	source, configHash := node.source, node.configHash
	if source != p.Review.Source || configHash != p.ConfigHash {
		return errors.New("node changed since review, review the release again")
	}
	if failed != "" && configHash != previous.ConfigHash {
		return errors.New("node configuration changed during the failed update, resolve that change before recovery")
	}
	if err := p.Review.Manifest.Admit(source, failed); err != nil {
		return err
	}
	if err := verifyHash(filepath.Join(root, id, "vpn"), p.WorkerHash); err != nil {
		return err
	}
	if err := verifyHash(filepath.Join(root, id, "update.json"), p.PlanHash); err != nil {
		return err
	}
	j := &job{Schema: jobSchema, Review: p.Review, WorkerHash: p.WorkerHash, PlanHash: p.PlanHash, ConfigHash: configHash, Phase: "accepted", Step: "Update accepted", JobID: jobID, Accepted: time.Now().UTC()}
	// The job record keeps the same fields as before: every later worker has
	// to read it, and the notice has served its purpose once approved.
	j.Review.KeyServerUnreachable = false
	j.Previous = failed
	if failed != "" {
		// A repair keeps the failed update's cancellation boundary. Its worker
		// can read anything else it needs from the record retained below, which
		// a later worker may have written in a form the installed side cannot
		// interpret.
		j.HostChanges = previous.HostChanges
	}

	if previous != nil {
		if err := files.Copy(filepath.Join(root, "current.json"), filepath.Join(previous.dir(root), "result.json"), 0600); err != nil {
			return err
		}
	}
	if err := ops.launcher(); err != nil {
		return err
	}
	return saveJob(root, j)
}

func installWorkerUnit() error {
	const body = "[Unit]\nDescription=VPN managed update\nAfter=local-fs.target network-online.target tor@default.service\nWants=network-online.target\n\n[Service]\nType=exec\nExecStart=" + Root + "/launcher update-launch\nRestart=no\nUMask=0077\nPrivateTmp=yes\nProtectHome=read-only\n\n[Install]\nWantedBy=multi-user.target\n"
	if err := files.Write("/etc/systemd/system/"+Service, []byte(body), 0644); err != nil {
		return err
	}
	if err := system.RunRoot("systemctl", "daemon-reload"); err != nil {
		return err
	}
	return system.RunRoot("systemctl", "enable", Service)
}

func startWorker() error { return system.RunRoot("systemctl", "start", "--no-block", Service) }

// workerRuns reports whether the update's unit runs or is about to. systemd
// calls a unit activating while it starts its program, and keeps a job on a
// unit whose start waits for another unit, Tor for one. Both end in a
// running worker with no step from the owner. A unit that someone stops has
// a job too, and its worker runs until the stop ends. A state that cannot be
// read counts as not running.
func workerRuns() bool {
	units, err := system.ReadUnitProperties(context.Background(), []string{Service}, "ActiveState", "Job")
	return err == nil && unitRuns(units[Service])
}

// unitRuns reports the same from systemd's record of the unit, its
// ActiveState and Job.
func unitRuns(unit map[string]string) bool {
	switch unit["ActiveState"] {
	case "active", "activating", "reloading", "refreshing":
		return true
	}
	return unit["Job"] != ""
}

// Resume records consent to retry. busy is the vpn API's busy rule for other
// jobs, checked under the operations lock.
func Resume(id string, busy func() error) (protocol.Status, error) {
	if err := requireRoot(); err != nil {
		return protocol.Status{}, err
	}
	unlock, err := files.Lock(LockPath)
	if err != nil {
		return protocol.Status{}, err
	}
	err = busy()
	if err == nil {
		err = retryUpdate(Root, id)
	}
	unlock()
	if err != nil {
		return protocol.Status{}, err
	}
	if err := startWorker(); err != nil {
		return protocol.Status{}, err
	}
	return Status()
}

// retryUpdate records explicit consent to try again. The worker decides where
// to resume.
func retryUpdate(root, id string) error {
	j, err := loadRecord(root)
	if err != nil {
		return err
	}
	if j == nil || !j.active() || j.Review.Digest != id {
		return errors.New("update identity changed, refresh its status")
	}
	return amendRecord(root, map[string]any{"phase": "retry", "error": nil, "next": nil, "advice": nil})
}

func Status() (protocol.Status, error) {
	if err := requireRoot(); err != nil {
		return protocol.Status{}, err
	}
	// A node with no update on record asks systemd nothing.
	if first, err := loadRecord(Root); err != nil || first == nil {
		return protocol.Status{}, err
	}
	// The unit is read before the record that is reported. A worker that ends
	// between the two reads is then seen with the end it saved, not as an
	// unfinished update that does not run.
	running := workerRuns()
	j, err := loadRecord(Root)
	if err != nil || j == nil {
		return protocol.Status{}, err
	}
	s := j.status()
	s.Running = running
	s.Cancellable = j.active() && !j.HostChanges && !s.Running
	return s, nil
}

// status is what the saved record tells the screen.
func (r *record) status() protocol.Status {
	s := protocol.Status{ID: r.Review.Digest, JobID: r.JobID, Version: r.Review.Manifest.Version, Phase: r.Phase, Step: r.Step, Error: r.Error, Active: r.active()}
	// A list the screen cannot draw sensibly is left out, and the screen
	// shows the step text instead. The screen scrolls, so a list may be of
	// any length. Times are passed only in a shape the screen can show.
	var steps []string
	var number int
	if json.Unmarshal(r.Steps, &steps) == nil && json.Unmarshal(r.StepNumber, &number) == nil &&
		number >= 1 && number <= len(steps) {
		s.Steps, s.StepNumber = steps, number
		var starts, ends []time.Time
		if json.Unmarshal(r.StepStarts, &starts) == nil && readableTimes(starts, len(steps), time.Now()) {
			s.StepStarts = starts
			if json.Unmarshal(r.StepEnds, &ends) == nil && readableEnds(ends, starts, time.Now()) {
				s.StepEnds = ends
			}
		}
	}
	// The next step and advice pass only with the error they explain, and only
	// in a shape this release can read, so later values reach the screen,
	// which ignores a next step it does not know.
	var next protocol.Next
	if r.Error != "" && json.Unmarshal(r.Next, &next) == nil && next.Readable() {
		s.Next = next
	}
	var advice string
	if r.Error != "" && json.Unmarshal(r.Advice, &advice) == nil && protocol.ReadableAdvice(advice) {
		s.Advice = advice
	}
	return s
}

// readableEnds reports whether each end belongs to a step that began, is
// not before its start and not ahead of the node's clock. A zero end is a
// step that has not ended.
func readableEnds(ends, starts []time.Time, now time.Time) bool {
	if len(ends) == 0 || len(ends) > len(starts) {
		return false
	}
	for i, end := range ends {
		if end.IsZero() {
			continue
		}
		if starts[i].IsZero() || end.Before(starts[i]) || end.After(now.Add(time.Minute)) {
			return false
		}
	}
	return true
}

// readableTimes reports step times the screen can use: no more than the
// steps, none later than a minute from now, which allows for a clock that
// was set back a little after the worker saved them.
func readableTimes(times []time.Time, steps int, now time.Time) bool {
	if len(times) == 0 || len(times) > steps {
		return false
	}
	for _, t := range times {
		if t.After(now.Add(time.Minute)) {
			return false
		}
	}
	return true
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
	if err := requireRoot(); err != nil {
		return protocol.Status{}, err
	}
	unlock, err := files.Lock(LockPath)
	if err != nil {
		return protocol.Status{}, err
	}
	defer unlock()
	if err := cancelUpdate(Root, id); err != nil {
		return protocol.Status{}, err
	}
	return Status()
}

func cancelUpdate(root, id string) error {
	j, err := loadRecord(root)
	if err != nil {
		return err
	}
	if j == nil || j.Review.Digest != id {
		return errors.New("update identity changed, refresh its status")
	}
	if !j.active() || j.HostChanges {
		return errors.New("host changes may have begun, complete or repair this update")
	}
	return amendRecord(root, map[string]any{"phase": "canceled", "step": "Update canceled before host changes", "error": nil, "next": nil, "advice": nil, "ended": time.Now().UTC()})
}

// removeEarlierReviews removes every review file, the folder of a review
// that stopped, and every release folder that never became an update. The
// folder of the update on record and every folder holding result.json, the
// record of an earlier update, stay, since a repair reads them. Only files
// and folders vpn made are removed, never a link, and other names are left
// alone. A removal that fails is logged and the review goes on. Prepare is
// the installed release's code on a node's first update, so a later release
// keeps what it needs inside its job's folder.
func removeEarlierReviews(root, current string) {
	// Review files go first, so a review never names a folder half removed.
	// While one is left, the release folders stay too.
	reviews := filepath.Join(root, "reviews")
	reviewsLeft := false
	if err := files.Check(reviews, true); err == nil {
		reviewsLeft = !removeReviewFiles(reviews)
	} else if !errors.Is(err, os.ErrNotExist) {
		logger.System("WARNING: left %s, it is not a folder vpn made", reviews)
		reviewsLeft = true
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		logger.System("WARNING: could not list %s: %v", root, err)
	}
	removed := false
	for _, e := range entries {
		name, path := e.Name(), filepath.Join(root, e.Name())
		stopped := strings.HasPrefix(name, "prepare-")
		if !stopped && (reviewsLeft || !protocol.ValidDigest(name) || name == current) {
			continue
		}
		if err := files.Check(path, true); err != nil {
			logger.System("WARNING: left %s, it is not a folder vpn made", path)
			continue
		}
		if !stopped {
			// A folder that may hold an earlier update's record stays.
			if _, err := os.Lstat(filepath.Join(path, "result.json")); !errors.Is(err, os.ErrNotExist) {
				continue
			}
		}
		if err := os.RemoveAll(path); err != nil {
			logger.System("WARNING: could not remove %s: %v", path, err)
			continue
		}
		removed = true
		if stopped {
			logger.System("removed the folder of a review that stopped, %s", path)
		} else {
			logger.System("removed the files of an earlier review, %s", path)
		}
	}
	if removed {
		if err := files.Sync(root); err != nil {
			logger.System("WARNING: could not sync %s: %v", root, err)
		}
	}
}

// removeReviewFiles removes every review file in dir and reports whether
// none is left.
func removeReviewFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		logger.System("WARNING: could not list %s: %v", dir, err)
		return false
	}
	gone, removed := true, false
	for _, e := range entries {
		if token, ok := strings.CutSuffix(e.Name(), ".json"); !ok || !protocol.ValidDigest(token) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if err := files.Check(path, false); err != nil {
			logger.System("WARNING: left %s, it is not a file vpn made", path)
			gone = false
			continue
		}
		if err := os.Remove(path); err != nil {
			logger.System("WARNING: could not remove %s: %v", path, err)
			gone = false
			continue
		}
		removed = true
	}
	if removed {
		if err := files.Sync(dir); err != nil {
			logger.System("WARNING: could not sync %s: %v", dir, err)
			return false
		}
	}
	return gone
}
