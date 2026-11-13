package tui

import (
	"errors"
	"io/fs"
	"os"
	"slices"

	tea "charm.land/bubbletea/v2"

	"github.com/virtpriv/node/internal/logger"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/release"
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

// clearReleasesFolderCmd runs at TUI start. After a complete update it
// removes vpn's release files not newer than this program, then reads the
// folder. Relaunch starts a new TUI, so the files go right after an update,
// and a removal that failed is tried again at the next start.
func clearReleasesFolderCmd(updates nodeUpdates, running string) tea.Cmd {
	return func() tea.Msg {
		if updates != nil {
			if st, err := updates.Status(); err == nil && st.Phase == "complete" {
				if newer, err := release.Newer(running, st.Version); err == nil && !newer {
					clearReleasesFolder(paths.ReleasesFolder, running)
				}
			}
		}
		return readReleasesFolderCmd()
	}
}

// clearReleasesFolder removes only the exact names of vpn's release files.
// The owner's other files and ripsline.asc stay.
func clearReleasesFolder(dir, running string) {
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
		return
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return
	}
	defer root.Close()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return
	}
	for _, e := range entries {
		version, ok := release.FileVersion(e.Name())
		if !ok {
			continue
		}
		if newer, err := release.Newer(running, version); err != nil || newer {
			continue
		}
		if err := root.Remove(e.Name()); err != nil && !errors.Is(err, fs.ErrNotExist) {
			logger.TUI("could not remove %s from the releases folder: %v", e.Name(), err)
			continue
		}
		logger.TUI("removed %s from the releases folder", e.Name())
	}
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
