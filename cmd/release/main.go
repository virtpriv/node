// Command release prepares signed VPN release files for separate publication.
package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
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
	if len(args) < 1 || len(args) > 2 || !release.ValidVersion(args[0]) {
		return errors.New("usage: GOTOOLCHAIN=local go run ./cmd/release VERSION [tested-update-plan.json]")
	}
	version := args[0]
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
	if _, err := exec.LookPath("gpg"); err != nil {
		return err
	}
	if err := os.MkdirAll("release", 0755); err != nil {
		return err
	}
	out := filepath.Join("release", version)
	// Reserve this version exclusively. Existing signed files are never replaced.
	if err := os.Mkdir(out, 0755); err != nil {
		return fmt.Errorf("create output directory (must not already exist): %w", err)
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
	cmd := goCommand("build", "-mod=readonly", "-trimpath", "-ldflags=-s -w -X main.version="+version, "-o", binary, "./cmd/")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build VPN: %w", err)
	}
	sum, err := writeArchive(filepath.Join(out, archive), binary, plan)
	if err != nil {
		return fmt.Errorf("package VPN: %w", err)
	}
	sums := filepath.Join(out, "SHA256SUMS")
	if err := os.WriteFile(sums, fmt.Appendf(nil, "%x  %s\n", sum, archive), 0644); err != nil {
		return err
	}
	if err := signChecksums(sums, work); err != nil {
		return err
	}
	if err := os.RemoveAll(work); err != nil {
		return err
	}
	complete = true
	fmt.Printf("Release files ready in %s:\n  %s\n  SHA256SUMS\n  SHA256SUMS.asc\n", out, archive)
	fmt.Printf("Verify the binary against signed source tag v%s, then upload these three files to a draft GitHub release.\n", version)
	if release.IsCandidate(version) {
		fmt.Println("Mark it as a prerelease before publishing for testing.")
	} else {
		fmt.Println("Publish with the prerelease flag for final validation by exact tag; after validation, clear that flag and mark it latest.")
	}
	fmt.Println("Keep published assets unchanged. Use a new version for revisions.")
	return nil
}

func goCommand(args ...string) *exec.Cmd {
	cmd := exec.Command("go", args...)
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOWORK=off", "GOFLAGS=", "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64", "GOAMD64=v1")
	return cmd
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
		return fmt.Errorf("release build requires %s; found %s", want, got)
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
