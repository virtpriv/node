// internal/installer/lifecycle.go

package installer

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/virtpriv/node/internal/paths"
)

const (
	layoutVersionContent       = "service-layout=1\n"
	maxLifecycleStateFileBytes = 64 * 1024
	ledgerBootstrapSuffix      = ".bootstrap"
	layoutBootstrapSuffix      = ".bootstrap"
)

type lifecycleDisposition int

const (
	lifecyclePristine lifecycleDisposition = iota
	lifecycleBootstrap
	lifecycleResumable
	lifecycleCompletionPending
	lifecycleCompleted
)

type lifecycleState struct {
	Disposition      lifecycleDisposition
	Ledger           *installLedger
	BootstrapContext *installContext
}

type lifecycleBootstrapState struct {
	path    string
	context installContext
}

type lifecycleDir struct {
	path string
	mode os.FileMode
}

type lifecycleFS struct {
	varLibVPN       string
	privateDir      string
	layoutVersion   string
	ledger          string
	passwordMarker  string
	bootstrapPrefix string
	bootstraps      []lifecycleBootstrapState
	ancestors       []lifecycleDir
	unmarkedPaths   []string
	conflictPaths   []string
}

type identityLookup struct {
	user  func(string) error
	group func(string) error
}

func productionLifecycleFS() lifecycleFS {
	return lifecycleFS{
		varLibVPN:       paths.VarLibVPN,
		privateDir:      paths.PrivateDir,
		layoutVersion:   paths.LayoutVersion,
		ledger:          paths.InstallStateFile,
		passwordMarker:  paths.PasswordPendingMarker,
		bootstrapPrefix: paths.InstallBootstrapPrefix,
		bootstraps: []lifecycleBootstrapState{
			{
				path: paths.InstallBootstrapMainnet,
				context: installContext{
					Network: "mainnet", InitialP2PMode: "tor",
				},
			},
			{
				path: paths.InstallBootstrapTestnet4,
				context: installContext{
					Network: "testnet4", InitialP2PMode: "tor",
				},
			},
			{
				path: paths.InstallBootstrapPublicSignet,
				context: installContext{
					Network: "public-signet", InitialP2PMode: "tor",
				},
			},
		},
		ancestors: []lifecycleDir{
			{path: "/var", mode: 0o755},
			{path: "/var/lib", mode: 0o755},
		},
		unmarkedPaths: []string{
			paths.VarLibVPN,
			paths.ConfigDir,
			paths.SSHDDropIn,
			paths.AdminSudoers,
			paths.AptTorProxy,
			paths.DisableIPv6Conf,
			paths.Fail2banJail,
			paths.BitcoinDir,
			paths.BitcoinDataDir,
			paths.BitcoindService,
			paths.LNDDir,
			paths.LNDDataDir,
			paths.LNDService,
			paths.TorBitcoinP2P,
			paths.TorLNDGRPC,
			paths.TorLNDREST,
			paths.SyncthingDir,
			paths.SyncthingDataDir,
			paths.SyncthingService,
			paths.TorSyncthing,
			paths.TorSyncthingSync,
			paths.StateDir,
			paths.ExportDir,
			paths.BackupWatchPath,
			paths.BackupExportService,
			paths.BackupCheckTimer,
			paths.LNDCertWatchPath,
			paths.LNDCertStageService,
			paths.HelperSocketUnit,
			paths.HelperServiceUnit,
			paths.OldSSHDDropIn,
			paths.OldInstallStateFile,
			paths.OldPasswordPending,
			paths.OldServiceLayoutMark,
			"/etc/rlvpn",
			"/usr/local/bin/rlvpn",
			"/var/log/rlvpn.log",
		},
		conflictPaths: []string{
			paths.OldSSHDDropIn,
			paths.OldInstallStateFile,
			paths.OldPasswordPending,
			paths.OldServiceLayoutMark,
			"/etc/rlvpn",
			"/usr/local/bin/rlvpn",
			"/var/log/rlvpn.log",
		},
	}
}

func productionIdentityLookup() identityLookup {
	return identityLookup{
		user: func(name string) error {
			_, err := user.Lookup(name)
			return err
		},
		group: func(name string) error {
			_, err := user.LookupGroup(name)
			return err
		},
	}
}

// classifyLifecycleState is the read-only authorization boundary. It never
// adopts, normalizes, deletes, or repairs observed state.
func classifyLifecycleState(
	fs lifecycleFS, lookup identityLookup,
) (lifecycleState, error) {
	for _, d := range fs.ancestors {
		if err := validateRootDir(d.path, d.mode); err != nil {
			return lifecycleState{}, fmt.Errorf(
				"unsafe lifecycle ancestor: %w", err)
		}
	}
	bootstrap, err := inspectLifecycleBootstrap(fs)
	if err != nil {
		return lifecycleState{}, err
	}
	if bootstrap != nil {
		return classifyBootstrapLifecycle(fs, lookup, *bootstrap)
	}

	_, markerErr := os.Lstat(fs.layoutVersion)
	if errors.Is(markerErr, os.ErrNotExist) {
		return classifyUnmarkedLifecycle(fs, lookup)
	}
	if markerErr != nil {
		return lifecycleState{}, fmt.Errorf(
			"inspect layout version %s: %w", fs.layoutVersion, markerErr)
	}

	if err := validateRootDir(fs.varLibVPN, 0o755); err != nil {
		return lifecycleState{}, fmt.Errorf("unsafe lifecycle state: %w", err)
	}
	if err := validateRootDir(fs.privateDir, 0o700); err != nil {
		return lifecycleState{}, fmt.Errorf("unsafe lifecycle state: %w", err)
	}
	marker, err := readValidatedRootFile(fs.layoutVersion, 0o600)
	if err != nil {
		return lifecycleState{}, fmt.Errorf("unsafe layout version: %w", err)
	}
	if string(marker) != layoutVersionContent {
		return lifecycleState{}, fmt.Errorf(
			"unsupported or invalid layout version in %s, refusing to modify the host",
			fs.layoutVersion)
	}
	for _, stage := range []string{
		fs.ledger + ledgerBootstrapSuffix,
		fs.layoutVersion + layoutBootstrapSuffix,
	} {
		if present, err := lifecyclePathExists(stage); err != nil {
			return lifecycleState{}, err
		} else if present {
			return lifecycleState{}, fmt.Errorf(
				"lifecycle bootstrap staging residue %s exists without its bootstrap authority, refusing to modify the host",
				stage)
		}
	}

	conflicts, err := existingPaths(fs.conflictPaths)
	if err != nil {
		return lifecycleState{}, err
	}
	if present, err := identityPresent(lookup.user, "ripsline"); err != nil {
		return lifecycleState{}, err
	} else if present {
		conflicts = append(conflicts, "user:ripsline")
	}
	if present, err := identityPresent(lookup.group, "ripsline"); err != nil {
		return lifecycleState{}, err
	} else if present {
		conflicts = append(conflicts, "group:ripsline")
	}
	if len(conflicts) > 0 {
		return lifecycleState{}, fmt.Errorf(
			"conflicting prior-generation state found (%s), automatic migration is not supported, no installation changes were made",
			strings.Join(conflicts, ", "))
	}

	ledgerData, err := readValidatedRootFile(fs.ledger, 0o600)
	if err != nil {
		return lifecycleState{}, fmt.Errorf("unsafe install ledger: %w", err)
	}
	ledger, err := parseLedger(ledgerData)
	if err != nil {
		return lifecycleState{}, fmt.Errorf(
			"invalid install ledger %s: %w", fs.ledger, err)
	}

	pending := false
	if _, err := os.Lstat(fs.passwordMarker); err == nil {
		data, readErr := readValidatedRootFile(fs.passwordMarker, 0o600)
		if readErr != nil {
			return lifecycleState{}, fmt.Errorf(
				"unsafe password-pending marker: %w", readErr)
		}
		if string(data) != passwordPendingNote {
			return lifecycleState{}, fmt.Errorf(
				"invalid password-pending marker %s", fs.passwordMarker)
		}
		pending = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return lifecycleState{}, fmt.Errorf(
			"inspect password-pending marker: %w", err)
	}

	switch ledger.Status {
	case ledgerComplete:
		if pending {
			return lifecycleState{}, fmt.Errorf(
				"completed install ledger conflicts with a pending password delivery")
		}
		return lifecycleState{Disposition: lifecycleCompleted, Ledger: ledger}, nil
	case ledgerInProgress:
		if ledger.allBaseStepsDone() {
			return lifecycleState{
				Disposition: lifecycleCompletionPending, Ledger: ledger}, nil
		}
		return lifecycleState{Disposition: lifecycleResumable, Ledger: ledger}, nil
	default:
		panic("validated ledger has impossible status")
	}
}

func inspectLifecycleBootstrap(
	fs lifecycleFS,
) (*lifecycleBootstrapState, error) {
	known := make(map[string]bool, len(fs.bootstraps))
	for _, candidate := range fs.bootstraps {
		known[candidate.path] = true
	}
	bootstrapDir := filepath.Dir(fs.bootstrapPrefix)
	entries, err := os.ReadDir(bootstrapDir)
	if err != nil {
		return nil, fmt.Errorf(
			"inspect lifecycle bootstrap directory %s: %w", bootstrapDir, err)
	}
	prefix := filepath.Base(fs.bootstrapPrefix)
	for _, entry := range entries {
		path := filepath.Join(bootstrapDir, entry.Name())
		if strings.HasPrefix(entry.Name(), prefix) && !known[path] {
			return nil, fmt.Errorf(
				"unsupported or ambiguous lifecycle bootstrap %s, refusing to modify the host",
				path)
		}
	}

	var found *lifecycleBootstrapState
	for i := range fs.bootstraps {
		candidate := fs.bootstraps[i]
		if _, err := os.Lstat(candidate.path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, fmt.Errorf(
				"inspect lifecycle bootstrap %s: %w", candidate.path, err)
		}
		if found != nil {
			return nil, fmt.Errorf(
				"conflicting lifecycle bootstrap state found (%s, %s), refusing to modify the host",
				found.path, candidate.path)
		}
		if err := validateBootstrapFile(candidate.path, 0); err != nil {
			return nil, fmt.Errorf(
				"unsafe lifecycle bootstrap %s: %w", candidate.path, err)
		}
		copy := candidate
		found = &copy
	}
	return found, nil
}

func classifyBootstrapLifecycle(
	fs lifecycleFS, lookup identityLookup, bootstrap lifecycleBootstrapState,
) (lifecycleState, error) {
	found, err := existingPathsExcept(
		fs.unmarkedPaths, map[string]bool{fs.varLibVPN: true})
	if err != nil {
		return lifecycleState{}, err
	}
	identities, err := existingLifecycleIdentities(lookup)
	if err != nil {
		return lifecycleState{}, err
	}
	found = append(found, identities...)
	if len(found) > 0 {
		return lifecycleState{}, fmt.Errorf(
			"lifecycle bootstrap conflicts with existing project state (%s), refusing to modify the host",
			strings.Join(found, ", "))
	}
	if err := validateBootstrapTree(fs, bootstrap.context); err != nil {
		return lifecycleState{}, fmt.Errorf(
			"invalid lifecycle bootstrap state: %w", err)
	}
	ctx := bootstrap.context
	return lifecycleState{
		Disposition: lifecycleBootstrap, BootstrapContext: &ctx,
	}, nil
}

func classifyUnmarkedLifecycle(
	fs lifecycleFS, lookup identityLookup,
) (lifecycleState, error) {
	found, err := existingPaths(fs.unmarkedPaths)
	if err != nil {
		return lifecycleState{}, err
	}
	identities, err := existingLifecycleIdentities(lookup)
	if err != nil {
		return lifecycleState{}, err
	}
	found = append(found, identities...)
	if len(found) == 0 {
		return lifecycleState{Disposition: lifecyclePristine}, nil
	}
	return lifecycleState{}, fmt.Errorf(
		"existing unmarked vpn/rlvpn state found (%s), v0.7.0 supports only a fresh machine or a recognized interrupted v0.7.0 install, no installation changes were made",
		strings.Join(found, ", "))
}

func existingLifecycleIdentities(lookup identityLookup) ([]string, error) {
	var found []string
	for _, name := range []string{paths.AdminUser, bitcoinUser, lndUser,
		syncthingUser, "ripsline"} {
		present, err := identityPresent(lookup.user, name)
		if err != nil {
			return nil, err
		}
		if present {
			found = append(found, "user:"+name)
		}
	}
	for _, name := range []string{paths.AdminUser, bitcoinUser, lndUser,
		syncthingUser, "ripsline", backupGroup} {
		present, err := identityPresent(lookup.group, name)
		if err != nil {
			return nil, err
		}
		if present {
			found = append(found, "group:"+name)
		}
	}
	return found, nil
}

func existingPaths(candidates []string) ([]string, error) {
	return existingPathsExcept(candidates, nil)
}

func existingPathsExcept(
	candidates []string, excluded map[string]bool,
) ([]string, error) {
	var found []string
	seen := map[string]bool{}
	for _, path := range candidates {
		if seen[path] || excluded[path] {
			continue
		}
		seen[path] = true
		if _, err := os.Lstat(path); err == nil {
			found = append(found, path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect lifecycle evidence %s: %w", path, err)
		}
	}
	return found, nil
}

func identityPresent(lookup func(string) error, name string) (bool, error) {
	err := lookup(name)
	if err == nil {
		return true, nil
	}
	var unknownUser user.UnknownUserError
	var unknownGroup user.UnknownGroupError
	if errors.As(err, &unknownUser) || errors.As(err, &unknownGroup) {
		return false, nil
	}
	return false, fmt.Errorf("inspect identity %s: %w", name, err)
}

func readValidatedRootFile(path string, mode os.FileMode) ([]byte, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|
		syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	if err := validateOpenRootFile(f, mode); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	data, err := io.ReadAll(io.LimitReader(
		f, maxLifecycleStateFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) > maxLifecycleStateFileBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", path,
			maxLifecycleStateFileBytes)
	}
	return data, nil
}

func validateBootstrapFile(path string, maxBytes int64) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() || fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("not a regular file")
	}
	// An interruption can occur between exclusive creation and fchmod. Only
	// owner permissions (or a safe subset removed by umask) can therefore be
	// present on an installer-created staging object.
	if fi.Mode().Perm()&^os.FileMode(0o600) != 0 {
		return fmt.Errorf("mode is %04o, want an owner-only subset of 0600",
			fi.Mode().Perm())
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != 0 || st.Gid != 0 {
		return fmt.Errorf("not owned by root:root")
	}
	if st.Nlink != 1 {
		return fmt.Errorf("link count is %d, want 1", st.Nlink)
	}
	if fi.Size() > maxBytes {
		return fmt.Errorf("size is %d, maximum is %d", fi.Size(), maxBytes)
	}
	return nil
}

func validateBootstrapTree(fs lifecycleFS, ctx installContext) error {
	varLibPresent, err := lifecyclePathExists(fs.varLibVPN)
	if err != nil {
		return err
	}
	if !varLibPresent {
		return nil
	}
	if err := validateBootstrapDir(fs.varLibVPN, 0o755); err != nil {
		return err
	}
	if err := validateDirectoryEntryNames(
		fs.varLibVPN, filepath.Base(fs.privateDir)); err != nil {
		return err
	}

	privatePresent, err := lifecyclePathExists(fs.privateDir)
	if err != nil {
		return err
	}
	if !privatePresent {
		return nil
	}
	if err := validateBootstrapDir(fs.privateDir, 0o700); err != nil {
		return err
	}
	ledgerStage := fs.ledger + ledgerBootstrapSuffix
	layoutStage := fs.layoutVersion + layoutBootstrapSuffix
	if err := validateDirectoryEntryNames(fs.privateDir,
		filepath.Base(fs.ledger), filepath.Base(fs.layoutVersion),
		filepath.Base(ledgerStage), filepath.Base(layoutStage)); err != nil {
		return err
	}

	ledgerPresent, err := lifecyclePathExists(fs.ledger)
	if err != nil {
		return err
	}
	ledgerStagePresent, err := lifecyclePathExists(ledgerStage)
	if err != nil {
		return err
	}
	layoutPresent, err := lifecyclePathExists(fs.layoutVersion)
	if err != nil {
		return err
	}
	layoutStagePresent, err := lifecyclePathExists(layoutStage)
	if err != nil {
		return err
	}

	if ledgerStagePresent {
		if err := validateBootstrapFile(
			ledgerStage, maxLifecycleStateFileBytes); err != nil {
			return fmt.Errorf("unsafe ledger staging file: %w", err)
		}
		if ledgerPresent || layoutStagePresent || layoutPresent {
			return fmt.Errorf("ledger staging file conflicts with published state")
		}
	}
	if ledgerPresent {
		data, err := readValidatedRootFile(fs.ledger, 0o600)
		if err != nil {
			return fmt.Errorf("unsafe bootstrap ledger: %w", err)
		}
		ledger, err := parseLedger(data)
		if err != nil {
			return fmt.Errorf("invalid bootstrap ledger: %w", err)
		}
		if !initialLedgerMatchesContext(ledger, ctx) {
			return fmt.Errorf(
				"bootstrap ledger is not the exact initial %s/%s state",
				ctx.Network, ctx.InitialP2PMode)
		}
	}
	if layoutStagePresent {
		if err := validateBootstrapFile(
			layoutStage, int64(len(layoutVersionContent))); err != nil {
			return fmt.Errorf("unsafe layout-version staging file: %w", err)
		}
		if !ledgerPresent || layoutPresent {
			return fmt.Errorf(
				"layout-version staging file lacks its initial ledger boundary")
		}
	}
	if layoutPresent {
		if !ledgerPresent {
			return fmt.Errorf("layout version exists without its initial ledger")
		}
		data, err := readValidatedRootFile(fs.layoutVersion, 0o600)
		if err != nil {
			return fmt.Errorf("unsafe bootstrap layout version: %w", err)
		}
		if string(data) != layoutVersionContent {
			return fmt.Errorf("invalid bootstrap layout-version content")
		}
	}
	return nil
}

func validateBootstrapDir(path string, finalMode os.FileMode) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is not a real directory", path)
	}
	mode := fi.Mode().Perm()
	transitional := mode&^os.FileMode(0o700) == 0
	if mode != finalMode && !transitional {
		return fmt.Errorf(
			"%s mode is %04o, want %04o or an owner-only bootstrap mode",
			path, mode, finalMode)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != 0 || st.Gid != 0 {
		return fmt.Errorf("%s is not owned by root:root", path)
	}
	return nil
}

func validateDirectoryEntryNames(dir string, allowed ...string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read bootstrap directory %s: %w", dir, err)
	}
	allowedSet := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		allowedSet[name] = true
	}
	for _, entry := range entries {
		if !allowedSet[entry.Name()] {
			return fmt.Errorf("unexpected bootstrap object %s",
				filepath.Join(dir, entry.Name()))
		}
	}
	return nil
}

func lifecyclePathExists(path string) (bool, error) {
	if _, err := os.Lstat(path); err == nil {
		return true, nil
	} else if errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else {
		return false, fmt.Errorf("inspect %s: %w", path, err)
	}
}

func initialLedgerMatchesContext(l *installLedger, ctx installContext) bool {
	return l.Schema == ledgerSchema && l.Status == ledgerInProgress &&
		l.Context.Network == ctx.Network &&
		l.Context.InitialP2PMode == ctx.InitialP2PMode &&
		l.Context.DbCacheMB == nil && len(l.Steps) == 0
}

type lifecycleInitBoundary string

const (
	initBootstrapPublished lifecycleInitBoundary = "bootstrap-published"
	initVarLibPublished    lifecycleInitBoundary = "var-lib-published"
	initPrivatePublished   lifecycleInitBoundary = "private-dir-published"
	initLedgerStaged       lifecycleInitBoundary = "ledger-staged"
	initLedgerPublished    lifecycleInitBoundary = "ledger-published"
	initLayoutStaged       lifecycleInitBoundary = "layout-version-staged"
	initLayoutPublished    lifecycleInitBoundary = "layout-version-published"
	initTreeFinalized      lifecycleInitBoundary = "tree-finalized"
	initBootstrapRemoved   lifecycleInitBoundary = "bootstrap-removed"
)

type lifecycleInitHook func(lifecycleInitBoundary) error

func initializeLifecycle(
	fs lifecycleFS, lookup identityLookup, ctx installContext,
) (*installLedger, error) {
	return initializeLifecycleWithHook(fs, lookup, ctx, nil)
}

func initializeLifecycleWithHook(
	fs lifecycleFS, lookup identityLookup, ctx installContext,
	hook lifecycleInitHook,
) (*installLedger, error) {
	ledger, err := newLedger(ctx)
	if err != nil {
		return nil, err
	}
	bootstrap, err := bootstrapForContext(fs, ctx)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(bootstrap.path,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf(
			"create lifecycle bootstrap %s: %w", bootstrap.path, err)
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("secure lifecycle bootstrap: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("sync lifecycle bootstrap: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("close lifecycle bootstrap: %w", err)
	}
	if err := syncLifecycleDir(filepath.Dir(bootstrap.path)); err != nil {
		return nil, fmt.Errorf("persist lifecycle bootstrap: %w", err)
	}
	if err := callLifecycleInitHook(hook, initBootstrapPublished); err != nil {
		return nil, err
	}
	return completeLifecycleBootstrapWithHook(
		fs, lookup, bootstrap.context, ledger, hook)
}

func resumeLifecycleBootstrap(
	fs lifecycleFS, lookup identityLookup, ctx installContext,
) (*installLedger, error) {
	ledger, err := newLedger(ctx)
	if err != nil {
		return nil, err
	}
	return completeLifecycleBootstrapWithHook(fs, lookup, ctx, ledger, nil)
}

func completeLifecycleBootstrapWithHook(
	fs lifecycleFS, lookup identityLookup, ctx installContext,
	ledger *installLedger, hook lifecycleInitHook,
) (*installLedger, error) {
	state, err := classifyLifecycleState(fs, lookup)
	if err != nil {
		return nil, err
	}
	if state.Disposition != lifecycleBootstrap ||
		state.BootstrapContext == nil ||
		!sameInitialContext(*state.BootstrapContext, ctx) {
		return nil, fmt.Errorf(
			"lifecycle bootstrap changed before initialization could resume")
	}

	varLibPresent, err := lifecyclePathExists(fs.varLibVPN)
	if err != nil {
		return nil, err
	}
	if !varLibPresent {
		if err := createBootstrapDir(fs.varLibVPN,
			filepath.Dir(fs.varLibVPN)); err != nil {
			return nil, err
		}
		if err := callLifecycleInitHook(hook, initVarLibPublished); err != nil {
			return nil, err
		}
	} else if err := secureBootstrapDir(fs.varLibVPN); err != nil {
		return nil, err
	}

	privatePresent, err := lifecyclePathExists(fs.privateDir)
	if err != nil {
		return nil, err
	}
	if !privatePresent {
		if err := createBootstrapDir(fs.privateDir, fs.varLibVPN); err != nil {
			return nil, err
		}
		if err := callLifecycleInitHook(hook, initPrivatePublished); err != nil {
			return nil, err
		}
	} else if err := secureBootstrapDir(fs.privateDir); err != nil {
		return nil, err
	}

	ledgerStage := fs.ledger + ledgerBootstrapSuffix
	if present, err := lifecyclePathExists(ledgerStage); err != nil {
		return nil, err
	} else if present {
		if err := os.Remove(ledgerStage); err != nil {
			return nil, fmt.Errorf("remove interrupted ledger staging file: %w", err)
		}
		if err := syncLifecycleDir(fs.privateDir); err != nil {
			return nil, fmt.Errorf("persist ledger staging cleanup: %w", err)
		}
	}
	ledgerPresent, err := lifecyclePathExists(fs.ledger)
	if err != nil {
		return nil, err
	}
	if !ledgerPresent {
		data, err := json.MarshalIndent(ledger, "", "  ")
		if err != nil {
			return nil, err
		}
		data = append(data, '\n')
		if err := publishBootstrapPayload(
			ledgerStage, fs.ledger, data, initLedgerStaged,
			initLedgerPublished, hook); err != nil {
			return nil, fmt.Errorf("initialize install ledger: %w", err)
		}
	} else {
		data, err := readValidatedRootFile(fs.ledger, 0o600)
		if err != nil {
			return nil, err
		}
		published, err := parseLedger(data)
		if err != nil || !initialLedgerMatchesContext(published, ctx) {
			return nil, fmt.Errorf(
				"published bootstrap ledger no longer matches its context")
		}
		ledger = published
	}

	layoutStage := fs.layoutVersion + layoutBootstrapSuffix
	if present, err := lifecyclePathExists(layoutStage); err != nil {
		return nil, err
	} else if present {
		if err := os.Remove(layoutStage); err != nil {
			return nil, fmt.Errorf(
				"remove interrupted layout-version staging file: %w", err)
		}
		if err := syncLifecycleDir(fs.privateDir); err != nil {
			return nil, fmt.Errorf("persist layout staging cleanup: %w", err)
		}
	}
	layoutPresent, err := lifecyclePathExists(fs.layoutVersion)
	if err != nil {
		return nil, err
	}
	if !layoutPresent {
		if err := publishBootstrapPayload(
			layoutStage, fs.layoutVersion, []byte(layoutVersionContent),
			initLayoutStaged, initLayoutPublished, hook); err != nil {
			return nil, fmt.Errorf("initialize layout version: %w", err)
		}
	}

	if err := os.Chmod(fs.varLibVPN, 0o755); err != nil {
		return nil, fmt.Errorf("publish %s mode: %w", fs.varLibVPN, err)
	}
	if err := syncLifecycleDir(fs.varLibVPN); err != nil {
		return nil, fmt.Errorf("persist %s mode: %w", fs.varLibVPN, err)
	}
	if err := callLifecycleInitHook(hook, initTreeFinalized); err != nil {
		return nil, err
	}

	bootstrap, err := bootstrapForContext(fs, ctx)
	if err != nil {
		return nil, err
	}
	if err := os.Remove(bootstrap.path); err != nil {
		return nil, fmt.Errorf("remove lifecycle bootstrap: %w", err)
	}
	if err := syncLifecycleDir(filepath.Dir(bootstrap.path)); err != nil {
		return nil, fmt.Errorf("persist lifecycle bootstrap removal: %w", err)
	}
	if err := callLifecycleInitHook(hook, initBootstrapRemoved); err != nil {
		return nil, err
	}
	return ledger, nil
}

func bootstrapForContext(
	fs lifecycleFS, ctx installContext,
) (lifecycleBootstrapState, error) {
	for _, candidate := range fs.bootstraps {
		if sameInitialContext(candidate.context, ctx) {
			return candidate, nil
		}
	}
	return lifecycleBootstrapState{}, fmt.Errorf(
		"no lifecycle bootstrap path for initial context %q/%q",
		ctx.Network, ctx.InitialP2PMode)
}

func sameInitialContext(a, b installContext) bool {
	return a.Network == b.Network &&
		a.InitialP2PMode == b.InitialP2PMode &&
		a.DbCacheMB == nil && b.DbCacheMB == nil
}

func createBootstrapDir(path, parent string) error {
	if err := os.Mkdir(path, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if err := secureBootstrapDir(path); err != nil {
		return err
	}
	if err := syncLifecycleDir(parent); err != nil {
		return fmt.Errorf("persist %s creation: %w", path, err)
	}
	return nil
}

func secureBootstrapDir(path string) error {
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("secure %s: %w", path, err)
	}
	if err := validateRootDir(path, 0o700); err != nil {
		return err
	}
	if err := syncLifecycleDir(path); err != nil {
		return fmt.Errorf("persist %s permissions: %w", path, err)
	}
	return nil
}

func publishBootstrapPayload(
	stage, destination string, data []byte,
	stagedBoundary, publishedBoundary lifecycleInitBoundary,
	hook lifecycleInitHook,
) error {
	f, err := os.OpenFile(stage, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create staging file %s: %w", stage, err)
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := callLifecycleInitHook(hook, stagedBoundary); err != nil {
		return err
	}
	if err := os.Rename(stage, destination); err != nil {
		return fmt.Errorf("publish %s: %w", destination, err)
	}
	if err := syncLifecycleDir(filepath.Dir(destination)); err != nil {
		return fmt.Errorf("persist %s publication: %w", destination, err)
	}
	return callLifecycleInitHook(hook, publishedBoundary)
}

func callLifecycleInitHook(
	hook lifecycleInitHook, boundary lifecycleInitBoundary,
) error {
	if hook == nil {
		return nil
	}
	if err := hook(boundary); err != nil {
		return fmt.Errorf("lifecycle initialization interrupted after %s: %w",
			boundary, err)
	}
	return nil
}

func syncLifecycleDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		_ = d.Close()
		return err
	}
	return d.Close()
}
