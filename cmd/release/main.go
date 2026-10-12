// Command release builds the VPN release archive and its checksum file from a
// clean Git checkout. With -sign it also signs the checksum file, which only
// the maintainer can do. Anyone can run it without -sign to rebuild a
// published release and compare the archive.
package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/virtpriv/node/internal/artifact"
	"github.com/virtpriv/node/internal/release"
	"github.com/virtpriv/node/internal/update"
	"github.com/virtpriv/node/internal/update/protocol"
	"golang.org/x/mod/modfile"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "release:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	usage := errors.New("usage: GOTOOLCHAIN=local go run ./cmd/release [-sign] VERSION [tested-update-plan.json]")
	flags := flag.NewFlagSet("release", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	signFlag := flags.Bool("sign", false, "sign SHA256SUMS with the release key")
	if err := flags.Parse(args); err != nil {
		return usage
	}
	sign, args := *signFlag, flags.Args()
	if len(args) < 1 || len(args) > 2 || !release.ValidVersion(args[0]) || (len(args) == 2 && strings.HasPrefix(args[1], "-")) {
		return usage
	}
	version := args[0]
	commit, err := cleanCommit()
	if err != nil {
		return err
	}
	archive, err := release.ArchiveName(version)
	if err != nil {
		return err
	}
	var plan []byte
	if len(args) == 2 {
		info, err := os.Stat(args[1])
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > 32<<10 {
			return errors.New("update plan must be a regular file of at most 32 KiB")
		}
		plan, err = os.ReadFile(args[1])
		if err != nil {
			return fmt.Errorf("read update plan: %w", err)
		}
		m, err := protocol.Decode(plan)
		if err != nil {
			return fmt.Errorf("decode update plan: %w", err)
		}
		if err := update.ValidateBuild(m, version); err != nil {
			return err
		}
	}
	if err := checkToolchain(); err != nil {
		return err
	}
	if sign {
		if _, err := exec.LookPath("gpg"); err != nil {
			return err
		}
	}
	if err := os.MkdirAll("release", 0755); err != nil {
		return err
	}
	out := filepath.Join("release", version)
	// Reserve this version exclusively. Existing files are never replaced.
	if err := os.Mkdir(out, 0755); errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%s already exists and is never replaced, check it and remove it before building again", out)
	} else if err != nil {
		return err
	}
	complete := false
	defer func() {
		if !complete {
			if err := os.RemoveAll(out); err != nil {
				fmt.Fprintln(os.Stderr, "remove incomplete release:", err)
			}
		}
	}()
	work, err := os.MkdirTemp(out, ".build-")
	if err != nil {
		return err
	}
	binary := filepath.Join(work, "vpn")
	// Without -buildvcs=false Go records a module version taken from the Git
	// tags it finds, so the same commit builds a different executable
	// depending on which tags are present. The source of a release is
	// identified by its signed tag, not by a stamp inside the executable.
	cmd := goCommand("build", "-mod=readonly", "-trimpath", "-buildvcs=false", "-ldflags=-s -w -X main.version="+version, "-o", binary, "./cmd/")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build VPN: %w", err)
	}
	sum, err := writeArchive(filepath.Join(out, archive), binary, plan)
	if err != nil {
		return fmt.Errorf("package VPN: %w", err)
	}
	sums := filepath.Join(out, "SHA256SUMS")
	line := fmt.Sprintf("%x  %s\n", sum, archive)
	if err := os.WriteFile(sums, []byte(line), 0644); err != nil {
		return err
	}
	if sign {
		if err := signChecksums(sums, work); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(work); err != nil {
		return err
	}
	complete = true
	if !sign {
		fmt.Printf("Release files ready in %s, not signed:\n  %s\n  SHA256SUMS\n", out, archive)
		fmt.Printf("Built from commit %s.\n%s", commit, line)
		fmt.Printf("To compare with a published release, check its SHA256SUMS inside %s, for example with sha256sum --check.\n", out)
		return nil
	}
	fmt.Printf("Release files ready in %s:\n  %s\n  SHA256SUMS\n  SHA256SUMS.asc\n", out, archive)
	fmt.Printf("Built from commit %s.\n%s", commit, line)
	fmt.Printf("Make the signed tag v%s on commit %s, then upload these three files to a draft GitHub release.\n", version, commit)
	if release.IsCandidate(version) {
		fmt.Println("Mark it as a prerelease before publishing for testing.")
	} else {
		fmt.Println("Publish with the prerelease flag for final validation by exact tag. After validation, clear that flag and mark it latest.")
	}
	fmt.Println("Keep published assets unchanged. Use a new version for revisions.")
	return nil
}

// goCommand fixes the Go settings known to change the built program, so the
// builder's own shell and saved Go settings (go env -w) have no effect.
func goCommand(args ...string) *exec.Cmd {
	cmd := exec.Command("go", args...)
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOENV=off", "GOWORK=off", "GOFLAGS=", "GOEXPERIMENT=", "GOFIPS140=off", "GO_EXTLINK_ENABLED=", "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64", "GOAMD64=v1")
	return cmd
}

// cleanCommit returns the commit being built. It refuses a folder that is not
// the top of a Git checkout, and a checkout with changes or with files Git
// does not ignore, so a forgotten edit cannot reach a release.
func cleanCommit() (string, error) {
	top, err := gitOutput("rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("read the Git checkout: %w", err)
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	if !sameDir(top, wd) {
		return "", fmt.Errorf("run from the top folder of the Git checkout, %s", top)
	}
	status, err := gitOutput("status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return "", fmt.Errorf("read the Git checkout: %w", err)
	}
	if status != "" {
		return "", fmt.Errorf("the Git checkout has changes, commit or remove them first:\n%s", status)
	}
	return gitOutput("rev-parse", "--verify", "HEAD^{commit}")
}

func sameDir(a, b string) bool {
	infoA, errA := os.Stat(a)
	infoB, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(infoA, infoB)
}

func gitOutput(args ...string) (string, error) {
	b, err := exec.Command("git", args...).Output()
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok && len(exitErr.Stderr) > 0 {
		return "", errors.New(strings.TrimSpace(string(exitErr.Stderr)))
	}
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(b), "\n"), nil
}

func checkToolchain() error {
	b, err := os.ReadFile("go.mod")
	if err != nil {
		return err
	}
	m, err := modfile.Parse("go.mod", b, nil)
	if err != nil {
		return err
	}
	if m.Go == nil || m.Module == nil || m.Module.Mod.Path != "github.com/virtpriv/node" {
		return errors.New("run from the VPN repository root with its pinned go.mod")
	}
	cmd := goCommand("env", "GOVERSION")
	cmd.Stderr = os.Stderr
	b, err = cmd.Output()
	if err != nil {
		return err
	}
	if got, want := strings.TrimSpace(string(b)), "go"+m.Go.Version; got != want {
		return fmt.Errorf("release build requires %s, found %s", want, got)
	}
	return nil
}

func writeArchive(path, binary string, plan []byte) (sum []byte, err error) {
	in, err := os.Open(binary)
	if err != nil {
		return nil, err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return nil, errors.New("build did not produce a regular, nonempty binary")
	}
	out, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	hash := sha256.New()
	gz := gzip.NewWriter(io.MultiWriter(out, hash))
	tw := tar.NewWriter(gz)
	defer func() {
		err = errors.Join(err, tw.Close(), gz.Close(), out.Close())
		sum = hash.Sum(nil)
	}()
	// Fixed member metadata keeps identical inputs independent of local paths,
	// file timestamps and the maintainer's user/group IDs.
	if err := tw.WriteHeader(&tar.Header{Name: "vpn", Mode: 0755, Size: info.Size(), Typeflag: tar.TypeReg}); err != nil {
		return nil, err
	}
	if _, err := io.Copy(tw, in); err != nil {
		return nil, err
	}
	if plan != nil {
		if err := tw.WriteHeader(&tar.Header{Name: "update.json", Mode: 0644, Size: int64(len(plan)), Typeflag: tar.TypeReg}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(plan); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func signChecksums(sums, work string) error {
	sig := sums + ".asc"
	cmd := exec.Command("gpg", "--local-user", release.SigningFingerprint, "--armor", "--detach-sign", "--output", sig, sums)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sign checksums: %w", err)
	}
	// Reuse the updater's isolated verification policy, including signing
	// subkeys. Only the public key leaves the maintainer's local GPG keyring.
	key := filepath.Join(work, "release-key.asc")
	cmd = exec.Command("gpg", "--batch", "--output", key, "--export", release.SigningFingerprint)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("export release public key: %w", err)
	}
	result, err := artifact.VerifySignature([]string{key}, sig, sums, map[string]bool{release.SigningFingerprint: true})
	if err != nil {
		return err
	}
	if len(result.Revoked) > 0 {
		return errors.New("the release signing key, or the subkey that signed, is revoked")
	}
	if result.Bad || result.Signers != 1 {
		return errors.New("checksum signature did not verify with the pinned release signing key")
	}
	return nil
}
