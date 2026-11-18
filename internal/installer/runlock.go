// internal/installer/runlock.go

package installer

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// acquireRunLock serializes the whole base installer on a stable runtime
// object. The lock is never renamed, deleted, truncated, or used as durable
// payload. The kernel releases the lock when the process ends, so there is
// no stale pid file to handle.
func acquireRunLock(dir, path string) (*os.File, error) {
	if err := ensureSecureRuntimeDir(dir); err != nil {
		return nil, err
	}

	fd, err := syscall.Open(path,
		syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, syscall.ENOENT) {
		fd, err = syscall.Open(path,
			syscall.O_RDWR|syscall.O_CREAT|syscall.O_EXCL|
				syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	}
	if err != nil {
		return nil, fmt.Errorf("open install lock %s: %w", path, err)
	}
	f := os.NewFile(uintptr(fd), path)
	if f == nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("open install lock %s: invalid descriptor", path)
	}

	if err := validateOpenRootFile(f, 0o600); err != nil {
		f.Close()
		return nil, fmt.Errorf("unsafe install lock %s: %w", path, err)
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errors.New(
				"another `vpn install` is already running, wait for it to finish")
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return f, nil
}

func ensureSecureRuntimeDir(path string) error {
	err := os.Mkdir(path, 0o700)
	if err != nil && !os.IsExist(err) {
		return fmt.Errorf("create install runtime directory %s: %w", path, err)
	}
	return validateRootDir(path, 0o700)
}

func validateOpenRootFile(f *os.File, mode os.FileMode) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("not a regular file")
	}
	if fi.Mode().Perm() != mode {
		return fmt.Errorf("mode is %04o, want %04o", fi.Mode().Perm(), mode)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("ownership metadata unavailable")
	}
	if st.Uid != 0 || st.Gid != 0 {
		return fmt.Errorf("owner is %d:%d, want 0:0", st.Uid, st.Gid)
	}
	if st.Nlink != 1 {
		return fmt.Errorf("link count is %d, want 1", st.Nlink)
	}
	return nil
}

func validateRootDir(path string, mode os.FileMode) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", path, err)
	}
	if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is not a real directory", path)
	}
	if fi.Mode().Perm() != mode {
		return fmt.Errorf("%s mode is %04o, want %04o",
			path, fi.Mode().Perm(), mode)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != 0 || st.Gid != 0 {
		return fmt.Errorf("%s is not owned by root:root", path)
	}
	return nil
}
