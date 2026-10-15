package system

import (
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
)

// folderEntries lists a folder so a test can see that no temporary file was
// left behind next to the target.
func folderEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func onlyEntry(t *testing.T, dir, want string) {
	t.Helper()
	if names := folderEntries(t, dir); len(names) != 1 || names[0] != want {
		t.Fatalf("folder holds %q, want only %q", names, want)
	}
}

// A replaced file ends with exactly the requested content and mode, not the
// mode of the file it replaces nor that of a new temporary file, and nothing
// else is left in its folder.
func TestWriteFileReplacesContentAndMode(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "lnd.conf")
	if err := os.WriteFile(target, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := writeFile(target, []byte("new\n"), 0o640, keepOwner, keepOwner); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new\n" {
		t.Fatalf("content %q, want %q", got, "new\n")
	}
	info, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode() != 0o640 {
		t.Fatalf("mode %v, want -rw-r-----", info.Mode())
	}
	onlyEntry(t, dir, "lnd.conf")
}

// Some targets sit in folders another account owns, which could place a link
// at the target's name. The write replaces the link itself and never writes
// through it to the file it points to.
func TestWriteFileReplacesALinkAtTheTarget(t *testing.T) {
	dir := t.TempDir()
	elsewhere := filepath.Join(t.TempDir(), "shadow")
	if err := os.WriteFile(elsewhere, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "config.xml")
	if err := os.Symlink(elsewhere, target); err != nil {
		t.Fatal(err)
	}

	if err := writeFile(target, []byte("new\n"), 0o640, keepOwner, keepOwner); err != nil {
		t.Fatalf("write: %v", err)
	}

	info, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o640 {
		t.Fatalf("target is %v, want a regular file with mode -rw-r-----", info.Mode())
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "new\n" {
		t.Fatalf("target content %q (%v), want %q", got, err, "new\n")
	}
	if got, err := os.ReadFile(elsewhere); err != nil || string(got) != "keep\n" {
		t.Fatalf("linked file content %q (%v), want it unchanged", got, err)
	}
	onlyEntry(t, dir, "config.xml")
}

// A write that cannot complete returns an error, leaves what was at the
// target as it was and leaves no temporary file behind. A folder at the
// target's name is the failure used here.
func TestWriteFileFailureLeavesTargetAndNoTemporary(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "bitcoin.conf")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := writeFile(target, []byte("new\n"), 0o640, keepOwner, keepOwner); err == nil {
		t.Fatal("write into a folder's name succeeded, want an error")
	}

	info, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatalf("target is %v, want the folder left in place", info.Mode())
	}
	if names := folderEntries(t, target); len(names) != 0 {
		t.Fatalf("folder at the target now holds %q, want it untouched", names)
	}
	onlyEntry(t, dir, "bitcoin.conf")
}

// fileOwner returns the numeric owner and group of path, without following a
// link.
func fileOwner(t *testing.T, path string) (uid, gid int) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	st := info.Sys().(*syscall.Stat_t)
	return int(st.Uid), int(st.Gid)
}

// A file handed to a service account must reach its final name already owned
// by that account. If the owner cannot be set, the old file stays exactly as
// it was, so the service never finds a new file it cannot read.
func TestWriteFileWhoseOwnerCannotBeSetLeavesTarget(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can give a file any owner, so this failure needs another account")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "torrc")
	if err := os.WriteFile(target, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o644); err != nil {
		t.Fatal(err)
	}
	// A group this account is not in, which only root could give.
	groups, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	foreign := 54321
	for slices.Contains(groups, foreign) || foreign == os.Getegid() {
		foreign++
	}

	if err := writeFile(target, []byte("new\n"), 0o640, keepOwner, foreign); err == nil {
		t.Fatal("write with an owner that cannot be set succeeded, want an error")
	}

	if got, err := os.ReadFile(target); err != nil || string(got) != "old\n" {
		t.Fatalf("target content %q (%v), want it unchanged", got, err)
	}
	info, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode() != 0o644 {
		t.Fatalf("mode %v, want -rw-r--r-- unchanged", info.Mode())
	}
	onlyEntry(t, dir, "torrc")
}

// The written file ends with the owner and group asked for, not those of the
// account that wrote it.
func TestWriteFileSetsTheRequestedOwner(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "lnd.conf")
	_, dirGID := fileOwner(t, dir)
	uid, gid := 4321, 4321
	if os.Geteuid() != 0 {
		// Without root, only a group this account is in can be given, and it
		// must differ from the one a new file gets anyway.
		groups, err := os.Getgroups()
		if err != nil {
			t.Fatal(err)
		}
		uid, gid = os.Geteuid(), -1
		for _, g := range groups {
			if g != os.Getegid() && g != dirGID {
				gid = g
				break
			}
		}
		if gid == -1 {
			t.Skip("this account is in no second group to give the file")
		}
	}

	if err := writeFile(target, []byte("new\n"), 0o640, uid, gid); err != nil {
		t.Fatalf("write: %v", err)
	}

	if gotUID, gotGID := fileOwner(t, target); gotUID != uid || gotGID != gid {
		t.Fatalf("owner %d:%d, want %d:%d", gotUID, gotGID, uid, gid)
	}
	info, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode() != 0o640 {
		t.Fatalf("mode %v, want -rw-r-----", info.Mode())
	}
	onlyEntry(t, dir, "lnd.conf")
}
