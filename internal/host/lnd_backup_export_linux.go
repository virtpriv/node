//go:build linux

package host

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"os/user"
	"path"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/paths"
)

const (
	backupFileName   = "channel.backup"
	maxTempNameTries = 16
)

// PublishLNDBackup is the internal service operation used by
// lnd-backup-export.service. The caller selects only a supported
// Bitcoin network; all filesystem paths and identity names are
// fixed here. The process must already be the lnd service user
// with the unit-local vpn-lnd-backup supplementary group.
func PublishLNDBackup(network string) error {
	profile, err := config.NetworkConfigFromName(network)
	if err != nil {
		return err
	}
	ids, err := resolveBackupPublisherIdentity()
	if err != nil {
		return err
	}
	if err := verifyBackupPublisherCaller(ids); err != nil {
		return err
	}
	return publishLNDBackup(
		productionBackupPublisherSpec(profile.LNDNetwork, ids), ids,
		publisherHooks{},
	)
}

type backupPublisherIdentity struct {
	lndUID    int
	lndGID    int
	backupGID int
}

func resolveBackupPublisherIdentity() (backupPublisherIdentity, error) {
	lndAccount, err := user.Lookup(lndUser)
	if err != nil {
		return backupPublisherIdentity{}, fmt.Errorf(
			"resolve %s service account: %w", lndUser, err)
	}
	lndUID, err := strconv.Atoi(lndAccount.Uid)
	if err != nil {
		return backupPublisherIdentity{}, fmt.Errorf(
			"parse %s uid %q: %w", lndUser, lndAccount.Uid, err)
	}
	lndGID, err := strconv.Atoi(lndAccount.Gid)
	if err != nil {
		return backupPublisherIdentity{}, fmt.Errorf(
			"parse %s gid %q: %w", lndUser, lndAccount.Gid, err)
	}
	backupAccount, err := user.LookupGroup(backupGroup)
	if err != nil {
		return backupPublisherIdentity{}, fmt.Errorf(
			"resolve %s group: %w", backupGroup, err)
	}
	backupGID, err := strconv.Atoi(backupAccount.Gid)
	if err != nil {
		return backupPublisherIdentity{}, fmt.Errorf(
			"parse %s gid %q: %w",
			backupGroup, backupAccount.Gid, err)
	}
	return backupPublisherIdentity{
		lndUID: lndUID, lndGID: lndGID, backupGID: backupGID,
	}, nil
}

func verifyBackupPublisherCaller(ids backupPublisherIdentity) error {
	if os.Geteuid() != ids.lndUID || os.Getegid() != ids.lndGID {
		return fmt.Errorf(
			"must run as %s:%s via lnd-backup-export.service "+
				"(effective uid:gid is %d:%d)",
			lndUser, lndUser, os.Geteuid(), os.Getegid())
	}
	groups, err := os.Getgroups()
	if err != nil {
		return fmt.Errorf("read publisher supplementary groups: %w", err)
	}
	for _, gid := range groups {
		if gid == ids.backupGID {
			return nil
		}
	}
	return fmt.Errorf(
		"missing unit-local supplementary group %s",
		backupGroup)
}

type publisherMetadataPolicy struct {
	uid       int
	gid       int
	exactMode os.FileMode
	exact     bool
}

type backupPublisherSpec struct {
	root          string
	rootPolicy    publisherMetadataPolicy
	sourceDir     string
	stageDir      string
	finalDir      string
	dirPolicies   map[string]publisherMetadataPolicy
	sourceDisplay string
	stageDisplay  string
	finalDisplay  string
}

func productionBackupPublisherSpec(
	network string, ids backupPublisherIdentity,
) backupPublisherSpec {
	source := paths.ChannelBackup(network)
	spec := backupPublisherSpec{
		root: "/",
		rootPolicy: publisherMetadataPolicy{
			uid: 0, gid: 0, exactMode: 0755, exact: true,
		},
		sourceDir:     strings.TrimPrefix(path.Dir(source), "/"),
		stageDir:      strings.TrimPrefix(paths.LNDBackupStage, "/"),
		finalDir:      strings.TrimPrefix(paths.LNDBackupExport, "/"),
		dirPolicies:   make(map[string]publisherMetadataPolicy),
		sourceDisplay: source,
		stageDisplay:  paths.LNDBackupStage,
		finalDisplay:  paths.LNDBackupExport + "/" + backupFileName,
	}
	rootSafe := publisherMetadataPolicy{uid: 0, gid: 0}
	lndSafe := publisherMetadataPolicy{uid: ids.lndUID, gid: ids.lndGID}
	spec.dirPolicies["var"] = rootSafe
	spec.dirPolicies["var/lib"] = rootSafe
	spec.dirPolicies["var/lib/lnd"] = publisherMetadataPolicy{
		uid: ids.lndUID, gid: ids.lndGID,
		exactMode: 0750, exact: true,
	}
	for current := "var/lib/lnd"; current != spec.sourceDir; {
		remainder := strings.TrimPrefix(spec.sourceDir, current+"/")
		next := strings.SplitN(remainder, "/", 2)[0]
		current = path.Join(current, next)
		spec.dirPolicies[current] = lndSafe
	}
	spec.dirPolicies["var/lib/vpn"] = rootSafe
	spec.dirPolicies["var/lib/vpn/exports"] = publisherMetadataPolicy{
		uid: 0, gid: ids.backupGID,
		exactMode: 0750, exact: true,
	}
	spec.dirPolicies[spec.stageDir] = publisherMetadataPolicy{
		uid: ids.lndUID, gid: ids.lndGID,
		exactMode: 0700, exact: true,
	}
	spec.dirPolicies[spec.finalDir] = publisherMetadataPolicy{
		uid: ids.lndUID, gid: ids.backupGID,
		exactMode: 0750, exact: true,
	}
	return spec
}

// publisherHooks are test-only fault seams. Production passes the zero value;
// there is deliberately no environment variable or command-line failure hook.
type publisherHooks struct {
	fail     func(string) error
	tempName func() (string, error)
}

func (h publisherHooks) check(point string) error {
	if h.fail == nil {
		return nil
	}
	return h.fail(point)
}

func (h publisherHooks) nextTempName() (string, error) {
	if h.tempName != nil {
		return h.tempName()
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate temporary name: %w", err)
	}
	return ".channel.backup.tmp-" + hex.EncodeToString(random[:]), nil
}

const secureResolve = unix.RESOLVE_BENEATH |
	unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_SYMLINKS

func publishLNDBackup(
	spec backupPublisherSpec, ids backupPublisherIdentity,
	hooks publisherHooks,
) (retErr error) {
	rootFD, err := unix.Open(
		spec.root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open publisher root %s: %w", spec.root, err)
	}
	defer unix.Close(rootFD)
	if err := validateDirectoryFD(
		rootFD, spec.root, spec.rootPolicy); err != nil {
		return err
	}

	if err := hooks.check("open-source"); err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	sourceDirFD, err := openValidatedDirectory(
		rootFD, spec.sourceDir, spec.dirPolicies)
	if err != nil {
		return fmt.Errorf("open source ancestry %s: %w",
			spec.sourceDisplay, err)
	}
	defer unix.Close(sourceDirFD)
	// A FIFO must reach the regular-file check without waiting for a writer.
	sourceFD, err := openAtNoSymlinks(
		sourceDirFD, backupFileName,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("open source %s: %w", spec.sourceDisplay, err)
	}
	source := os.NewFile(uintptr(sourceFD), spec.sourceDisplay)
	sourceOpen := true
	defer func() {
		if sourceOpen {
			if err := source.Close(); err != nil && retErr == nil {
				retErr = fmt.Errorf("close source: %w", err)
			}
		}
	}()

	if err := hooks.check("source-metadata-verify"); err != nil {
		return fmt.Errorf("verify source metadata: %w", err)
	}
	var sourceInitial unix.Stat_t
	if err := unix.Fstat(sourceFD, &sourceInitial); err != nil {
		return fmt.Errorf("stat source %s: %w", spec.sourceDisplay, err)
	}
	if err := validateSourceStat(
		&sourceInitial, ids, spec.sourceDisplay); err != nil {
		return err
	}

	if err := hooks.check("open-stage"); err != nil {
		return fmt.Errorf("open private stage: %w", err)
	}
	stageFD, err := openValidatedDirectory(
		rootFD, spec.stageDir, spec.dirPolicies)
	if err != nil {
		return fmt.Errorf("open private stage %s: %w",
			spec.stageDisplay, err)
	}
	defer unix.Close(stageFD)
	if err := hooks.check("lock-stage"); err != nil {
		return fmt.Errorf("lock private stage: %w", err)
	}
	if err := unix.Flock(stageFD, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) {
			return errors.New("another LND backup publisher is running")
		}
		return fmt.Errorf("lock private stage: %w", err)
	}
	defer unix.Flock(stageFD, unix.LOCK_UN)

	if err := hooks.check("open-final-directory"); err != nil {
		return fmt.Errorf("open final export: %w", err)
	}
	finalFD, err := openValidatedDirectory(
		rootFD, spec.finalDir, spec.dirPolicies)
	if err != nil {
		return fmt.Errorf("open final export %s: %w",
			spec.finalDisplay, err)
	}
	defer unix.Close(finalFD)
	var stageStat, finalDirStat unix.Stat_t
	if err := unix.Fstat(stageFD, &stageStat); err != nil {
		return fmt.Errorf("stat private stage: %w", err)
	}
	if err := unix.Fstat(finalFD, &finalDirStat); err != nil {
		return fmt.Errorf("stat final export: %w", err)
	}
	if stageStat.Dev != finalDirStat.Dev {
		return errors.New(
			"private stage and final export are not on the same filesystem")
	}

	if err := hooks.check("destination-metadata-verify"); err != nil {
		return fmt.Errorf("verify existing destination: %w", err)
	}
	if err := validateExistingDestination(
		finalFD, ids, spec.finalDisplay); err != nil {
		return err
	}

	var temp *os.File
	var tempName string
	tempOwned := false
	defer func() {
		if temp != nil {
			if err := temp.Close(); err != nil && retErr == nil {
				retErr = fmt.Errorf("close temporary: %w", err)
			}
		}
		if !tempOwned {
			return
		}
		cleanupErr := hooks.check("cleanup")
		if cleanupErr == nil {
			cleanupErr = unix.Unlinkat(stageFD, tempName, 0)
			if errors.Is(cleanupErr, unix.ENOENT) {
				cleanupErr = nil
			}
		}
		if cleanupErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf(
				"clean up owned temporary %s/%s: %w",
				spec.stageDisplay, tempName, cleanupErr))
		}
	}()

	for attempt := 0; attempt < maxTempNameTries; attempt++ {
		tempName, err = hooks.nextTempName()
		if err != nil {
			return err
		}
		if path.Base(tempName) != tempName ||
			!strings.HasPrefix(tempName, ".channel.backup.tmp-") {
			return fmt.Errorf("unsafe temporary name %q", tempName)
		}
		if err := hooks.check("create-temp"); err != nil {
			return fmt.Errorf("create temporary: %w", err)
		}
		fd, openErr := openAtNoSymlinks(
			stageFD, tempName,
			unix.O_RDWR|unix.O_CLOEXEC|unix.O_CREAT|
				unix.O_EXCL|unix.O_NOFOLLOW,
			0600,
		)
		if errors.Is(openErr, unix.EEXIST) {
			continue
		}
		if openErr != nil {
			return fmt.Errorf("create exclusive temporary: %w", openErr)
		}
		temp = os.NewFile(uintptr(fd),
			path.Join(spec.stageDisplay, tempName))
		tempOwned = true
		break
	}
	if temp == nil {
		return fmt.Errorf(
			"could not create an exclusive temporary after %d attempts",
			maxTempNameTries)
	}

	copiedHash := sha256.New()
	reader := &publisherFaultReader{
		reader: source, hooks: hooks, point: "source-read",
	}
	writer := &publisherFaultWriter{
		writer: io.MultiWriter(temp, copiedHash),
		hooks:  hooks, point: "temp-write",
	}
	copied, err := io.Copy(writer, reader)
	if err != nil {
		return fmt.Errorf("copy source to private temporary: %w", err)
	}
	if copied != sourceInitial.Size {
		return fmt.Errorf(
			"source size changed during publication: copied %d, expected %d",
			copied, sourceInitial.Size)
	}

	if err := hooks.check("temp-chown"); err != nil {
		return fmt.Errorf("set temporary group: %w", err)
	}
	if err := unix.Fchown(int(temp.Fd()), -1, ids.backupGID); err != nil {
		return fmt.Errorf("set temporary group: %w", err)
	}
	if err := hooks.check("temp-chmod"); err != nil {
		return fmt.Errorf("set temporary mode: %w", err)
	}
	if err := unix.Fchmod(int(temp.Fd()), 0640); err != nil {
		return fmt.Errorf("set temporary mode: %w", err)
	}
	if err := hooks.check("temp-metadata-verify"); err != nil {
		return fmt.Errorf("verify temporary metadata: %w", err)
	}
	var tempStat unix.Stat_t
	if err := unix.Fstat(int(temp.Fd()), &tempStat); err != nil {
		return fmt.Errorf("stat temporary: %w", err)
	}
	if err := validatePublishedStat(
		&tempStat, ids, "private temporary"); err != nil {
		return err
	}
	if tempStat.Size != copied {
		return fmt.Errorf(
			"temporary size is %d, copied %d", tempStat.Size, copied)
	}

	if err := hooks.check("source-stability"); err != nil {
		return fmt.Errorf("verify source stability: %w", err)
	}
	if err := verifySourceSnapshot(
		sourceFD, &sourceInitial, spec.sourceDisplay); err != nil {
		return err
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind source: %w", err)
	}
	recheckHash := sha256.New()
	rechecked, err := io.Copy(recheckHash, &publisherFaultReader{
		reader: source, hooks: hooks, point: "source-reread",
	})
	if err != nil {
		return fmt.Errorf("re-read source: %w", err)
	}
	if rechecked != copied ||
		!hashEqual(copiedHash, recheckHash) {
		return errors.New("source changed while it was being published")
	}
	if err := verifySourceSnapshot(
		sourceFD, &sourceInitial, spec.sourceDisplay); err != nil {
		return err
	}
	if err := hooks.check("source-path-verify"); err != nil {
		return fmt.Errorf("verify source pathname: %w", err)
	}
	if err := verifySourcePath(
		sourceDirFD, &sourceInitial, ids, spec.sourceDisplay); err != nil {
		return err
	}

	if err := hooks.check("temp-sync"); err != nil {
		return fmt.Errorf("sync temporary: %w", err)
	}
	if err := temp.Sync(); err != nil {
		return fmt.Errorf("sync temporary: %w", err)
	}
	if err := temp.Close(); err != nil {
		temp = nil
		return fmt.Errorf("close temporary: %w", err)
	}
	temp = nil
	if err := hooks.check("temp-close"); err != nil {
		return fmt.Errorf("close temporary: %w", err)
	}
	if err := source.Close(); err != nil {
		sourceOpen = false
		return fmt.Errorf("close source: %w", err)
	}
	sourceOpen = false
	if err := hooks.check("source-close"); err != nil {
		return fmt.Errorf("close source: %w", err)
	}

	if err := hooks.check("before-rename"); err != nil {
		return fmt.Errorf("before atomic publication: %w", err)
	}
	if err := hooks.check("rename"); err != nil {
		return fmt.Errorf("publish by atomic rename: %w", err)
	}
	if err := unix.Renameat(
		stageFD, tempName, finalFD, backupFileName); err != nil {
		return fmt.Errorf("publish by atomic rename: %w", err)
	}
	tempOwned = false

	if err := hooks.check("open-final"); err != nil {
		return fmt.Errorf("open published backup: %w", err)
	}
	// Refuse a FIFO substituted after rename without waiting for a writer.
	publishedFD, err := openAtNoSymlinks(
		finalFD, backupFileName,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("open published backup: %w", err)
	}
	published := os.NewFile(uintptr(publishedFD), spec.finalDisplay)
	if err := hooks.check("final-verify"); err != nil {
		published.Close()
		return fmt.Errorf("verify published backup: %w", err)
	}
	var publishedStat unix.Stat_t
	if err := unix.Fstat(publishedFD, &publishedStat); err != nil {
		published.Close()
		return fmt.Errorf("stat published backup: %w", err)
	}
	if err := validatePublishedStat(
		&publishedStat, ids, spec.finalDisplay); err != nil {
		published.Close()
		return err
	}
	finalHash := sha256.New()
	finalBytes, err := io.Copy(finalHash, &publisherFaultReader{
		reader: published, hooks: hooks, point: "final-read",
	})
	if err != nil {
		published.Close()
		return fmt.Errorf("read published backup: %w", err)
	}
	if finalBytes != copied || !hashEqual(copiedHash, finalHash) {
		published.Close()
		return errors.New("published backup does not match opened source")
	}
	if err := published.Close(); err != nil {
		return fmt.Errorf("close published backup: %w", err)
	}
	if err := hooks.check("final-close"); err != nil {
		return fmt.Errorf("close published backup: %w", err)
	}
	if err := hooks.check("final-dir-sync"); err != nil {
		return fmt.Errorf("sync final export directory: %w", err)
	}
	if err := unix.Fsync(finalFD); err != nil {
		return fmt.Errorf("sync final export directory: %w", err)
	}
	if err := hooks.check("stage-dir-sync"); err != nil {
		return fmt.Errorf("sync private stage directory: %w", err)
	}
	if err := unix.Fsync(stageFD); err != nil {
		return fmt.Errorf("sync private stage directory: %w", err)
	}
	return nil
}

func openValidatedDirectory(
	rootFD int, relative string,
	policies map[string]publisherMetadataPolicy,
) (int, error) {
	if relative == "" || path.IsAbs(relative) ||
		path.Clean(relative) != relative {
		return -1, fmt.Errorf("unsafe directory path %q", relative)
	}
	currentFD, err := unix.Dup(rootFD)
	if err != nil {
		return -1, err
	}
	current := ""
	for _, component := range strings.Split(relative, "/") {
		current = path.Join(current, component)
		policy, ok := policies[current]
		if !ok {
			unix.Close(currentFD)
			return -1, fmt.Errorf(
				"no metadata policy for %s", current)
		}
		nextFD, openErr := openAtNoSymlinks(
			currentFD, component,
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|
				unix.O_NOFOLLOW,
			0,
		)
		unix.Close(currentFD)
		if openErr != nil {
			return -1, fmt.Errorf("open %s: %w", current, openErr)
		}
		if err := validateDirectoryFD(nextFD, current, policy); err != nil {
			unix.Close(nextFD)
			return -1, err
		}
		currentFD = nextFD
	}
	return currentFD, nil
}

func openAtNoSymlinks(
	dirFD int, name string, flags int, mode uint64,
) (int, error) {
	return unix.Openat2(dirFD, name, &unix.OpenHow{
		Flags: uint64(flags), Mode: mode, Resolve: secureResolve,
	})
}

func validateDirectoryFD(
	fd int, display string, policy publisherMetadataPolicy,
) error {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return fmt.Errorf("stat directory %s: %w", display, err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("%s is not a directory", display)
	}
	if int(st.Uid) != policy.uid || int(st.Gid) != policy.gid {
		return fmt.Errorf(
			"directory %s has uid:gid %d:%d, want %d:%d",
			display, st.Uid, st.Gid, policy.uid, policy.gid)
	}
	mode := os.FileMode(st.Mode & 0777)
	if policy.exact {
		if mode != policy.exactMode {
			return fmt.Errorf(
				"directory %s has mode %04o, want %04o",
				display, mode, policy.exactMode)
		}
	} else if mode&0022 != 0 || mode&0100 == 0 {
		return fmt.Errorf(
			"directory %s has unsafe mode %04o", display, mode)
	}
	return nil
}

func validateSourceStat(
	st *unix.Stat_t, ids backupPublisherIdentity, display string,
) error {
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return fmt.Errorf("source %s is not a regular file", display)
	}
	if st.Nlink != 1 {
		return fmt.Errorf(
			"source %s has %d links, want 1", display, st.Nlink)
	}
	if int(st.Uid) != ids.lndUID || int(st.Gid) != ids.lndGID {
		return fmt.Errorf(
			"source %s has uid:gid %d:%d, want %d:%d",
			display, st.Uid, st.Gid, ids.lndUID, ids.lndGID)
	}
	if mode := os.FileMode(st.Mode & 0777); mode&0022 != 0 {
		return fmt.Errorf(
			"source %s has unsafe writable mode %04o", display, mode)
	}
	return nil
}

func validatePublishedStat(
	st *unix.Stat_t, ids backupPublisherIdentity, display string,
) error {
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return fmt.Errorf("%s is not a regular file", display)
	}
	if st.Nlink != 1 {
		return fmt.Errorf("%s has %d links, want 1", display, st.Nlink)
	}
	if int(st.Uid) != ids.lndUID || int(st.Gid) != ids.backupGID {
		return fmt.Errorf(
			"%s has uid:gid %d:%d, want %d:%d",
			display, st.Uid, st.Gid, ids.lndUID, ids.backupGID)
	}
	if mode := os.FileMode(st.Mode & 0777); mode != 0640 {
		return fmt.Errorf(
			"%s has mode %04o, want 0640", display, mode)
	}
	return nil
}

func validateExistingDestination(
	finalFD int, ids backupPublisherIdentity, display string,
) error {
	var st unix.Stat_t
	err := unix.Fstatat(
		finalFD, backupFileName, &st, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect existing destination %s: %w", display, err)
	}
	if err := validatePublishedStat(&st, ids, display); err != nil {
		return fmt.Errorf(
			"refusing to replace malformed destination: %w", err)
	}
	return nil
}

func verifySourceSnapshot(
	fd int, initial *unix.Stat_t, display string,
) error {
	var current unix.Stat_t
	if err := unix.Fstat(fd, &current); err != nil {
		return fmt.Errorf("re-stat source %s: %w", display, err)
	}
	if !sameSourceSnapshot(initial, &current) {
		return fmt.Errorf("source %s changed during publication", display)
	}
	return nil
}

func verifySourcePath(
	dirFD int, opened *unix.Stat_t, ids backupPublisherIdentity,
	display string,
) error {
	var current unix.Stat_t
	if err := unix.Fstatat(
		dirFD, backupFileName, &current,
		unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("re-stat source path %s: %w", display, err)
	}
	if err := validateSourceStat(&current, ids, display); err != nil {
		return err
	}
	if current.Dev != opened.Dev || current.Ino != opened.Ino ||
		!sameSourceSnapshot(opened, &current) {
		return fmt.Errorf("source path %s was replaced during publication", display)
	}
	return nil
}

func sameSourceSnapshot(a, b *unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino &&
		a.Size == b.Size && a.Mode == b.Mode &&
		a.Uid == b.Uid && a.Gid == b.Gid && a.Nlink == b.Nlink &&
		a.Mtim == b.Mtim && a.Ctim == b.Ctim
}

func hashEqual(a, b hash.Hash) bool {
	return string(a.Sum(nil)) == string(b.Sum(nil))
}

type publisherFaultReader struct {
	reader io.Reader
	hooks  publisherHooks
	point  string
	failed bool
}

func (r *publisherFaultReader) Read(p []byte) (int, error) {
	if !r.failed {
		r.failed = true
		if err := r.hooks.check(r.point); err != nil {
			return 0, err
		}
	}
	return r.reader.Read(p)
}

type publisherFaultWriter struct {
	writer io.Writer
	hooks  publisherHooks
	point  string
	failed bool
}

func (w *publisherFaultWriter) Write(p []byte) (int, error) {
	if !w.failed {
		w.failed = true
		if err := w.hooks.check(w.point); err != nil {
			return 0, err
		}
	}
	return w.writer.Write(p)
}
