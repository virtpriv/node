// Package release owns VPN release discovery and verification policy.
package release

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/virtualprivatenode/vpn/internal/paths"
	"github.com/virtualprivatenode/vpn/internal/system"
)

type githubRelease struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

const versionCacheMaxAge = 24 * time.Hour

// CheckLatestVersion returns a cached version for up to 24 hours or fetches it
// over Tor and may update the cache. Empty means unavailable; a returned version
// does not establish update eligibility or that it is newer than this binary.
func CheckLatestVersion() string {
	if cached := readVersionCache(); cached != "" {
		return cached
	}

	if _, err := exec.LookPath("torsocks"); err != nil {
		return ""
	}
	output, err := system.RunOutputWithTimeout(10*time.Second,
		"torsocks", "curl", "-sL",
		"https://api.github.com/repos/virtualprivatenode/vpn/releases/latest")
	if err != nil {
		return ""
	}

	version := stableReleaseVersion([]byte(output))
	if version != "" {
		writeVersionCache(version)
	}
	return version
}

// GitHub's latest endpoint normally excludes prereleases. Check the tag too so
// a candidate accidentally published as stable cannot enter normal discovery.
func stableReleaseVersion(data []byte) string {
	var r githubRelease
	if err := json.Unmarshal(data, &r); err != nil || r.Draft || r.Prerelease {
		return ""
	}
	version, ok := strings.CutPrefix(r.TagName, "v")
	if !ok || !IsStable(version) {
		return ""
	}
	return version
}

func readVersionCache() string {
	info, err := os.Stat(paths.VersionCacheFile)
	if err != nil {
		return ""
	}
	if time.Since(info.ModTime()) > versionCacheMaxAge {
		return ""
	}
	data, err := os.ReadFile(paths.VersionCacheFile)
	if err != nil {
		return ""
	}
	version := strings.TrimSpace(string(data))
	if !IsStable(version) {
		return ""
	}
	return version
}

func writeVersionCache(version string) {
	existing := readVersionCache()
	if existing == version {
		return
	}
	os.MkdirAll(paths.VersionCacheDir, 0750)
	os.WriteFile(paths.VersionCacheFile,
		[]byte(version), 0600)
}
