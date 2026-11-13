package host

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/update/files"
)

// CreateReleasesFolder makes the owner's releases folder, where the owner may
// put release files the node would otherwise download. A folder or anything
// else already at that name is left as it is, so a resumed install never
// touches what the owner put there.
func CreateReleasesFolder() error {
	dir := paths.ReleasesFolder
	account, err := user.Lookup(paths.AdminUser)
	if err != nil {
		return fmt.Errorf("look up %s: %w", paths.AdminUser, err)
	}
	uid, uidErr := strconv.Atoi(account.Uid)
	gid, gidErr := strconv.Atoi(account.Gid)
	if uidErr != nil || gidErr != nil {
		return fmt.Errorf("account %s has ids %q and %q", paths.AdminUser, account.Uid, account.Gid)
	}
	if err := os.Mkdir(dir, 0o700); errors.Is(err, fs.ErrExist) {
		return nil
	} else if err != nil {
		return err
	}
	// The owner is set on the open folder, never through a link the owner
	// could put at its name in their own home.
	f, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Chown(uid, gid); err != nil {
		return err
	}
	if err := f.Chmod(0o700); err != nil {
		return err
	}
	return files.Sync(filepath.Dir(dir))
}
