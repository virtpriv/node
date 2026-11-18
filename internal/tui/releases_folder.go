package tui

import (
	"slices"

	tea "charm.land/bubbletea/v2"

	"github.com/virtpriv/node/internal/logger"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/release"
	"github.com/virtpriv/node/internal/update/files"
)

// releasesFolderMsg is what the releases folder holds, read as the owner,
// by name only. The node checks the files themselves at Review.
type releasesFolderMsg struct {
	versions  []string
	notFolder bool
}

// readReleasesFolderCmd reads the folder. The status poll repeats it, so files
// the owner copies in while the TUI is open are offered.
func readReleasesFolderCmd() tea.Msg {
	versions, notFolder := release.FolderVersions(paths.ReleasesFolder)
	return releasesFolderMsg{versions: versions, notFolder: notFolder}
}

// clearReleasesFolderCmd runs at TUI start, then reads the folder. Relaunch
// starts a new TUI, so used files go right after an update, and a removal
// that failed is tried again at the next start.
func clearReleasesFolderCmd(updates nodeUpdates, running string) tea.Cmd {
	return func() tea.Msg {
		clearUsedReleaseFiles(paths.ReleasesFolder, updates, running)
		return readReleasesFolderCmd()
	}
}

// clearUsedReleaseFiles removes vpn's release files in dir the node has used,
// by their exact names, and only after a recorded finish: after a complete
// update not newer than this program, the release files not newer than it.
// Newer versions stay for a coming update, the owner's other files and
// ripsline.asc stay.
func clearUsedReleaseFiles(dir string, updates nodeUpdates, running string) {
	if updates == nil {
		return
	}
	st, err := updates.Status()
	if err != nil || st.Phase != "complete" {
		return
	}
	if newer, err := release.Newer(running, st.Version); err != nil || newer {
		return
	}
	folder, _, err := files.OpenFolder(dir)
	if folder == nil || err != nil {
		return
	}
	defer folder.Close()
	folder.RemoveMatching(func(name string) bool {
		version, ok := release.FileVersion(name)
		if !ok {
			return false
		}
		newer, err := release.Newer(running, version)
		return err == nil && !newer
	}, logger.TUI)
}

// applyReleasesFolder keeps what the folder holds, and logs a folder that is
// not read once, when it is first seen.
func (c *ScreenContext) applyReleasesFolder(msg releasesFolderMsg) {
	if msg.notFolder && !c.FolderNotFolder {
		logger.TUI("the releases folder is not a folder, ignored")
	}
	c.FolderReleases, c.FolderNotFolder = msg.versions, msg.notFolder
}

// inReleasesFolder reports that all three files of version are in the folder.
func (c *ScreenContext) inReleasesFolder(version string) bool {
	return slices.Contains(c.FolderReleases, version)
}

// offeredRelease is the newest stable release newer than this program, from
// GitHub's answer or the releases folder, or empty.
func (c *ScreenContext) offeredRelease() string {
	offered := ""
	for _, v := range append([]string{c.LatestVersion}, c.FolderReleases...) {
		if !release.IsStable(v) {
			continue
		}
		if newer, err := release.Newer(c.Version, v); err != nil || !newer {
			continue
		}
		if offered == "" {
			offered = v
		} else if later, _ := release.Newer(offered, v); later {
			offered = v
		}
	}
	return offered
}
