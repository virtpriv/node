package host

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/virtpriv/node/internal/paths"
)

// stagingBoard publishes deliberately shared credentials. It does not own
// wallet data, auto-unlock secrets, or the separate backup export.
type stagingBoard struct {
	dir string
	gid int
}

func productionStagingBoard() (stagingBoard, error) {
	if os.Geteuid() != 0 {
		return stagingBoard{}, fmt.Errorf("credential staging requires root")
	}
	gid, err := vpnGID()
	if err != nil {
		return stagingBoard{}, err
	}
	return stagingBoard{dir: paths.StateDir, gid: gid}, nil
}

// writeBoard accepts only the fixed destinations selected by host operations.
// No caller-controlled path is exposed through the helper protocol.
func writeBoard(path string, data []byte) error {
	b, err := productionStagingBoard()
	if err != nil {
		return err
	}
	return b.write(path, data, os.Rename)
}

// StageBitcoindRPCPassword publishes the initial provisioning operation's operator
// credential. LND's separate Bitcoin RPC password is never staged here.
func StageBitcoindRPCPassword(password string) error {
	return writeBoard(paths.StateBitcoindRPCPass, []byte(password+"\n"))
}

// StageSyncthingWebPassword publishes the generated password. Syncthing keeps
// only its hash, so this credential cannot be recovered from daemon config.
func StageSyncthingWebPassword(password string) error {
	return writeBoard(paths.StateSyncthingWebPassword, []byte(password+"\n"))
}

// vpnGID resolves the owner group's numeric gid. The install creates the
// group before anything is staged, so a missing group is an error, never a
// reason to stage files the TUI cannot read.
func vpnGID() (int, error) {
	g, err := user.LookupGroup(paths.AdminUser)
	if err != nil {
		return 0, fmt.Errorf("credential staging needs the %s group: %w", paths.AdminUser, err)
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return 0, fmt.Errorf("group %s has id %q", paths.AdminUser, g.Gid)
	}
	return gid, nil
}

// ensureDir creates the board directory with root:vpn 0750. Safe to
// repeat. It applies ownership and mode again each time. On top of the
// guarantee that its ancestors are root owned, it refuses to operate on
// anything that is not a real directory owned by root.
func (b stagingBoard) ensureDir() error {
	if err := os.MkdirAll(filepath.Dir(b.dir), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(b.dir), err)
	}
	if err := os.MkdirAll(b.dir, 0o750); err != nil {
		return fmt.Errorf("create %s: %w", b.dir, err)
	}
	fi, err := os.Lstat(b.dir)
	if err != nil {
		return fmt.Errorf("stat %s: %w", b.dir, err)
	}
	if !fi.Mode().IsDir() {
		return fmt.Errorf(
			"%s is not a directory, refusing to stage",
			b.dir)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || st.Uid != 0 {
		return fmt.Errorf(
			"%s is not owned by root, refusing to stage",
			b.dir)
	}
	if err := os.Chown(b.dir, 0, b.gid); err != nil {
		return fmt.Errorf("chown %s: %w", b.dir, err)
	}
	return os.Chmod(b.dir, 0o750)
}

// write replaces one staging-board file atomically: a temporary
// in the same folder, an explicit chmod (umask filters the open
// mode), chown root:vpn, a sync, the rename onto the final path,
// then a sync of the folder so the new name survives a power cut.
// A reader never sees a partial file or a loose mode. Runs as
// root only.
func (b stagingBoard) write(path string, data []byte, rename func(string, string) error) error {
	if err := b.ensureDir(); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(b.dir,
		"."+filepath.Base(path)+".tmp-")
	if err != nil {
		return fmt.Errorf("stage %s: %w", path, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op after successful rename

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("stage %s: write: %w", path, err)
	}
	if err := tmp.Chmod(0o640); err != nil {
		tmp.Close()
		return fmt.Errorf("stage %s: chmod: %w", path, err)
	}
	if err := tmp.Chown(0, b.gid); err != nil {
		tmp.Close()
		return fmt.Errorf("stage %s: chown: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("stage %s: sync: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("stage %s: close: %w", path, err)
	}
	if err := rename(tmpPath, filepath.Join(b.dir, filepath.Base(path))); err != nil {
		return fmt.Errorf("stage %s: rename: %w", path, err)
	}
	if err := syncDir(b.dir); err != nil {
		return fmt.Errorf("stage %s: %w", path, err)
	}
	return nil
}
