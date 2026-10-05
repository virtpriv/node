package installer

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/virtpriv/node/internal/paths"
)

type lifecycleFixture struct {
	fs     lifecycleFS
	lookup identityLookup
	users  map[string]bool
	groups map[string]bool
}

func newLifecycleFixture(t *testing.T) *lifecycleFixture {
	t.Helper()
	root := t.TempDir()
	rootInfo, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(root, "var-lib-vpn", "private")
	f := &lifecycleFixture{
		users: map[string]bool{}, groups: map[string]bool{},
	}
	f.fs = lifecycleFS{
		varLibVPN:       filepath.Join(root, "var-lib-vpn"),
		privateDir:      private,
		layoutVersion:   filepath.Join(private, "layout-version"),
		ledger:          filepath.Join(private, "install-state.json"),
		passwordMarker:  filepath.Join(private, "password-pending"),
		bootstrapPrefix: filepath.Join(root, "bootstrap-"),
		bootstraps: []lifecycleBootstrapState{
			{
				path: filepath.Join(root, "bootstrap-mainnet-tor"),
				context: installContext{
					Network: "mainnet", InitialP2PMode: "tor",
				},
			},
			{
				path: filepath.Join(root, "bootstrap-testnet4-tor"),
				context: installContext{
					Network: "testnet4", InitialP2PMode: "tor",
				},
			},
			{
				path: filepath.Join(root, "bootstrap-public-signet-tor"),
				context: installContext{
					Network: "public-signet", InitialP2PMode: "tor",
				},
			},
		},
		ancestors: []lifecycleDir{{path: root, mode: rootInfo.Mode().Perm()}},
		unmarkedPaths: []string{
			filepath.Join(root, "var-lib-vpn"),
			filepath.Join(root, "etc-vpn"),
			filepath.Join(root, "etc-rlvpn"),
		},
		conflictPaths: []string{
			filepath.Join(root, "old-layout-version"),
			filepath.Join(root, "etc-rlvpn"),
		},
	}
	f.lookup = identityLookup{
		user: func(name string) error {
			if f.users[name] {
				return nil
			}
			return user.UnknownUserError(name)
		},
		group: func(name string) error {
			if f.groups[name] {
				return nil
			}
			return user.UnknownGroupError(name)
		},
	}
	return f
}

func (f *lifecycleFixture) classify() (lifecycleState, error) {
	return classifyLifecycleState(f.fs, f.lookup)
}

func (f *lifecycleFixture) initialize(t *testing.T) *installLedger {
	t.Helper()
	l, err := initializeLifecycle(f.fs, f.lookup, installContext{
		Network: "testnet4", InitialP2PMode: "tor",
	})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func (f *lifecycleFixture) seedBootstrap(t *testing.T, index int) string {
	t.Helper()
	path := f.fs.bootstraps[index].path
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRootLifecyclePristineInitializeAndResume(t *testing.T) {
	requireRootTestEnvironment(t)
	f := newLifecycleFixture(t)
	state, err := f.classify()
	if err != nil || state.Disposition != lifecyclePristine {
		t.Fatalf("pristine classification: %+v, %v", state, err)
	}
	l := f.initialize(t)
	if err := l.setDbCache(1024); err != nil {
		t.Fatal(err)
	}
	if err := l.markDone("binary.install", "0.7.0"); err != nil {
		t.Fatal(err)
	}
	if err := l.save(f.fs.ledger); err != nil {
		t.Fatal(err)
	}
	state, err = f.classify()
	if err != nil || state.Disposition != lifecycleResumable {
		t.Fatalf("resume classification: %+v, %v", state, err)
	}
	if state.Ledger.Context.Network != "testnet4" {
		t.Fatalf("resume lost network: %+v", state.Ledger.Context)
	}
	for path, mode := range map[string]os.FileMode{
		f.fs.varLibVPN: 0o755, f.fs.privateDir: 0o700,
		f.fs.layoutVersion: 0o600, f.fs.ledger: 0o600,
	} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Errorf("%s metadata: %v", path, err)
			continue
		}
		if info.Mode().Perm() != mode {
			t.Errorf("%s mode=%04o, want %04o", path,
				info.Mode().Perm(), mode)
		}
	}
	data, _ := os.ReadFile(f.fs.layoutVersion)
	if string(data) != layoutVersionContent {
		t.Fatalf("layout bytes %q", data)
	}

	boundaries := []lifecycleInitBoundary{
		initBootstrapPublished,
		initVarLibPublished,
		initPrivatePublished,
		initLedgerStaged,
		initLedgerPublished,
		initLayoutStaged,
		initLayoutPublished,
		initTreeFinalized,
		initBootstrapRemoved,
	}
	for _, boundary := range boundaries {
		t.Run("interruption after "+string(boundary), func(t *testing.T) {
			f := newLifecycleFixture(t)
			interrupted := errors.New("simulated process death")
			reached := false
			_, err := initializeLifecycleWithHook(
				f.fs, f.lookup, installContext{
					Network: "testnet4", InitialP2PMode: "tor",
				}, func(got lifecycleInitBoundary) error {
					if got != boundary {
						return nil
					}
					reached = true
					return interrupted
				})
			if !reached || !errors.Is(err, interrupted) {
				t.Fatalf("boundary was not retained: reached=%v err=%v",
					reached, err)
			}

			state, err := f.classify()
			if err != nil {
				t.Fatalf("interrupted state refused: %v", err)
			}
			if boundary == initBootstrapRemoved {
				if state.Disposition != lifecycleResumable {
					t.Fatalf("post-bootstrap state=%v, want resumable",
						state.Disposition)
				}
			} else {
				if state.Disposition != lifecycleBootstrap ||
					state.BootstrapContext == nil {
					t.Fatalf("interrupted state=%+v, want bootstrap",
						state)
				}
				if _, err := resumeLifecycleBootstrap(
					f.fs, f.lookup, *state.BootstrapContext); err != nil {
					t.Fatalf("resume bootstrap: %v", err)
				}
			}

			state, err = f.classify()
			if err != nil || state.Disposition != lifecycleResumable {
				t.Fatalf("resumed classification: %+v, %v", state, err)
			}
			for _, path := range []string{
				f.fs.ledger + ledgerBootstrapSuffix,
				f.fs.layoutVersion + layoutBootstrapSuffix,
				f.fs.bootstraps[0].path,
				f.fs.bootstraps[1].path,
			} {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("bootstrap residue %s: %v", path, err)
				}
			}
		})
	}
}

func TestRootLifecycleCompletionPendingThenCompleted(t *testing.T) {
	requireRootTestEnvironment(t)
	f := newLifecycleFixture(t)
	l := f.initialize(t)
	if err := l.setDbCache(1024); err != nil {
		t.Fatal(err)
	}
	for _, key := range baseInstallStepKeys {
		if err := l.markDone(key, "0.7.0"); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.save(f.fs.ledger); err != nil {
		t.Fatal(err)
	}
	state, err := f.classify()
	if err != nil || state.Disposition != lifecycleCompletionPending {
		t.Fatalf("completion pending: %+v, %v", state, err)
	}
	if err := l.markComplete(); err != nil {
		t.Fatal(err)
	}
	if err := l.save(f.fs.ledger); err != nil {
		t.Fatal(err)
	}
	state, err = f.classify()
	if err != nil || state.Disposition != lifecycleCompleted {
		t.Fatalf("completed: %+v, %v", state, err)
	}
}

func TestRootLifecycleCompletedWithPasswordPendingRefuses(t *testing.T) {
	requireRootTestEnvironment(t)
	f := newLifecycleFixture(t)
	l := f.initialize(t)
	if err := l.setDbCache(512); err != nil {
		t.Fatal(err)
	}
	for _, key := range baseInstallStepKeys {
		if err := l.markDone(key, "0.7.0"); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.markComplete(); err != nil {
		t.Fatal(err)
	}
	if err := l.save(f.fs.ledger); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.fs.passwordMarker,
		[]byte(passwordPendingNote), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.classify(); err == nil ||
		!strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("completed/pending contradiction accepted: %v", err)
	}
}

func TestRootLifecycleUnmarkedEvidenceRefuses(t *testing.T) {
	requireRootTestEnvironment(t)
	cases := []struct {
		name string
		seed func(*testing.T, *lifecycleFixture)
	}{
		{"project directory", func(t *testing.T, f *lifecycleFixture) {
			if err := os.Mkdir(f.fs.varLibVPN, 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{"config", func(t *testing.T, f *lifecycleFixture) {
			if err := os.WriteFile(f.fs.unmarkedPaths[1], []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"vpn user", func(_ *testing.T, f *lifecycleFixture) {
			f.users["vpn"] = true
		}},
		{"bitcoin user", func(_ *testing.T, f *lifecycleFixture) {
			f.users[bitcoinUser] = true
		}},
		{"lnd user", func(_ *testing.T, f *lifecycleFixture) {
			f.users[lndUser] = true
		}},
		{"syncthing user", func(_ *testing.T, f *lifecycleFixture) {
			f.users[syncthingUser] = true
		}},
		{"rlvpn user", func(_ *testing.T, f *lifecycleFixture) {
			f.users["ripsline"] = true
		}},
		{"backup group", func(_ *testing.T, f *lifecycleFixture) {
			f.groups[backupGroup] = true
		}},
		{"vpn group", func(_ *testing.T, f *lifecycleFixture) {
			f.groups[paths.AdminUser] = true
		}},
		{"rlvpn group", func(_ *testing.T, f *lifecycleFixture) {
			f.groups["ripsline"] = true
		}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			f := newLifecycleFixture(t)
			tt.seed(t, f)
			if _, err := f.classify(); err == nil ||
				!strings.Contains(err.Error(), "unmarked") {
				t.Fatalf("unmarked evidence accepted: %v", err)
			}
		})
	}
}

func TestProductionLifecycleEvidenceInventory(t *testing.T) {
	fs := productionLifecycleFS()
	present := make(map[string]bool, len(fs.unmarkedPaths))
	for _, path := range fs.unmarkedPaths {
		present[path] = true
	}
	for _, path := range []string{
		paths.VarLibVPN,
		paths.ConfigDir,
		paths.SSHDDropIn,
		paths.AdminSudoers,
		paths.AptTorProxy,
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
		paths.StateDir,
		paths.ExportDir,
		paths.BackupWatchPath,
		paths.BackupExportService,
		paths.LNDCertWatchPath,
		paths.LNDCertStageService,
		paths.HelperSocketUnit,
		paths.HelperServiceUnit,
		paths.OldSSHDDropIn,
		paths.OldInstallStateFile,
		paths.OldPasswordPending,
		paths.OldServiceLayoutMark,
	} {
		if !present[path] {
			t.Errorf("project lifecycle evidence missing from classifier: %s", path)
		}
	}
	for _, allowed := range []string{paths.BinaryPath, paths.LogFile} {
		if present[allowed] {
			t.Errorf("bootstrap/diagnostic path would make fresh install impossible: %s", allowed)
		}
	}
	if len(fs.bootstraps) != 3 {
		t.Fatalf("production bootstrap contexts=%d, want 3", len(fs.bootstraps))
	}
	wantBootstrap := map[string]string{
		"mainnet":       paths.InstallBootstrapMainnet,
		"testnet4":      paths.InstallBootstrapTestnet4,
		"public-signet": paths.InstallBootstrapPublicSignet,
	}
	for _, bootstrap := range fs.bootstraps {
		if got := wantBootstrap[bootstrap.context.Network]; got != bootstrap.path {
			t.Errorf("%s bootstrap=%q, want %q",
				bootstrap.context.Network, bootstrap.path, got)
		}
		if !strings.Contains(bootstrap.path, "service-layout-1") ||
			layoutVersionContent != "service-layout=1\n" {
			t.Errorf("bootstrap path %q and layout content %q disagree",
				bootstrap.path, layoutVersionContent)
		}
	}
}

func TestRootLifecycleMarkedConflictRefuses(t *testing.T) {
	requireRootTestEnvironment(t)
	f := newLifecycleFixture(t)
	f.initialize(t)
	if err := os.WriteFile(f.fs.conflictPaths[0], []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.classify(); err == nil ||
		!strings.Contains(err.Error(), "conflicting") {
		t.Fatalf("marked conflict accepted: %v", err)
	}
}

func TestRootLifecycleMarkedOldIdentityConflictRefuses(t *testing.T) {
	requireRootTestEnvironment(t)
	for _, kind := range []string{"user", "group"} {
		t.Run(kind, func(t *testing.T) {
			f := newLifecycleFixture(t)
			f.initialize(t)
			if kind == "user" {
				f.users["ripsline"] = true
			} else {
				f.groups["ripsline"] = true
			}
			if _, err := f.classify(); err == nil ||
				!strings.Contains(err.Error(), "conflicting") {
				t.Fatalf("marked old %s accepted: %v", kind, err)
			}
		})
	}
}

func TestRootLifecycleUnsafeAndMalformedStateRefuses(t *testing.T) {
	requireRootTestEnvironment(t)
	bootstrapCases := []struct {
		name string
		seed func(*testing.T, *lifecycleFixture)
	}{
		{"nonempty bootstrap", func(t *testing.T, f *lifecycleFixture) {
			path := f.seedBootstrap(t, 1)
			if err := os.WriteFile(path, []byte("unexpected"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"bootstrap mode", func(t *testing.T, f *lifecycleFixture) {
			path := f.seedBootstrap(t, 1)
			if err := os.Chmod(path, 0o640); err != nil {
				t.Fatal(err)
			}
		}},
		{"bootstrap symlink", func(t *testing.T, f *lifecycleFixture) {
			if err := os.Symlink("missing", f.fs.bootstraps[1].path); err != nil {
				t.Fatal(err)
			}
		}},
		{"bootstrap hardlink", func(t *testing.T, f *lifecycleFixture) {
			path := f.seedBootstrap(t, 1)
			if err := os.Link(path, path+"-link"); err != nil {
				t.Fatal(err)
			}
		}},
		{"multiple bootstrap contexts", func(t *testing.T, f *lifecycleFixture) {
			f.seedBootstrap(t, 0)
			f.seedBootstrap(t, 1)
		}},
		{"unknown bootstrap context", func(t *testing.T, f *lifecycleFixture) {
			if err := os.WriteFile(f.fs.bootstrapPrefix+"future-layout",
				nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"bootstrap with external project state", func(t *testing.T, f *lifecycleFixture) {
			f.seedBootstrap(t, 1)
			if err := os.WriteFile(
				f.fs.unmarkedPaths[1], []byte("existing"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"bootstrap with project identity", func(t *testing.T, f *lifecycleFixture) {
			f.seedBootstrap(t, 1)
			f.users[paths.AdminUser] = true
		}},
		{"bootstrap with unexpected tree object", func(t *testing.T, f *lifecycleFixture) {
			f.seedBootstrap(t, 1)
			if err := os.Mkdir(f.fs.varLibVPN, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.fs.varLibVPN, "unknown"),
				[]byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"bootstrap with unsafe tree mode", func(t *testing.T, f *lifecycleFixture) {
			f.seedBootstrap(t, 1)
			if err := os.Mkdir(f.fs.varLibVPN, 0o750); err != nil {
				t.Fatal(err)
			}
		}},
		{"bootstrap with unsafe staging object", func(t *testing.T, f *lifecycleFixture) {
			f.seedBootstrap(t, 1)
			if err := os.Mkdir(f.fs.varLibVPN, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(f.fs.privateDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("missing",
				f.fs.ledger+ledgerBootstrapSuffix); err != nil {
				t.Fatal(err)
			}
		}},
		{"bootstrap with invalid ledger", func(t *testing.T, f *lifecycleFixture) {
			f.seedBootstrap(t, 1)
			if err := os.Mkdir(f.fs.varLibVPN, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(f.fs.privateDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(f.fs.ledger, []byte("{}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"bootstrap with wrong-context ledger", func(t *testing.T, f *lifecycleFixture) {
			f.seedBootstrap(t, 1)
			if err := os.Mkdir(f.fs.varLibVPN, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(f.fs.privateDir, 0o700); err != nil {
				t.Fatal(err)
			}
			ledger, err := newLedger(installContext{
				Network: "mainnet", InitialP2PMode: "tor",
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := ledger.save(f.fs.ledger); err != nil {
				t.Fatal(err)
			}
		}},
		{"bootstrap layout without ledger", func(t *testing.T, f *lifecycleFixture) {
			f.seedBootstrap(t, 1)
			if err := os.Mkdir(f.fs.varLibVPN, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(f.fs.privateDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(f.fs.layoutVersion,
				[]byte(layoutVersionContent), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range bootstrapCases {
		t.Run("bootstrap/"+tt.name, func(t *testing.T) {
			f := newLifecycleFixture(t)
			tt.seed(t, f)
			if _, err := f.classify(); err == nil {
				t.Fatal("unsafe or ambiguous bootstrap state accepted")
			}
		})
	}

	cases := []struct {
		name   string
		mutate func(*testing.T, *lifecycleFixture)
	}{
		{"marker content", func(t *testing.T, f *lifecycleFixture) {
			if err := os.WriteFile(f.fs.layoutVersion, []byte("service-layout=2\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"marker mode", func(t *testing.T, f *lifecycleFixture) {
			if err := os.Chmod(f.fs.layoutVersion, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"marker owner", func(t *testing.T, f *lifecycleFixture) {
			requireRootTestCapability(t, "changing file ownership",
				os.Chown(f.fs.layoutVersion, 1, 1))
		}},
		{"marker symlink", func(t *testing.T, f *lifecycleFixture) {
			if err := os.Remove(f.fs.layoutVersion); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("install-state.json", f.fs.layoutVersion); err != nil {
				t.Fatal(err)
			}
		}},
		{"marker fifo", func(t *testing.T, f *lifecycleFixture) {
			if err := os.Remove(f.fs.layoutVersion); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(f.fs.layoutVersion, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"marker hardlink", func(t *testing.T, f *lifecycleFixture) {
			if err := os.Link(f.fs.layoutVersion,
				filepath.Join(f.fs.privateDir, "second-link")); err != nil {
				t.Fatal(err)
			}
		}},
		{"ledger bootstrap residue", func(t *testing.T, f *lifecycleFixture) {
			if err := os.WriteFile(f.fs.ledger+ledgerBootstrapSuffix,
				[]byte("residue"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"layout bootstrap residue", func(t *testing.T, f *lifecycleFixture) {
			if err := os.WriteFile(f.fs.layoutVersion+layoutBootstrapSuffix,
				[]byte("residue"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"private mode", func(t *testing.T, f *lifecycleFixture) {
			if err := os.Chmod(f.fs.privateDir, 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{"varlib mode", func(t *testing.T, f *lifecycleFixture) {
			if err := os.Chmod(f.fs.varLibVPN, 0o750); err != nil {
				t.Fatal(err)
			}
		}},
		{"missing ledger", func(t *testing.T, f *lifecycleFixture) {
			if err := os.Remove(f.fs.ledger); err != nil {
				t.Fatal(err)
			}
		}},
		{"ledger mode", func(t *testing.T, f *lifecycleFixture) {
			if err := os.Chmod(f.fs.ledger, 0o640); err != nil {
				t.Fatal(err)
			}
		}},
		{"ledger owner", func(t *testing.T, f *lifecycleFixture) {
			requireRootTestCapability(t, "changing file ownership",
				os.Chown(f.fs.ledger, 1, 1))
		}},
		{"ledger symlink", func(t *testing.T, f *lifecycleFixture) {
			if err := os.Remove(f.fs.ledger); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("layout-version", f.fs.ledger); err != nil {
				t.Fatal(err)
			}
		}},
		{"ledger directory", func(t *testing.T, f *lifecycleFixture) {
			if err := os.Remove(f.fs.ledger); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(f.fs.ledger, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"ledger fifo", func(t *testing.T, f *lifecycleFixture) {
			if err := os.Remove(f.fs.ledger); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(f.fs.ledger, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"ledger hardlink", func(t *testing.T, f *lifecycleFixture) {
			if err := os.Link(f.fs.ledger,
				filepath.Join(f.fs.privateDir, "ledger-link")); err != nil {
				t.Fatal(err)
			}
		}},
		{"malformed ledger", func(t *testing.T, f *lifecycleFixture) {
			if err := os.WriteFile(f.fs.ledger, []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"oversized ledger", func(t *testing.T, f *lifecycleFixture) {
			data := make([]byte, maxLifecycleStateFileBytes+1)
			if err := os.WriteFile(f.fs.ledger, data, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"password marker content", func(t *testing.T, f *lifecycleFixture) {
			if err := os.WriteFile(f.fs.passwordMarker, []byte("unknown\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"password marker mode", func(t *testing.T, f *lifecycleFixture) {
			if err := os.WriteFile(f.fs.passwordMarker,
				[]byte(passwordPendingNote), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"password marker symlink", func(t *testing.T, f *lifecycleFixture) {
			if err := os.Symlink("layout-version", f.fs.passwordMarker); err != nil {
				t.Fatal(err)
			}
		}},
		{"password marker fifo", func(t *testing.T, f *lifecycleFixture) {
			if err := syscall.Mkfifo(f.fs.passwordMarker, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			f := newLifecycleFixture(t)
			f.initialize(t)
			tt.mutate(t, f)
			if _, err := f.classify(); err == nil {
				t.Fatal("unsafe or malformed state accepted")
			}
		})
	}
}

func TestRootLifecycleRefusalDoesNotMutateEvidence(t *testing.T) {
	requireRootTestEnvironment(t)
	f := newLifecycleFixture(t)
	legacy := f.fs.unmarkedPaths[2]
	if err := os.WriteFile(legacy, []byte("preserve me"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(legacy)
	if err != nil {
		t.Fatal(err)
	}
	dataBefore, _ := os.ReadFile(legacy)
	if _, err := f.classify(); err == nil {
		t.Fatal("legacy evidence accepted")
	}
	after, err := os.Stat(legacy)
	if err != nil {
		t.Fatal(err)
	}
	dataAfter, _ := os.ReadFile(legacy)
	if string(dataAfter) != string(dataBefore) ||
		after.Mode() != before.Mode() || after.Size() != before.Size() ||
		!after.ModTime().Equal(before.ModTime()) {
		t.Fatal("read-only lifecycle refusal changed protected evidence")
	}

	t.Run("bootstrap conflict", func(t *testing.T) {
		f := newLifecycleFixture(t)
		bootstrap := f.seedBootstrap(t, 1)
		conflict := f.fs.unmarkedPaths[1]
		if err := os.WriteFile(conflict, []byte("preserve me"), 0o600); err != nil {
			t.Fatal(err)
		}
		beforeBootstrap, err := os.Stat(bootstrap)
		if err != nil {
			t.Fatal(err)
		}
		beforeConflict, err := os.Stat(conflict)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.classify(); err == nil {
			t.Fatal("conflicting bootstrap state accepted")
		}
		afterBootstrap, err := os.Stat(bootstrap)
		if err != nil {
			t.Fatal(err)
		}
		afterConflict, err := os.Stat(conflict)
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(beforeBootstrap, afterBootstrap) ||
			!os.SameFile(beforeConflict, afterConflict) ||
			!beforeBootstrap.ModTime().Equal(afterBootstrap.ModTime()) ||
			!beforeConflict.ModTime().Equal(afterConflict.ModTime()) {
			t.Fatal("bootstrap refusal changed protected evidence")
		}
	})
}
