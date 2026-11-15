package update

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/virtpriv/node/internal/logger"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/release"
	"github.com/virtpriv/node/internal/update/files"
)

// The largest manifest, signature or key copy Prepare takes from the releases
// folder. The archive's is files.ArchiveLimit.
const folderFileLimit = 1 << 20

// folderKeyName is the owner's copy of the release key in the releases
// folder, and folderKeyCopy its private copy in Prepare's work folder.
const (
	folderKeyName = "ripsline.asc"
	folderKeyCopy = "release-key-folder.asc"
)

// copyFromReleasesFolder copies the release files of version from the
// releases folder into work, Prepare's private folder, where they are checked
// as a download would be. fromFolder is false when none of the three files is
// there, and Prepare downloads them. One or two of them is a refusal. keyCopy
// is the private copy of the owner's ripsline.asc, or empty.
//
// The folder belongs to the owner, so nothing in it is trusted (files.Folder).
func copyFromReleasesFolder(version, work string) (fromFolder bool, keyCopy string, err error) {
	folder, _, err := files.OpenFolder(paths.ReleasesFolder)
	if folder == nil || err != nil {
		return false, "", err
	}
	defer folder.Close()

	assets, err := release.AssetsFor(version)
	if err != nil {
		return false, "", err
	}
	names := []string{assets.Archive, assets.Manifest, assets.Signature}
	var missing []string
	for _, name := range names {
		if !folder.Has(name) {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		for _, name := range names {
			limit := int64(folderFileLimit)
			if name == assets.Archive {
				limit = files.ArchiveLimit
			}
			if err := folder.CopyOut(name, filepath.Join(work, name), limit); err != nil {
				return false, "", err
			}
		}
		fromFolder = true
		logger.Verify("release files for v%s taken from the releases folder", version)
	} else if len(missing) < len(names) {
		return false, "", fmt.Errorf("The releases folder is missing %s.", strings.Join(missing, " and "))
	}

	// The key copy counts only when both key downloads fail, so a copy that
	// cannot be read is left out and the downloads decide.
	if folder.Has(folderKeyName) {
		keyCopy = filepath.Join(work, folderKeyCopy)
		if err := folder.CopyOut(folderKeyName, keyCopy, folderFileLimit); err != nil {
			logger.Verify("%v", err)
			os.Remove(keyCopy)
			keyCopy = ""
		}
	}
	return fromFolder, keyCopy, nil
}
