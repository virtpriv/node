// Package release owns VPN release discovery and verification policy.
package release

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/virtpriv/node/internal/logger"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/system"
)

type githubRelease struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

const (
	versionCacheMaxAge = 24 * time.Hour
	latestReleaseURL   = "https://api.github.com/repos/virtpriv/node/releases/latest"
	latestTries        = 3
	latestTryTimeout   = 10 * time.Second
)

// CheckLatestVersion returns a cached version for up to 24 hours or asks
// GitHub over Tor and may update the cache. Empty means unavailable. A returned
// version does not establish update eligibility or that it is newer than this
// binary.
//
// Each try runs on its own Tor circuit (torsocks -i), so a try that failed on
// one exit, such as an exit past GitHub's hourly limit for its address, is not
// repeated there. The tries stop at the first stable answer.
func CheckLatestVersion() string {
	if cached := readVersionCache(); cached != "" {
		return cached
	}

	if _, err := exec.LookPath("torsocks"); err != nil {
		return ""
	}
	for try := 1; try <= latestTries; try++ {
		output, err := system.RunOutputWithTimeout(latestTryTimeout,
			"torsocks", "-i", "curl", "-sfL", latestReleaseURL)
		if err != nil {
			logger.TUI("release check try %d of %d failed: %v", try, latestTries, err)
			continue
		}
		version := stableReleaseVersion([]byte(output))
		if version == "" {
			logger.TUI("release check try %d of %d found no stable release in GitHub's answer", try, latestTries)
			continue
		}
		logger.TUI("release check found v%s at try %d", version, try)
		writeVersionCache(version)
		return version
	}
	return ""
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
