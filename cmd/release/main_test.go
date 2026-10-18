package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/virtpriv/node/internal/component"
	"github.com/virtpriv/node/internal/host"
	"github.com/virtpriv/node/internal/release"
	"github.com/virtpriv/node/internal/update/protocol"
)

// Build and GPG processes are fixtures. Packaging, plan admission, checksum
// verification and the updater's archive extraction run their production code.
func TestReleaseAssetsMatchUpdater(t *testing.T) {
	for _, version := range []string{"0.7.0-rc.1", "0.7.1-rc.1", "0.7.1"} {
		t.Run(version, func(t *testing.T) {
			dir := releaseFixture(t, version)
			args := []string{"-sign", version}
			members := map[string]string{"vpn": "vpn"}
			var plan []byte
			if version != "0.7.0-rc.1" {
				var path string
				path, plan = writePlan(t, planFixture(version))
				args = append(args, path)
				members["update.json"] = "update.json"
			}
			if err := run(args); err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(dir, "release", version)
			if err := release.VerifyChecksum(version, out); err != nil {
				t.Fatal("checksum consumer rejected release:", err)
			}
			archive, err := release.ArchiveName(version)
			if err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(out)
			if err != nil || len(entries) != 3 {
				t.Fatalf("expected exactly three release assets: %v, %v", entries, err)
			}
			extracted := filepath.Join(dir, "extracted")
			if err := host.ExtractUpdateArchive(filepath.Join(out, archive), extracted, members); err != nil {
				t.Fatal("updater rejected archive:", err)
			}
			if got := readFile(t, filepath.Join(extracted, "vpn")); string(got) != "release binary fixture\n" {
				t.Fatal("packaged different binary bytes")
			}
			if plan != nil && !bytes.Equal(readFile(t, filepath.Join(extracted, "update.json")), plan) {
				t.Fatal("packaged plan differs from the reviewed bytes")
			}
			// Bootstrap installs also extract with tar, so the executable mode
			// must survive without the updater supplying its own mode.
			gz, err := gzip.NewReader(bytes.NewReader(readFile(t, filepath.Join(out, archive))))
			if err != nil {
				t.Fatal(err)
			}
			defer gz.Close()
			tr := tar.NewReader(gz)
			for i := 0; ; i++ {
				h, err := tr.Next()
				if err == io.EOF {
					if i != len(members) {
						t.Fatal("archive member count differs from release contract")
					}
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if h.Name == "vpn" && h.Mode&0111 != 0111 {
					t.Fatal("bootstrap binary is not executable")
				}
			}
			if version == "0.7.1" {
				before := map[string][]byte{}
				for _, entry := range entries {
					before[entry.Name()] = readFile(t, filepath.Join(out, entry.Name()))
				}
				if err := run(args); err == nil || !strings.Contains(err.Error(), "already exists") {
					t.Fatal("did not refuse an existing release:", err)
				}
				for name, data := range before {
					if !bytes.Equal(data, readFile(t, filepath.Join(out, name))) {
						t.Fatalf("existing release asset %s changed", name)
					}
				}
			}
		})
	}
}

func TestReleaseRefusesInvalidPlanOrCompilerBeforeBuilding(t *testing.T) {
	for _, test := range []struct {
		name     string
		change   func(*protocol.Manifest)
		compiler string
		want     string
	}{
		{"build mismatch", func(m *protocol.Manifest) { m.Bitcoin.Version = "999.0" }, "", "pinned versions"},
		{"unsupported host step", func(m *protocol.Manifest) { m.HostSteps = []string{"unknown-step"} }, "", "does not implement host step"},
		{"wrong compiler", nil, "go0.0.0", "release build requires"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := releaseFixture(t, "0.7.1")
			m := planFixture("0.7.1")
			if test.change != nil {
				test.change(&m)
			}
			path, _ := writePlan(t, m)
			if test.compiler != "" {
				t.Setenv("RELEASE_TEST_GO_VERSION", test.compiler)
			}
			if err := run([]string{"-sign", "0.7.1", path}); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("wrong refusal: %v", err)
			}
			if trace, err := os.ReadFile(filepath.Join(dir, "trace")); !os.IsNotExist(err) {
				t.Fatalf("built before release admission completed: %q, %v", trace, err)
			}
			assertNoRelease(t, dir, "0.7.1")
		})
	}
}

func TestReleaseFailureDoesNotLeavePublishableOutput(t *testing.T) {
	for failure, boundary := range map[string]string{
		"build":     "build",
		"sign":      "sign",
		"export":    "export",
		"untrusted": "verify",
		"badsig":    "verify",
	} {
		t.Run(failure, func(t *testing.T) {
			dir := releaseFixture(t, "0.7.1")
			old := filepath.Join(dir, "release", "0.7.0")
			if err := os.MkdirAll(old, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(old, "keep"), []byte("existing signed release"), 0644); err != nil {
				t.Fatal(err)
			}
			t.Setenv("RELEASE_TEST_FAILURE", failure)
			if err := run([]string{"-sign", "0.7.1"}); err == nil {
				t.Fatal("accepted a failed build or unverifiable release")
			}
			trace := strings.Fields(string(readFile(t, filepath.Join(dir, "trace"))))
			if !slices.Contains(trace, boundary) {
				t.Fatalf("did not reach the intended %s failure: %v", boundary, trace)
			}
			assertNoRelease(t, dir, "0.7.1")
			if got := readFile(t, filepath.Join(old, "keep")); string(got) != "existing signed release" {
				t.Fatal("failed release changed an earlier release")
			}
		})
	}
}

func TestArchiveBytesDoNotDependOnLocalFileMetadata(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "vpn")
	if err := os.WriteFile(binary, []byte("binary"), 0700); err != nil {
		t.Fatal(err)
	}
	first, second := filepath.Join(dir, "first.tar.gz"), filepath.Join(dir, "second.tar.gz")
	if _, err := writeArchive(first, binary, []byte("plan")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(binary, time.Unix(1, 0), time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(binary, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := writeArchive(second, binary, []byte("plan")); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readFile(t, first), readFile(t, second)) {
		t.Fatal("local metadata changed the signed archive bytes")
	}
}

func planFixture(version string) protocol.Manifest {
	return protocol.Manifest{
		Protocol: protocol.Protocol, Platform: "debian-13-amd64", Version: version,
		Summary: "Test fixture only", MinimumFreeMiB: 2048,
		Sources:   []protocol.Versions{{VPN: "0.7.0", Bitcoin: component.BitcoinCoreVersion, LND: component.LNDVersion}},
		Networks:  []string{"public-signet"},
		Bitcoin:   protocol.Artifact{Version: component.BitcoinCoreVersion, SHA256: component.BitcoinCoreArchiveSHA256},
		LND:       protocol.Artifact{Version: component.LNDVersion, SHA256: component.LNDArchiveSHA256},
		Syncthing: protocol.Artifact{Version: component.SyncthingVersion, SHA256: component.SyncthingArchiveSHA256},
		HostSteps: []string{"lnd-service-v1"},
	}
}

// writePlan keeps the plan outside the checkout, as the release tool refuses
// files Git does not know.
func writePlan(t *testing.T, m protocol.Manifest) (string, []byte) {
	t.Helper()
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	return path, b
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func assertNoRelease(t *testing.T, dir, version string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, "release", version)); !os.IsNotExist(err) {
		t.Fatal("incomplete release output remains", err)
	}
}

func releaseFixture(t *testing.T, version string) string {
	t.Helper()
	mod := readFile(t, filepath.Join("..", "..", "go.mod"))
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	if err := os.WriteFile("go.mod", mod, 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	stubs := map[string]string{
		"go": `#!/bin/sh
set -eu
[ "$GOTOOLCHAIN:$GOENV:$GOWORK:$GOFLAGS:$GOEXPERIMENT:$GOFIPS140:$GO_EXTLINK_ENABLED:$CGO_ENABLED:$GOOS:$GOARCH:$GOAMD64" = local:off:off:::off::0:linux:amd64:v1 ]
command="$1"; shift
case "$command" in
    env) test "$*" = GOVERSION; echo "$RELEASE_TEST_GO_VERSION" ;;
    build)
        readonly_mod=0; trimpath=0; buildvcs=1; output=; flags=; package=
        while [ "$#" -gt 0 ]; do
            case "$1" in
                -mod=readonly) readonly_mod=1 ;;
                -trimpath) trimpath=1 ;;
                -buildvcs=false) buildvcs=0 ;;
                -ldflags=*) flags="${1#-ldflags=}" ;;
                -o) shift; output="$1" ;;
                -o=*) output="${1#-o=}" ;;
                ./cmd/) package="$1" ;;
                *) exit 1 ;;
            esac
            shift
        done
        test "$readonly_mod:$trimpath:$buildvcs:$package" = 1:1:0:./cmd/
        test -n "$output"
        for required in '-s' '-w' "-X main.version=$RELEASE_TEST_VERSION"; do
            case " $flags " in *" $required "*) ;; *) exit 1 ;; esac
        done
        echo build >> "$RELEASE_TEST_TRACE"
        test "${RELEASE_TEST_FAILURE:-}" != build
        printf 'release binary fixture\n' > "$output"
        ;;
    *) exit 1 ;;
esac
`,
		"gpg": `#!/bin/sh
set -eu
mode=; signer=; output=; homedir=; statusfd=; armor=0; first=; second=
while [ "$#" -gt 0 ]; do
    case "$1" in
        --local-user) shift; signer="$1" ;;
        --output) shift; output="$1" ;;
        --homedir) shift; homedir="$1" ;;
        --status-fd) shift; statusfd="$1" ;;
        --armor) armor=1 ;;
        --batch) ;;
        --detach-sign) mode=sign ;;
        --export) mode=export ;;
        --import) mode=import ;;
        --verify) mode=verify ;;
        --list-keys) mode=list ;;
        --with-colons) ;;
        --*) exit 1 ;;
        *) if [ -z "$first" ]; then first="$1"; elif [ -z "$second" ]; then second="$1"; else exit 1; fi ;;
    esac
    shift
done
case "$mode" in
    sign)
        test "$signer:$armor" = "$RELEASE_TEST_SIGNER:1"
        test -f "$first"; test -z "$second"; test -n "$output"
        echo sign >> "$RELEASE_TEST_TRACE"
        cp "$first" "$output"
        test "${RELEASE_TEST_FAILURE:-}" != sign
        ;;
    export)
        test "$first" = "$RELEASE_TEST_SIGNER"; test -n "$output"
        echo export >> "$RELEASE_TEST_TRACE"
        test "${RELEASE_TEST_FAILURE:-}" != export
        printf 'public key fixture\n' > "$output"
        ;;
    list) test -d "$homedir" ;;
    import)
        test -d "$homedir"; test -f "$first"
        echo import >> "$RELEASE_TEST_TRACE"
        ;;
    verify)
        test -d "$homedir"; test "$statusfd" = 1
        cmp "$first" "$second"
        echo verify >> "$RELEASE_TEST_TRACE"
        signer="$RELEASE_TEST_SIGNER"
        if [ "${RELEASE_TEST_FAILURE:-}" = untrusted ]; then signer=UNTRUSTED; fi
        printf '[GNUPG:] NEWSIG\n[GNUPG:] GOODSIG %s fixture\n' "$signer"
        printf '[GNUPG:] VALIDSIG %s 2026-09-28 0 0 4 0 1 10 00 %s\n' "$signer" "$signer"
        if [ "${RELEASE_TEST_FAILURE:-}" = badsig ]; then
            printf '[GNUPG:] BADSIG fixture\n'
            exit 1
        fi
        ;;
    *) exit 1 ;;
esac
`,
	}
	for name, body := range stubs {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	// The release tool builds only from a clean Git checkout. Files the
	// fixtures write while running are ignored, as the repository ignores
	// release/. The developer's own Git settings are left out, so commit
	// signing and hooks are never used and git status behaves the same
	// everywhere. Variables Git sets while running a hook would point every
	// git command here at the developer's repository, so they are cleared.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, name := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR"} {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}
	if err := os.WriteFile(".gitignore", []byte("/release/\n/bin/\n/trace\n/extracted/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"commit", "-q", "-m", "fixture"}} {
		git := exec.Command("git", append([]string{"-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid"}, args...)...)
		if out, err := git.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GOFLAGS", "-race")
	t.Setenv("RELEASE_TEST_TRACE", filepath.Join(dir, "trace"))
	t.Setenv("RELEASE_TEST_FAILURE", "")
	t.Setenv("RELEASE_TEST_VERSION", version)
	t.Setenv("RELEASE_TEST_GO_VERSION", "go1.26.9")
	t.Setenv("RELEASE_TEST_SIGNER", release.SigningFingerprint)
	return dir
}
