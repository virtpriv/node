package host

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/virtpriv/node/internal/component"
	"github.com/virtpriv/node/internal/logger"
	"github.com/virtpriv/node/internal/system"
	"github.com/virtpriv/node/internal/update/files"
	"github.com/virtpriv/node/internal/update/protocol"
)

// ArchiveInFolder reports that the archive of c at version is in folder, by
// name only. The install names its step by it before the step runs.
func ArchiveInFolder(c protocol.Component, version, folder string) bool {
	f, _, err := files.OpenFolder(folder)
	if f == nil || err != nil {
		return false
	}
	defer f.Close()
	return f.Has(component.ArchiveName(c, version))
}

// TakeArchive puts the archive of c at version into workDir: the owner's copy
// from folder when one is there, otherwise a download through Tor. from is
// the folder the archive came from, or empty for a download. A file in the
// folder that cannot be read safely stops the step, it is never replaced by a
// download. Nothing is trusted before CheckArchive.
func TakeArchive(c protocol.Component, version, folder, workDir string) (from string, err error) {
	name := component.ArchiveName(c, version)
	dest := filepath.Join(workDir, name)
	// An update's Retry stages again into its earlier work folder.
	if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	f, _, err := files.OpenFolder(folder)
	if err != nil {
		return "", err
	}
	if f != nil {
		defer f.Close()
		if f.Has(name) {
			if err := f.CopyOut(name, dest, files.ArchiveLimit); err != nil {
				return "", err
			}
			logger.Verify("%s %s taken from %s", component.Name(c), version, folder)
			return folder, nil
		}
	}
	return "", system.DownloadRequireTor(component.ArchiveURL(c, version), dest)
}

// CheckArchive compares the archive of c at version in workDir with want, the
// SHA-256 this vpn release pins for it, before anything is unpacked. The hash
// is the only check of these programs on a node: the maintainer checked
// upstream's signatures when the hash was pinned. from is TakeArchive's.
func CheckArchive(c protocol.Component, version, want, workDir, from string) error {
	name := component.ArchiveName(c, version)
	got, err := files.Hash(filepath.Join(workDir, name))
	if err != nil {
		logger.Verify("FAIL: %s archive: %v", component.Name(c), err)
		return fmt.Errorf("checksum failed: %w", err)
	}
	if got != want {
		logger.Verify("FAIL: %s archive differs from the hash pinned in this VPN release", component.Name(c))
		if from != "" {
			return fmt.Errorf("%s in %s does not match the hash this vpn release pins.", name, from)
		}
		return fmt.Errorf("the downloaded %s does not match the hash this vpn release pins.", name)
	}
	logger.Verify("OK %s archive matches the pinned hash", component.Name(c))
	return nil
}

// CheckPinnedArchive is CheckArchive for the version and hash this release
// pins, as the install and the Syncthing add-on use them.
func CheckPinnedArchive(c protocol.Component, workDir, from string) error {
	version, want := component.Pinned(c)
	return CheckArchive(c, version, want, workDir, from)
}

// InstallPinnedPrograms installs the programs of c's pinned archive in workDir
// after CheckPinnedArchive succeeds. The caller owns the private workspace
// and lifecycle admission.
func InstallPinnedPrograms(c protocol.Component, workDir string) error {
	version, _ := component.Pinned(c)
	return installPrograms(c, version, workDir)
}

// RemoveUsedArchives removes from folder the archives of cs whose version is
// the pinned one or older, by their exact names. It runs only after a
// recorded finish. A removal that fails is logged and harms nothing.
func RemoveUsedArchives(folder string, cs ...protocol.Component) {
	f, _, err := files.OpenFolder(folder)
	if f == nil || err != nil {
		return
	}
	defer f.Close()
	f.RemoveMatching(func(name string) bool {
		c, used := component.UsedArchive(name)
		return used && slices.Contains(cs, c)
	}, logger.Install)
}
