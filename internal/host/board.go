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
	dir       string
	gid       int
	haveGroup bool
}

func productionStagingBoard() (stagingBoard, error) {
	if os.Geteuid() != 0 {
		return stagingBoard{}, fmt.Errorf("credential staging requires root")
	}
	gid, ok := vpnGID()
	return stagingBoard{dir: paths.StateDir, gid: gid, haveGroup: ok}, nil
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

// vpnGID resolves the admin group's numeric gid. The group may
// legitimately not exist yet during the bake phase of an image
// build (the admin user is created at first boot); the caller
// falls back to root-only modes and the first-boot staging step
// re-applies ownership.
func vpnGID() (int, bool) {
	g, err := user.LookupGroup(paths.AdminUser)
	if err != nil {
		return 0, false
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return 0, false
	}
	return gid, true
}

// ensureDir creates the board directory with root:vpn
// 0750 (root:root 0700 before the admin group exists).
// Idempotent; re-applies ownership and mode so a bake-phase
// directory is corrected at first boot. As belt and braces on
// top of the root-owned-ancestors guarantee, it refuses to
// operate on anything that is not a real directory owned by
// root.
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
			"%s is not a directory; refusing to stage",
			b.dir)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || st.Uid != 0 {
		return fmt.Errorf(
			"%s is not owned by root; refusing to stage",
			b.dir)
	}
	gid, ok := b.gid, b.haveGroup
	if !ok {
		return os.Chmod(b.dir, 0o700)
	}
	if err := os.Chown(b.dir, 0, gid); err != nil {
		return fmt.Errorf("chown %s: %w", b.dir, err)
	}
	return os.Chmod(b.dir, 0o750)
}

// write writes one staging-board file atomically: same-dir
// temp, explicit chmod (the open mode is filtered by umask),
// chown root:vpn, then rename onto the final path; a reader
// never sees a partial file or a loose mode. Runs as root only.
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
	if gid, ok := b.gid, b.haveGroup; ok {
		if err := tmp.Chown(0, gid); err != nil {
			tmp.Close()
			return fmt.Errorf("stage %s: chown: %w", path, err)
		}
	} else if err := tmp.Chmod(0o600); err != nil {
		// No admin group yet (bake phase): keep it root-only;
		// the first-boot staging step re-owns every board file.
		tmp.Close()
		return fmt.Errorf("stage %s: chmod: %w", path, err)
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
	return nil
}

// FixBoardOwnership re-applies root:vpn 0640 to every existing
// board file and 0750 to the directory. The first-boot staging
// step calls this so files written during an image bake (before
// the admin group existed) become readable once the group does.
func FixBoardOwnership() error {
	b, err := productionStagingBoard()
	if err != nil {
		return err
	}
	return b.fixOwnership()
}

func (b stagingBoard) fixOwnership() error {
	if err := b.ensureDir(); err != nil {
		return err
	}
	gid, ok := b.gid, b.haveGroup
	if !ok {
		return fmt.Errorf(
			"admin group %q does not exist", paths.AdminUser)
	}
	entries, err := os.ReadDir(b.dir)
	if err != nil {
		return fmt.Errorf("list %s: %w", b.dir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		p := filepath.Join(b.dir, e.Name())
		if !e.Type().IsRegular() {
			return fmt.Errorf("%s is not a regular board file; refusing ownership repair", p)
		}
		if err := os.Chown(p, 0, gid); err != nil {
			return fmt.Errorf("chown %s: %w", p, err)
		}
		if err := os.Chmod(p, 0o640); err != nil {
			return fmt.Errorf("chmod %s: %w", p, err)
		}
	}
	return nil
}
