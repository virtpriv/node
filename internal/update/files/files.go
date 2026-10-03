// Package files provides the durable file operations used by managed updates.
// Paths are chosen by trusted code and must be under owned, non-writable parents.
package files

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

func Check(path string, directory bool) error {
	i, err := os.Lstat(path)
	if err != nil {
		return err
	}
	s, ok := i.Sys().(*syscall.Stat_t)
	if !ok || int(s.Uid) != os.Geteuid() || i.Mode()&os.ModeSymlink != 0 || i.IsDir() != directory || (!directory && !i.Mode().IsRegular()) || i.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("unsafe update path %s", path)
	}
	return nil
}

// Directory checks ancestors before creating anything. Root-owned sticky
// parents are permitted for isolated tests; production uses /var/lib and /run.
func Directory(path string, mode os.FileMode) error {
	parent := filepath.Dir(path)
	if path != parent {
		if err := Directory(parent, 0755); err != nil {
			return err
		}
	}
	i, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if err := os.Mkdir(path, mode); err != nil {
			return err
		}
		return Sync(parent)
	}
	if err != nil {
		return err
	}
	s, ok := i.Sys().(*syscall.Stat_t)
	if !ok || (s.Uid != 0 && int(s.Uid) != os.Geteuid()) || !i.IsDir() || i.Mode()&os.ModeSymlink != 0 || (i.Mode().Perm()&0022 != 0 && i.Mode()&os.ModeSticky == 0) {
		return fmt.Errorf("unsafe update directory %s", path)
	}
	return nil
}

func CheckAncestors(path string) error {
	parent := filepath.Dir(path)
	if parent != path {
		if err := CheckAncestors(parent); err != nil {
			return err
		}
	}
	i, err := os.Lstat(path)
	if err != nil {
		return err
	}
	s, ok := i.Sys().(*syscall.Stat_t)
	if !ok || (s.Uid != 0 && int(s.Uid) != os.Geteuid()) || !i.IsDir() || i.Mode()&os.ModeSymlink != 0 || (i.Mode().Perm()&0022 != 0 && i.Mode()&os.ModeSticky == 0) {
		return fmt.Errorf("unsafe update ancestor %s", path)
	}
	return nil
}

func Sync(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func Write(path string, data []byte, mode os.FileMode) error {
	return Replace(path, mode, func(w io.Writer) error { _, err := w.Write(data); return err })
}

func Replace(path string, mode os.FileMode, write func(io.Writer) error) error {
	if err := Check(filepath.Dir(path), true); err != nil {
		return err
	}
	if err := Check(path, false); err != nil && !os.IsNotExist(err) {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".vpn-update-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := write(f); err != nil {
		return err
	}
	if err := f.Chmod(mode); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	return Sync(filepath.Dir(path))
}

func Copy(source, dest string, mode os.FileMode) error {
	if err := Check(source, false); err != nil {
		return err
	}
	f, err := os.Open(source)
	if err != nil {
		return err
	}
	defer f.Close()
	return Replace(dest, mode, func(w io.Writer) error { _, err := io.Copy(w, f); return err })
}

func Remove(path string) error {
	if err := Check(path, false); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return Sync(filepath.Dir(path))
}

func Hash(path string) (string, error) {
	if err := Check(path, false); err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Lock never removes its inode. Every helper mutation and worker shares it.
func Lock(path string) (func(), error) {
	if err := Directory(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	if err := Check(path, false); err != nil {
		f.Close()
		return nil, err
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, errors.New("another node operation is running")
		}
		return nil, err
	}
	return func() { _ = unix.Flock(fd, unix.LOCK_UN); _ = f.Close() }, nil
}
