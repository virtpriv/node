package host

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/virtpriv/node/internal/paths"
)

const keyVerificationNote = "pending\n"

func markerOwner(info os.FileInfo) (int, error) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("filesystem does not expose numeric ownership")
	}
	return int(st.Uid), nil
}

func validateKeyVerificationMarker(path string, expectedUID int) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	if info.Mode().Perm() != 0o600 {
		return fmt.Errorf("%s mode is %04o, want 0600", path, info.Mode().Perm())
	}
	uid, err := markerOwner(info)
	if err != nil {
		return err
	}
	if uid != expectedUID {
		return fmt.Errorf("%s owner is uid %d, want %d", path, uid, expectedUID)
	}
	return nil
}

func syncMarkerDir(path string) error {
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func ensureKeyVerificationPendingAt(path string, expectedUID int) error {
	if err := validateKeyVerificationMarker(path, expectedUID); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("unsafe key-verification marker: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create key-verification marker: %w", err)
	}
	cleanup := true
	defer func() {
		f.Close()
		if cleanup {
			os.Remove(path)
		}
	}()
	if _, err := f.WriteString(keyVerificationNote); err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	cleanup = false
	return syncMarkerDir(path)
}

func keyVerificationPendingAt(path string, expectedUID int) (bool, error) {
	if err := validateKeyVerificationMarker(path, expectedUID); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("unsafe key-verification marker: %w", err)
	}
	return true, nil
}

func clearKeyVerificationPendingAt(path string, expectedUID int) error {
	pending, err := keyVerificationPendingAt(path, expectedUID)
	if err != nil || !pending {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return syncMarkerDir(path)
}

// SetKeyVerificationPending publishes the installer's first-login requirement.
// Only the installer arms it; runtime verification can only clear it.
func SetKeyVerificationPending(pending bool) error {
	if os.Geteuid() != 0 {
		return errors.New("SSH login verification requires root")
	}
	if pending {
		return ensureKeyVerificationPendingAt(paths.KeyVerificationMarker, 0)
	}
	return clearKeyVerificationPendingAt(paths.KeyVerificationMarker, 0)
}

// KeyVerificationPending reads the root-private workflow marker.
func KeyVerificationPending() (bool, error) {
	if os.Geteuid() != 0 {
		return false, errors.New("SSH login verification requires root")
	}
	return keyVerificationPendingAt(paths.KeyVerificationMarker, 0)
}

// VerifyAdminLogin clears the marker only after real sshd journal evidence.
// Unavailable evidence leaves the marker intact and returns an error.
func VerifyAdminLogin() (pending, verified bool, err error) {
	if os.Geteuid() != 0 {
		return false, false, errors.New("SSH login verification requires root")
	}
	return verifyAdminLoginAt(paths.KeyVerificationMarker, 0, AdminLoginObserved)
}

func verifyAdminLoginAt(path string, uid int, observe func() (bool, error)) (pending, verified bool, err error) {
	pending, err = keyVerificationPendingAt(path, uid)
	if err != nil || !pending {
		return pending, false, err
	}
	observed, err := observe()
	if err != nil || !observed {
		return true, false, err
	}
	if err := clearKeyVerificationPendingAt(path, uid); err != nil {
		return true, false, err
	}
	return false, true, nil
}
