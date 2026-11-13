package update

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/virtpriv/node/internal/logger"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/release"
)

// The largest files Prepare copies from the releases folder. The archive gets
// the limit its extraction already has, so a file that fits the extraction is
// never refused here.
const (
	folderArchiveLimit = 2 << 30
	folderFileLimit    = 1 << 20
)

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
// The folder belongs to the owner, so nothing in it is trusted. A link out of
// it, or a folder that is itself a link, is never followed, and only regular
// files are read, each up to its limit.
func copyFromReleasesFolder(version, work string) (fromFolder bool, keyCopy string, err error) {
	root, err := openReleasesFolder(paths.ReleasesFolder)
	if root == nil || err != nil {
		return false, "", err
	}
	defer root.Close()

	assets, err := release.AssetsFor(version)
	if err != nil {
		return false, "", err
	}
	names := []string{assets.Archive, assets.Manifest, assets.Signature}
	var missing []string
	for _, name := range names {
		if _, err := root.Lstat(name); errors.Is(err, fs.ErrNotExist) {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		for _, name := range names {
			limit := int64(folderFileLimit)
			if name == assets.Archive {
				limit = folderArchiveLimit
			}
			if err := copyFolderFile(root, name, filepath.Join(work, name), limit); err != nil {
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
	if _, err := root.Lstat(folderKeyName); err == nil {
		keyCopy = filepath.Join(work, folderKeyCopy)
		if err := copyFolderFile(root, folderKeyName, keyCopy, folderFileLimit); err != nil {
			logger.Verify("%v", err)
			os.Remove(keyCopy)
			keyCopy = ""
		}
	}
	return fromFolder, keyCopy, nil
}

// openReleasesFolder opens the folder, or returns nil when it is missing or
// is not a real folder.
func openReleasesFolder(dir string) (*os.Root, error) {
	info, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		logger.Verify("the releases folder is not a folder, ignored")
		return nil, nil
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	// OpenRoot follows a link, so the folder opened must be the one checked.
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		root.Close()
		logger.Verify("the releases folder is not a folder, ignored")
		return nil, err
	}
	return root, nil
}

// copyFolderFile copies one file from the releases folder to dest, a new
// file. It is opened without blocking, so a FIFO cannot hold Prepare, then
// required to be a regular file.
func copyFolderFile(root *os.Root, name, dest string, limit int64) error {
	notRegular := fmt.Errorf("%s in the releases folder is not a regular file.", name)
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		// A link out of the folder, a link to nothing or a socket.
		logger.Verify("FAIL: %s in the releases folder: %v", name, err)
		return notRegular
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return notRegular
	}
	if info.Size() > limit {
		return fmt.Errorf("%s in the releases folder is too large.", name)
	}
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, io.LimitReader(f, limit+1))
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	// The file may still grow while the owner copies it.
	if n > limit {
		return fmt.Errorf("%s in the releases folder is too large.", name)
	}
	return nil
}
