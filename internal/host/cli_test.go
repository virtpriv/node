package host

import (
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/virtpriv/node/internal/config"
)

// The shortcuts file belongs to the owner, whether vpn creates it or the
// account came with one, and the shortcuts are written once.
func TestShortcutsFileBelongsToTheOwner(t *testing.T) {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Skip("owner tests require Linux root")
	}
	owner, err := user.Lookup("nobody")
	if err != nil {
		t.Skip("no nobody account")
	}
	if _, err := user.LookupGroup("nogroup"); err != nil {
		t.Skip("no nogroup group")
	}
	uid, _ := strconv.Atoi(owner.Uid)
	cfg := config.Default()
	cfg.Network = config.NetworkPublicSignet

	check := func(t *testing.T, path, keep string) {
		t.Helper()
		for range 2 {
			if err := setupNodeCLI(path, "nobody", "nogroup", cfg); err != nil {
				t.Fatal(err)
			}
		}
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if st := fi.Sys().(*syscall.Stat_t); int(st.Uid) != uid {
			t.Fatalf("owner %d, want %d", st.Uid, uid)
		}
		data, _ := os.ReadFile(path)
		text := string(data)
		if !strings.HasPrefix(text, keep) {
			t.Fatal("the account's own lines were not kept first")
		}
		if strings.Count(text, "bitcoin-cli()") != 1 || strings.Count(text, "lncli()") != 1 {
			t.Fatal("the shortcuts are not there exactly once")
		}
	}

	t.Run("missing", func(t *testing.T) {
		check(t, filepath.Join(t.TempDir(), ".bashrc"), "")
	})
	t.Run("from the account", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), ".bashrc")
		if err := os.WriteFile(path, []byte("# the account's own\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(path, uid, uid); err != nil {
			t.Fatal(err)
		}
		check(t, path, "# the account's own\n")
	})
	t.Run("a link is refused", func(t *testing.T) {
		dir := t.TempDir()
		secret := filepath.Join(dir, "secret")
		if err := os.WriteFile(secret, []byte("root only\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, ".bashrc")
		if err := os.Symlink(secret, path); err != nil {
			t.Fatal(err)
		}
		if err := setupNodeCLI(path, "nobody", "nogroup", cfg); err == nil {
			t.Fatal("a .bashrc that is a link was used")
		}
		if fi, err := os.Lstat(path); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Fatal("the link was replaced")
		}
		if data, _ := os.ReadFile(secret); string(data) != "root only\n" {
			t.Fatal("the link's target changed")
		}
	})
}
