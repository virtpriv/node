//go:build linux

package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/paths"
)

type publisherFixture struct {
	spec       backupPublisherSpec
	ids        backupPublisherIdentity
	sourcePath string
	stagePath  string
	finalPath  string
}

func newPublisherFixture(t *testing.T, source []byte) publisherFixture {
	t.Helper()
	root := t.TempDir()
	uid, gid := os.Geteuid(), os.Getegid()
	ids := backupPublisherIdentity{
		lndUID: uid, lndGID: gid, backupGID: gid,
	}
	sourceDir := "lnd/mainnet"
	stageDir := "vpn/exports/lnd-backup-stage"
	finalDir := "vpn/exports/lnd-backup"
	policies := map[string]publisherMetadataPolicy{
		"lnd": {
			uid: uid, gid: gid, exactMode: 0700, exact: true,
		},
		sourceDir: {
			uid: uid, gid: gid, exactMode: 0700, exact: true,
		},
		"vpn": {
			uid: uid, gid: gid, exactMode: 0700, exact: true,
		},
		"vpn/exports": {
			uid: uid, gid: gid, exactMode: 0750, exact: true,
		},
		stageDir: {
			uid: uid, gid: gid, exactMode: 0700, exact: true,
		},
		finalDir: {
			uid: uid, gid: gid, exactMode: 0750, exact: true,
		},
	}
	for dir, mode := range map[string]os.FileMode{
		"lnd":         0700,
		sourceDir:     0700,
		"vpn":         0700,
		"vpn/exports": 0750,
		stageDir:      0700,
		finalDir:      0750,
	} {
		full := filepath.Join(root, filepath.FromSlash(dir))
		if err := os.MkdirAll(full, mode); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if err := os.Chmod(full, mode); err != nil {
			t.Fatalf("chmod %s: %v", dir, err)
		}
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(root,
		filepath.FromSlash(sourceDir), backupFileName)
	if err := os.WriteFile(sourcePath, source, 0600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := os.Chmod(sourcePath, 0600); err != nil {
		t.Fatalf("chmod source: %v", err)
	}
	stagePath := filepath.Join(root, filepath.FromSlash(stageDir))
	markerPath := filepath.Join(
		root, filepath.FromSlash(finalDir), paths.ExportReadyMarkerName)
	if err := os.Mkdir(markerPath, 0750); err != nil {
		t.Fatalf("mkdir export marker: %v", err)
	}
	if err := os.Chmod(markerPath, 0750); err != nil {
		t.Fatalf("chmod export marker: %v", err)
	}
	finalPath := filepath.Join(
		root, filepath.FromSlash(finalDir), backupFileName)
	return publisherFixture{
		ids: ids,
		spec: backupPublisherSpec{
			root: root,
			rootPolicy: publisherMetadataPolicy{
				uid: uid, gid: gid,
				exactMode: rootInfo.Mode().Perm(), exact: true,
			},
			sourceDir:     sourceDir,
			stageDir:      stageDir,
			finalDir:      finalDir,
			dirPolicies:   policies,
			sourceDisplay: sourcePath,
			stageDisplay:  stagePath,
			finalDisplay:  finalPath,
		},
		sourcePath: sourcePath,
		stagePath:  stagePath,
		finalPath:  finalPath,
	}
}

func fixedTemp(name string) func() (string, error) {
	return func() (string, error) { return name, nil }
}

func runPublisher(
	t *testing.T, f publisherFixture, hooks publisherHooks,
) error {
	t.Helper()
	if hooks.tempName == nil {
		hooks.tempName = fixedTemp(".channel.backup.tmp-test")
	}
	return publishLNDBackup(f.spec, f.ids, hooks)
}

func assertNoPublisherTemps(t *testing.T, stage string) {
	t.Helper()
	entries, err := os.ReadDir(stage)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".channel.backup.tmp-") {
			t.Errorf("backup export temporary remains: %s", entry.Name())
		}
	}
}

func assertPublished(t *testing.T, f publisherFixture, want []byte) {
	t.Helper()
	got, err := os.ReadFile(f.finalPath)
	if err != nil {
		t.Fatalf("read published backup: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("published bytes %q, want %q", got, want)
	}
	info, err := os.Lstat(f.finalPath)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0640 {
		t.Errorf("published mode/type %s, want regular 0640", info.Mode())
	}
	var st unix.Stat_t
	if err := unix.Stat(f.finalPath, &st); err != nil {
		t.Fatalf("stat published backup: %v", err)
	}
	if int(st.Uid) != f.ids.lndUID || int(st.Gid) != f.ids.backupGID {
		t.Errorf("published uid:gid %d:%d, want %d:%d",
			st.Uid, st.Gid, f.ids.lndUID, f.ids.backupGID)
	}
}

func TestProductionBackupPublisherPathsAreFixed(t *testing.T) {
	ids := backupPublisherIdentity{lndUID: 11, lndGID: 12, backupGID: 13}
	for network, source := range map[string]string{
		"mainnet":       "/var/lib/lnd/data/chain/bitcoin/mainnet/channel.backup",
		"testnet4":      "/var/lib/lnd/data/chain/bitcoin/testnet4/channel.backup",
		"public-signet": "/var/lib/lnd/data/chain/bitcoin/signet/channel.backup",
	} {
		profile, err := config.NetworkConfigFromName(network)
		if err != nil {
			t.Fatal(err)
		}
		spec := productionBackupPublisherSpec(profile.LNDNetwork, ids)
		if got := "/" + spec.sourceDir + "/" + backupFileName; got != source {
			t.Errorf("%s source %q, want %q",
				network, got, source)
		}
		if got := "/" + spec.stageDir; got != "/var/lib/vpn/exports/lnd-backup-stage" {
			t.Errorf("unexpected private stage %q", got)
		}
		if got := "/" + spec.finalDir; got != "/var/lib/vpn/exports/lnd-backup" {
			t.Errorf("unexpected final export %q", got)
		}
	}
	if err := PublishLNDBackup("signet"); err == nil ||
		!strings.Contains(err.Error(), "unknown network") {
		t.Errorf("unsupported network did not fail closed: %v", err)
	}
}

func TestPublishLNDBackupInitialAndReplacement(t *testing.T) {
	f := newPublisherFixture(t, []byte("first-complete-backup"))
	unknown := filepath.Join(f.stagePath, ".channel.backup.tmp-unknown")
	if err := os.WriteFile(unknown, []byte("do not delete"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runPublisher(t, f, publisherHooks{
		tempName: fixedTemp(".channel.backup.tmp-first"),
	}); err != nil {
		t.Fatalf("initial publication: %v", err)
	}
	assertPublished(t, f, []byte("first-complete-backup"))
	if got, err := os.ReadFile(unknown); err != nil ||
		string(got) != "do not delete" {
		t.Fatalf("unknown stage object changed: %q, %v", got, err)
	}

	second := []byte("second-complete-backup-is-longer")
	if err := os.WriteFile(f.sourcePath, second, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f.sourcePath, 0600); err != nil {
		t.Fatal(err)
	}
	if err := runPublisher(t, f, publisherHooks{
		tempName: fixedTemp(".channel.backup.tmp-second"),
	}); err != nil {
		t.Fatalf("replacement publication: %v", err)
	}
	assertPublished(t, f, second)
}

func TestPublishLNDBackupRejectsUnsafeObjects(t *testing.T) {
	t.Run("source symlink", func(t *testing.T) {
		f := newPublisherFixture(t, []byte("source"))
		target := filepath.Join(filepath.Dir(f.sourcePath), "other")
		if err := os.WriteFile(target, []byte("other"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(f.sourcePath); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Base(target), f.sourcePath); err != nil {
			t.Fatal(err)
		}
		if err := runPublisher(t, f, publisherHooks{}); !errors.Is(err, unix.ELOOP) {
			t.Fatalf("source symlink was not refused at open: %v", err)
		}
	})

	t.Run("source ancestor symlink", func(t *testing.T) {
		f := newPublisherFixture(t, []byte("source"))
		mainnet := filepath.Dir(f.sourcePath)
		alternate := filepath.Join(filepath.Dir(mainnet), "alternate")
		if err := os.Rename(mainnet, alternate); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Base(alternate), mainnet); err != nil {
			t.Fatal(err)
		}
		if err := runPublisher(t, f, publisherHooks{}); err == nil {
			t.Fatal("source ancestor symlink accepted")
		}
	})

	t.Run("destination symlink", func(t *testing.T) {
		f := newPublisherFixture(t, []byte("source"))
		target := filepath.Join(filepath.Dir(f.finalPath), "other")
		if err := os.WriteFile(target, []byte("other"), 0640); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, f.finalPath); err != nil {
			t.Fatal(err)
		}
		if err := runPublisher(t, f, publisherHooks{}); err == nil ||
			!strings.Contains(err.Error(), "malformed destination") {
			t.Fatalf("destination symlink result: %v", err)
		}
	})

	t.Run("destination mode", func(t *testing.T) {
		f := newPublisherFixture(t, []byte("source"))
		if err := os.WriteFile(f.finalPath, []byte("old"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(f.finalPath, 0600); err != nil {
			t.Fatal(err)
		}
		if err := runPublisher(t, f, publisherHooks{}); err == nil ||
			!strings.Contains(err.Error(), "mode 0600") {
			t.Fatalf("malformed destination result: %v", err)
		}
	})

	t.Run("source owner", func(t *testing.T) {
		f := newPublisherFixture(t, []byte("source"))
		f.ids.lndUID++
		if err := runPublisher(t, f, publisherHooks{}); err == nil ||
			!strings.Contains(err.Error(), "source "+f.sourcePath+" has uid:gid") {
			t.Fatalf("wrong source owner result: %v", err)
		}
	})
}

func TestPublishLNDBackupRejectsFIFO(t *testing.T) {
	for _, location := range []string{"source", "published"} {
		t.Run(location, func(t *testing.T) {
			// A subprocess bounds a regressed blocking open without leaving a
			// stuck goroutine. Its fixtures belong to the parent's temporary tree.
			if os.Getenv("VPN_TEST_BACKUP_FIFO_CHILD") != "1" {
				executable, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, executable,
					"-test.run=^TestPublishLNDBackupRejectsFIFO$/^"+location+"$")
				cmd.Env = append(os.Environ(), "VPN_TEST_BACKUP_FIFO_CHILD=1",
					"TMPDIR="+t.TempDir())
				output, err := cmd.CombinedOutput()
				if ctx.Err() != nil {
					t.Fatalf("publisher did not reject %s FIFO without a writer: %v\n%s",
						location, ctx.Err(), output)
				}
				if err != nil {
					t.Fatalf("FIFO refusal: %v\n%s", err, output)
				}
				return
			}

			old := []byte("previous complete backup")
			f := newPublisherFixture(t, old)
			if err := runPublisher(t, f, publisherHooks{}); err != nil {
				t.Fatal(err)
			}
			replaceWithFIFO := func(name string) error {
				if err := os.Remove(name); err != nil {
					return err
				}
				return unix.Mkfifo(name, 0600)
			}
			fifoPath := f.sourcePath
			var hooks publisherHooks
			if location == "source" {
				if err := replaceWithFIFO(fifoPath); err != nil {
					t.Fatal(err)
				}
			} else {
				fifoPath = f.finalPath
				hooks.fail = func(point string) error {
					if point == "open-final" {
						return replaceWithFIFO(fifoPath)
					}
					return nil
				}
			}
			if err := runPublisher(t, f, hooks); err == nil ||
				!strings.Contains(err.Error(), "not a regular file") {
				t.Fatalf("FIFO refusal: %v", err)
			}
			if info, err := os.Lstat(fifoPath); err != nil {
				t.Fatal(err)
			} else if info.Mode()&os.ModeNamedPipe == 0 {
				t.Fatalf("refused FIFO was replaced: %s", info.Mode())
			}
			assertNoPublisherTemps(t, f.stagePath)
			if location == "source" {
				assertPublished(t, f, old)
			}
		})
	}
}

func TestPublishLNDBackupCleansOwnedTempOnPrePublishFailures(t *testing.T) {
	points := []string{
		"open-source", "source-metadata-verify", "open-stage",
		"lock-stage", "open-final-directory",
		"destination-metadata-verify", "create-temp", "source-read",
		"temp-write", "temp-chown", "temp-chmod",
		"temp-metadata-verify", "source-stability", "source-reread",
		"source-path-verify", "temp-sync", "temp-close", "source-close",
		"rename",
	}
	for _, existing := range []bool{false, true} {
		for _, point := range points {
			t.Run(fmt.Sprintf("existing=%t/%s", existing, point), func(t *testing.T) {
				f := newPublisherFixture(t, []byte("complete source"))
				old := []byte("previous complete backup")
				if existing {
					if err := os.WriteFile(f.finalPath, old, 0640); err != nil {
						t.Fatal(err)
					}
					if err := os.Chmod(f.finalPath, 0640); err != nil {
						t.Fatal(err)
					}
				}
				sentinel := errors.New("injected " + point)
				err := runPublisher(t, f, publisherHooks{
					fail: func(got string) error {
						if got == point {
							return sentinel
						}
						return nil
					},
				})
				if !errors.Is(err, sentinel) {
					t.Fatalf("failure %q not returned: %v", point, err)
				}
				if existing {
					assertPublished(t, f, old)
				} else if _, statErr := os.Lstat(f.finalPath); !os.IsNotExist(statErr) {
					t.Errorf("final exists after pre-publish failure: %v", statErr)
				}
				assertNoPublisherTemps(t, f.stagePath)
			})
		}
	}
}

func TestPublishLNDBackupReportsPostRenameFailuresWithoutDeletingFinal(t *testing.T) {
	points := []string{
		"open-final", "final-verify", "final-read", "final-close",
		"final-dir-sync", "stage-dir-sync",
	}
	for _, point := range points {
		t.Run(point, func(t *testing.T) {
			content := []byte("complete source")
			f := newPublisherFixture(t, content)
			sentinel := errors.New("injected " + point)
			err := runPublisher(t, f, publisherHooks{
				fail: func(got string) error {
					if got == point {
						return sentinel
					}
					return nil
				},
			})
			if !errors.Is(err, sentinel) {
				t.Fatalf("failure %q not returned: %v", point, err)
			}
			assertPublished(t, f, content)
			assertNoPublisherTemps(t, f.stagePath)
		})
	}
}

func TestPublishLNDBackupCleanupFailureIsVisibleAndScoped(t *testing.T) {
	f := newPublisherFixture(t, []byte("source"))
	unknown := filepath.Join(f.stagePath, "unknown-object")
	if err := os.WriteFile(unknown, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	err := runPublisher(t, f, publisherHooks{
		fail: func(point string) error {
			switch point {
			case "temp-write":
				return errors.New("injected write")
			case "cleanup":
				return errors.New("injected cleanup")
			default:
				return nil
			}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "injected write") ||
		!strings.Contains(err.Error(), "injected cleanup") {
		t.Fatalf("joined failure missing: %v", err)
	}
	if got, readErr := os.ReadFile(unknown); readErr != nil ||
		string(got) != "keep" {
		t.Fatalf("unknown object changed: %q, %v", got, readErr)
	}
	if _, statErr := os.Stat(filepath.Join(
		f.stagePath, ".channel.backup.tmp-test")); statErr != nil {
		t.Fatalf("owned temp absent after injected cleanup failure: %v", statErr)
	}
}

func TestPublishLNDBackupNeverReusesTempCollisions(t *testing.T) {
	f := newPublisherFixture(t, []byte("source"))
	collision := filepath.Join(f.stagePath, ".channel.backup.tmp-collision")
	if err := os.WriteFile(collision, []byte("unknown"), 0600); err != nil {
		t.Fatal(err)
	}
	err := runPublisher(t, f, publisherHooks{
		tempName: fixedTemp(".channel.backup.tmp-collision"),
	})
	if err == nil || !strings.Contains(err.Error(), "exclusive temporary") {
		t.Fatalf("collisions did not fail closed: %v", err)
	}
	if got, readErr := os.ReadFile(collision); readErr != nil ||
		string(got) != "unknown" {
		t.Fatalf("collision object changed: %q, %v", got, readErr)
	}
}

func TestPublishLNDBackupRejectsSourceReplacementRace(t *testing.T) {
	f := newPublisherFixture(t, []byte("opened source"))
	err := runPublisher(t, f, publisherHooks{
		fail: func(point string) error {
			if point != "source-path-verify" {
				return nil
			}
			replacement := f.sourcePath + ".replacement"
			if err := os.WriteFile(
				replacement, []byte("replacement"), 0600); err != nil {
				return err
			}
			if err := os.Chmod(replacement, 0600); err != nil {
				return err
			}
			return os.Rename(replacement, f.sourcePath)
		},
	})
	if err == nil || !strings.Contains(err.Error(), "was replaced") {
		t.Fatalf("source replacement not detected: %v", err)
	}
	assertNoPublisherTemps(t, f.stagePath)
}

func TestPublishLNDBackupSerializesConcurrentPublishers(t *testing.T) {
	f := newPublisherFixture(t, []byte("source"))
	locked := make(chan struct{})
	release := make(chan struct{})
	releasePublisher := sync.OnceFunc(func() { close(release) })
	firstDone := make(chan error, 1)
	t.Cleanup(func() {
		releasePublisher()
		awaitPublisherResult(t, firstDone)
	})
	go func() {
		defer close(firstDone)
		firstDone <- runPublisher(t, f, publisherHooks{
			tempName: fixedTemp(".channel.backup.tmp-first"),
			fail: func(point string) error {
				if point == "before-rename" {
					close(locked)
					<-release
				}
				return nil
			},
		})
	}()
	select {
	case <-locked:
	case err := <-firstDone:
		t.Fatalf("first publisher exited before publication: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("first publisher did not reach locked publication point")
	}
	secondErr := runPublisher(t, f, publisherHooks{
		tempName: fixedTemp(".channel.backup.tmp-second"),
	})
	if secondErr == nil ||
		!strings.Contains(secondErr.Error(), "another LND backup publisher") {
		t.Errorf("concurrent publisher result: %v", secondErr)
	}
	releasePublisher()
	if err := awaitPublisherResult(t, firstDone); err != nil {
		t.Fatalf("first publisher: %v", err)
	}
	assertPublished(t, f, []byte("source"))
}

func TestPublishLNDBackupAtomicObservation(t *testing.T) {
	oldContent := []byte("old-complete")
	newContent := []byte(strings.Repeat("new-complete-", 4096))
	f := newPublisherFixture(t, newContent)
	if err := os.WriteFile(f.finalPath, oldContent, 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f.finalPath, 0640); err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{})
	release := make(chan struct{})
	releasePublisher := sync.OnceFunc(func() { close(release) })
	done := make(chan error, 1)
	t.Cleanup(func() {
		releasePublisher()
		awaitPublisherResult(t, done)
	})
	go func() {
		defer close(done)
		done <- runPublisher(t, f, publisherHooks{
			tempName: fixedTemp(".channel.backup.tmp-atomic"),
			fail: func(point string) error {
				if point == "before-rename" {
					close(ready)
					<-release
				}
				return nil
			},
		})
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("publisher exited before publication: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("publisher did not reach publication point")
	}
	if got, err := os.ReadFile(f.finalPath); err != nil || string(got) != string(oldContent) {
		t.Fatalf("before publication: len=%d err=%v", len(got), err)
	}
	entries, err := os.ReadDir(filepath.Dir(f.finalPath))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != backupFileName &&
			entry.Name() != paths.ExportReadyMarkerName {
			t.Errorf("unexpected synchronized-folder object %q",
				entry.Name())
		}
	}
	stopObserver := make(chan struct{})
	stop := sync.OnceFunc(func() { close(stopObserver) })
	observed := make(chan struct{})
	markObserved := sync.OnceFunc(func() { close(observed) })
	observerDone := make(chan error, 1)
	t.Cleanup(func() {
		stop()
		awaitPublisherResult(t, observerDone)
	})
	go func() {
		defer close(observerDone)
		for {
			select {
			case <-stopObserver:
				observerDone <- nil
				return
			default:
			}
			got, err := os.ReadFile(f.finalPath)
			if err != nil {
				observerDone <- err
				return
			}
			if string(got) != string(oldContent) &&
				string(got) != string(newContent) {
				observerDone <- fmt.Errorf(
					"observed partial content with length %d", len(got))
				return
			}
			markObserved()
			if string(got) == string(newContent) {
				observerDone <- nil
				return
			}
		}
	}()
	select {
	case <-observed:
	case err := <-observerDone:
		t.Fatalf("observer exited before publication: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("observer did not read before publication")
	}
	releasePublisher()
	if err := awaitPublisherResult(t, done); err != nil {
		t.Fatal(err)
	}
	if err := awaitPublisherResult(t, observerDone); err != nil {
		t.Fatal(err)
	}
	assertPublished(t, f, newContent)
	if info, err := os.Stat(filepath.Join(
		filepath.Dir(f.finalPath), paths.ExportReadyMarkerName)); err != nil {
		t.Errorf("export marker was not preserved: %v", err)
	} else if !info.IsDir() {
		t.Errorf("export marker is %s, want directory", info.Mode().Type())
	}
}

func awaitPublisherResult(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("publisher test worker did not finish")
		return nil
	}
}
