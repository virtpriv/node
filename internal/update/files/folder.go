package files

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"syscall"

	"github.com/virtpriv/node/internal/logger"
)

// ArchiveLimit is the largest archive taken from an owner's folder, the limit
// the archive's extraction already has, so a file that fits the extraction is
// never refused.
const ArchiveLimit = 2 << 30

// Folder is a folder the owner fills, such as a releases folder. Nothing in
// it is trusted: a link out of it, or a folder that is itself a link, is never
// followed, and only regular files are read, each up to a limit and copied
// before anything checks it.
type Folder struct {
	root *os.Root
	dir  string
}

// OpenFolder opens dir. It returns nil when dir is missing, and nil with
// notFolder when dir is a link or a file, which is never read.
func OpenFolder(dir string) (f *Folder, notFolder bool, err error) {
	info, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.IsDir() {
		logger.Verify("%s is not a folder, ignored", dir)
		return nil, true, nil
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, false, err
	}
	// OpenRoot follows a link, so the folder opened must be the one checked.
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		root.Close()
		logger.Verify("%s is not a folder, ignored", dir)
		return nil, err == nil, err
	}
	return &Folder{root: root, dir: dir}, false, nil
}

func (f *Folder) Close() { f.root.Close() }

// Has reports that something is at name, whatever it is.
func (f *Folder) Has(name string) bool {
	_, err := f.root.Lstat(name)
	return !errors.Is(err, fs.ErrNotExist)
}

// CopyOut copies name to dest, a new file. It is opened without blocking, so
// a FIFO cannot hold the copy, then required to be a regular file.
func (f *Folder) CopyOut(name, dest string, limit int64) error {
	notRegular := fmt.Errorf("%s in %s is not a regular file.", name, f.dir)
	in, err := f.root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		// A link out of the folder, a link to nothing or a socket.
		logger.Verify("FAIL: %s in %s: %v", name, f.dir, err)
		return notRegular
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return notRegular
	}
	tooLarge := fmt.Errorf("%s in %s is too large.", name, f.dir)
	if info.Size() > limit {
		return tooLarge
	}
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, io.LimitReader(in, limit+1))
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("copy %s from %s: %w", name, f.dir, err)
	}
	// The file may still grow while the owner copies it.
	if n > limit {
		return tooLarge
	}
	return nil
}

// RemoveMatching removes every file whose name match accepts, never through
// a link, and logs each removal and each failure with log. A failure harms
// nothing and is tried again by the next caller.
func (f *Folder) RemoveMatching(match func(name string) bool, log func(format string, args ...any)) {
	entries, err := fs.ReadDir(f.root.FS(), ".")
	if err != nil {
		log("could not read %s: %v", f.dir, err)
		return
	}
	for _, e := range entries {
		if e.IsDir() || !match(e.Name()) {
			continue
		}
		if err := f.root.Remove(e.Name()); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log("could not remove %s from %s: %v", e.Name(), f.dir, err)
			continue
		}
		log("removed %s from %s", e.Name(), f.dir)
	}
}
