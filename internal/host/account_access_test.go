package host

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/virtualprivatenode/vpn/internal/accountaccess"
	"golang.org/x/sys/unix"
)

const discoveryKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEB laptop"

func accountFixture(t *testing.T) (*accountObserver, string) {
	t.Helper()
	dir := t.TempDir()
	for _, path := range []string{"etc", "srv/deploy/.ssh"} {
		if err := os.MkdirAll(filepath.Join(dir, path), 0700); err != nil {
			t.Fatal(err)
		}
	}
	uid := os.Geteuid()
	passwd := fmt.Sprintf("deploy:x:%d:1000::/srv/deploy:/bin/bash\n", uid)
	for path, data := range map[string]string{
		"etc/passwd": passwd, "etc/group": "primary:x:1000:\nsudo:x:27:deploy\n",
		"srv/deploy/.ssh/authorized_keys": discoveryKey + "\ncommand=\"false\" " + discoveryKey + "\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, path), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return &accountObserver{root: root, systemUID: uint32(uid)}, dir
}

func TestAccountDiscoveryUsesLocalIdentityAndReportsIncompleteSources(t *testing.T) {
	o, dir := accountFixture(t)
	accounts, err := o.accounts()
	if err != nil || len(accounts) != 1 {
		t.Fatalf("inventory: %v %v", accounts, err)
	}
	a := accounts[0]
	// Debian's sync account has /bin/sync, not nologin, as its shell.
	// It must remain visible without making /bin a key-discovery source.
	passwd := fmt.Sprintf("deploy:x:%d:1000::/srv/deploy:/bin/bash\nsync:x:4:65534::/bin:/bin/sync\n", a.UID)
	if err := os.WriteFile(filepath.Join(dir, "etc/passwd"), []byte(passwd), 0600); err != nil {
		t.Fatal(err)
	}
	observed, err := o.accounts()
	if err != nil || len(observed) != 2 {
		t.Fatalf("system identity inventory: %v %v", observed, err)
	}
	for _, account := range observed {
		if account.Name == "sync" && account.KeyDiscoverySupported() {
			t.Fatal("system command became an interactive key source")
		}
	}
	source := o.keys(a)
	if source.Problem != "" || len(source.Keys) != 1 || source.Excluded != 1 || source.User != "deploy" {
		t.Fatalf("discovery: %+v", source)
	}
	// An arbitrary directory is not an account, and an absent file is distinct
	// from an unsafe file. Neither may invent a selectable key.
	if err := os.MkdirAll(filepath.Join(dir, "home/phantom/.ssh"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "srv/deploy/.ssh/authorized_keys")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got := o.keys(a); got.Problem != "" || len(got.Keys) != 0 {
		t.Fatalf("missing: %+v", got)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if got := o.keys(a); got.Problem == "" {
		t.Fatal("directory became an empty successful observation")
	}
	if _, err := o.account(accountaccess.Ref{Name: "phantom", UID: a.UID}); err == nil {
		t.Fatal("accepted directory as account")
	}
	if _, err := o.account(accountaccess.Ref{Name: a.Name, UID: a.UID + 1}); err == nil {
		t.Fatal("accepted changed account identity")
	}
}

func TestKeyDiscoveryRefusesRedirectedSpecialAndOversizedFiles(t *testing.T) {
	for _, attack := range []string{"file symlink", "parent symlink", "fifo", "hardlink", "writable", "oversized"} {
		t.Run(attack, func(t *testing.T) {
			o, dir := accountFixture(t)
			accounts, err := o.accounts()
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "srv/deploy/.ssh/authorized_keys")
			switch attack {
			case "file symlink":
				if err := os.Rename(path, path+".other"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("authorized_keys.other", path); err != nil {
					t.Fatal(err)
				}
			case "parent symlink":
				parent := filepath.Dir(path)
				if err := os.Rename(parent, parent+".other"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(".ssh.other", parent); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := unix.Mkfifo(path, 0600); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(path, path+".other"); err != nil {
					t.Fatal(err)
				}
			case "writable":
				if err := os.Chmod(path, 0666); err != nil {
					t.Fatal(err)
				}
			case "oversized":
				if err := os.WriteFile(path, []byte(strings.Repeat("#", keyFileLimit+1)), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if got := o.keys(accounts[0]); got.Problem == "" || len(got.Keys) != 0 {
				t.Fatalf("unsafe source: %+v", got)
			}
		})
	}
}

func TestAccountInventoryRefusesAmbiguousOrMalformedIdentity(t *testing.T) {
	for _, data := range []string{
		"deploy:x:1000:1000::/home/deploy:/bin/bash\ndeploy:x:1001:1001::/other:/bin/bash\n",
		"deploy:x:bad:1000::/home/deploy:/bin/bash\n",
		"../etc:x:1000:1000::/home/deploy:/bin/bash\n",
		"deploy:x:1000:1000::relative:/bin/bash\n",
	} {
		o, dir := accountFixture(t)
		if err := os.WriteFile(filepath.Join(dir, "etc/passwd"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := o.accounts(); err == nil {
			t.Fatalf("accepted %q", data)
		}
	}
}
