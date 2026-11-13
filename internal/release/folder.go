package release

import (
	"os"
	"slices"
	"strings"

	"golang.org/x/mod/semver"
)

// FileVersion returns the version a release file name belongs to, for the
// exact names AssetsFor gives and nothing else.
func FileVersion(name string) (string, bool) {
	rest, ok := strings.CutPrefix(name, "vpn-")
	if !ok {
		return "", false
	}
	for _, suffix := range []string{"-x86_64.tar.gz", "-manifest.txt.asc", "-manifest.txt"} {
		if version, ok := strings.CutSuffix(rest, suffix); ok && ValidVersion(version) {
			return version, true
		}
	}
	return "", false
}

// FolderVersions lists the versions whose three release files are all in
// dir, newest first, by name only. Prepare checks the files themselves.
// notFolder reports a dir that exists but is a link or a file, which is never
// read.
func FolderVersions(dir string) (versions []string, notFolder bool) {
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, false
	}
	if !info.IsDir() {
		return nil, true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, false
	}
	names := make(map[string]bool, len(entries))
	for _, e := range entries {
		names[e.Name()] = true
	}
	for _, e := range entries {
		version, ok := FileVersion(e.Name())
		if !ok || slices.Contains(versions, version) {
			continue
		}
		assets, _ := AssetsFor(version)
		if names[assets.Archive] && names[assets.Manifest] && names[assets.Signature] {
			versions = append(versions, version)
		}
	}
	slices.SortFunc(versions, func(a, b string) int { return semver.Compare("v"+b, "v"+a) })
	return versions, false
}
