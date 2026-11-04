package host

import (
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/virtpriv/node/internal/paths"
)

// Each case puts the files apt reads in a test folder and stands in for
// Debian's tools with their output formats, as Debian 13's apt 3.0.3 and
// dpkg print them. The node checks show the rest on a real machine.

const (
	testKeyring    = "trixie archive keys"
	testRemoved    = "removed keys"
	testTrustedAsc = "trixie stable key"
)

// fakeAptMachine is a test folder holding the files apt reads, with the
// stand-in answers of Debian's tools.
type fakeAptMachine struct {
	t       *testing.T
	root    string
	dump    []string          // apt-config dump lines beyond Debian's defaults
	folders map[string]string // apt-config shell answers that differ from Debian's
	targets []fakeTarget
	pkgs    []string          // dpkg-query lines, each status, package and version
	env     []string          // systemctl show-environment lines
	timers  map[string]string // the nightly timers' UnitFileState
}

type fakeTarget struct {
	entry, uri, suite, component, of string
	allow                            string // an Allow-* field apt prints as true
}

func newFakeApt(t *testing.T) *fakeAptMachine {
	t.Helper()
	m := &fakeAptMachine{t: t, root: t.TempDir(), folders: map[string]string{}}
	m.file("/usr/share/keyrings/debian-archive-keyring.pgp", testKeyring)
	m.file("/usr/share/keyrings/debian-archive-removed-keys.pgp", testRemoved)
	m.link("/usr/share/keyrings/debian-archive-keyring.gpg", "debian-archive-keyring.pgp")
	m.link("/usr/share/keyrings/debian-archive-removed-keys.gpg", "debian-archive-removed-keys.pgp")
	m.file("/var/lib/dpkg/info/debian-archive-keyring.md5sums",
		md5hex(testKeyring)+"  usr/share/keyrings/debian-archive-keyring.pgp\n"+
			md5hex(testRemoved)+"  usr/share/keyrings/debian-archive-removed-keys.pgp\n")
	m.file("/etc/apt/trusted.gpg.d/debian-archive-trixie-stable.asc", testTrustedAsc)
	m.file("/var/lib/dpkg/status", "Package: debian-archive-keyring\nStatus: install ok installed\nVersion: 2025.1\nConffiles:\n"+
		" /etc/apt/trusted.gpg.d/debian-archive-trixie-stable.asc "+md5hex(testTrustedAsc)+"\n"+
		" /etc/apt/trusted.gpg.d/debian-archive-bookworm-stable.asc 0123456789abcdef0123456789abcdef obsolete\n\n")
	m.pkgs = []string{"installed tor 0.4.9.15-1~deb13u1", "installed debian-archive-keyring 2025.1"}
	m.env = []string{"LANG=C.UTF-8", "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin"}
	m.timers = map[string]string{"apt-daily.timer": "enabled", "apt-daily-upgrade.timer": "enabled",
		"apt-daily.service": "static", "apt-daily-upgrade.service": "static"}
	return m
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func (m *fakeAptMachine) path(p string) string { return filepath.Join(m.root, p) }

func (m *fakeAptMachine) file(path, content string) {
	m.t.Helper()
	if err := os.MkdirAll(filepath.Dir(m.path(path)), 0o755); err != nil {
		m.t.Fatal(err)
	}
	if err := os.WriteFile(m.path(path), []byte(content), 0o644); err != nil {
		m.t.Fatal(err)
	}
}

func (m *fakeAptMachine) link(path, target string) {
	m.t.Helper()
	if err := os.MkdirAll(filepath.Dir(m.path(path)), 0o755); err != nil {
		m.t.Fatal(err)
	}
	if err := os.Symlink(target, m.path(path)); err != nil {
		m.t.Fatal(err)
	}
}

// setting writes an apt settings file and, for the stand-in, the line apt
// would then print.
func (m *fakeAptMachine) setting(file, line string) {
	m.file(file, line+"\n")
	m.dump = append(m.dump, line)
}

// debianImage sets up Debian's official cloud image: one deb822 file with
// Debian's keyring, backports listed.
func (m *fakeAptMachine) debianImage() {
	m.file("/etc/apt/sources.list.d/debian.sources", `Types: deb
URIs: mirror+file:///etc/apt/mirrors/debian.list
Suites: trixie trixie-updates trixie-backports
Components: main
Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg

Types: deb
URIs: mirror+file:///etc/apt/mirrors/debian-security.list
Suites: trixie-security
Components: main
Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg
`)
	f := "/etc/apt/sources.list.d/debian.sources"
	m.targets = []fakeTarget{
		{entry: f + ":1", uri: "mirror+file:/etc/apt/mirrors/debian.list/", suite: "trixie", component: "main", of: "deb"},
		{entry: f + ":1", uri: "mirror+file:/etc/apt/mirrors/debian.list/", suite: "trixie-updates", component: "main", of: "deb"},
		{entry: f + ":1", uri: "mirror+file:/etc/apt/mirrors/debian.list/", suite: "trixie-backports", component: "main", of: "deb"},
		{entry: f + ":2", uri: "mirror+file:/etc/apt/mirrors/debian-security.list/", suite: "trixie-security", component: "main", of: "deb"},
	}
}

// installerImage sets up what Debian's installer writes: one-line entries
// without Signed-By, deb-src lines, non-free-firmware.
func (m *fakeAptMachine) installerImage() {
	m.file("/etc/apt/sources.list", `#deb cdrom:[Debian GNU/Linux 13.1.0 _Trixie_ - Official amd64 NETINST]/ trixie contrib main non-free-firmware

deb http://deb.debian.org/debian/ trixie main non-free-firmware
deb-src http://deb.debian.org/debian/ trixie main non-free-firmware

deb http://security.debian.org/debian-security trixie-security main non-free-firmware
deb-src http://security.debian.org/debian-security trixie-security main non-free-firmware
`)
	f := "/etc/apt/sources.list"
	for _, c := range []string{"main", "non-free-firmware"} {
		m.targets = append(m.targets,
			fakeTarget{entry: f + ":3", uri: "http://deb.debian.org/debian/", suite: "trixie", component: c, of: "deb"},
			fakeTarget{entry: f + ":4", uri: "http://deb.debian.org/debian/", suite: "trixie", component: c, of: "deb-src"},
			fakeTarget{entry: f + ":6", uri: "http://security.debian.org/debian-security/", suite: "trixie-security", component: c, of: "deb"},
			fakeTarget{entry: f + ":7", uri: "http://security.debian.org/debian-security/", suite: "trixie-security", component: c, of: "deb-src"})
	}
}

// oneSource sets up a single one-line source on Debian's main archive.
func (m *fakeAptMachine) oneSource(line string) {
	m.file(aptFileSources, line+"\n")
	m.targets = []fakeTarget{{entry: aptFileSources + ":1", uri: "http://deb.debian.org/debian/", suite: "trixie", component: "main", of: "deb"}}
}

func (m *fakeAptMachine) baseline() AptBaseline {
	return aptHost{root: m.root, run: m.run}.baseline()
}

var debianDumpDefaults = []string{
	`Dir "/";`,
	`Dir::Etc "etc/apt/";`,
	`Dir::Bin::methods "/usr/lib/apt/methods";`,
	`Binary::apt-cdrom::APT::Internal::OpProgress::EraseLines "0";`,
	`Acquire::AllowInsecureRepositories "0";`,
	`Acquire::AllowWeakRepositories "0";`,
	`Acquire::AllowDowngradeToInsecureRepositories "0";`,
}

func (m *fakeAptMachine) run(name string, args ...string) (string, error) {
	switch name {
	case "apt-config":
		if args[0] == "shell" {
			// Debian's folders, resolved as apt-config shell prints them.
			var lines []string
			for i := 1; i+1 < len(args); i += 2 {
				key := strings.Split(args[i+1], "/")[0]
				value, ok := m.folders[key]
				if !ok {
					value = aptDefaultFolders[key]
				}
				if value != "" {
					lines = append(lines, args[i]+"='"+value+"'")
				}
			}
			return strings.Join(lines, "\n"), nil
		}
		return strings.Join(append(append([]string{}, debianDumpDefaults...), m.dump...), "\n"), nil
	case "apt-get":
		var b strings.Builder
		for _, tg := range m.targets {
			fmt.Fprintf(&b, "MetaKey: x\nShortDesc: Packages\nRepo-URI: %s\nRelease: %s\n", tg.uri, tg.suite)
			if tg.component != "" {
				fmt.Fprintf(&b, "Component: %s\n", tg.component)
			}
			if tg.allow != "" {
				fmt.Fprintf(&b, "%s: true\n", tg.allow)
			}
			fmt.Fprintf(&b, "Sourcesentry: %s\nTarget-Of: %s\n\n", tg.entry, tg.of)
		}
		return strings.TrimSpace(b.String()), nil
	case "apt-mark":
		return "", nil
	case "dpkg-query":
		return strings.Join(m.pkgs, "\n"), nil
	case "systemctl":
		if args[0] != "show" {
			return strings.Join(m.env, "\n"), nil
		}
		var blocks []string
		for _, a := range args {
			if state, ok := m.timers[a]; ok {
				blocks = append(blocks, "Id="+a+"\nUnitFileState="+state)
			}
		}
		return strings.Join(blocks, "\n\n"), nil
	}
	m.t.Fatalf("unexpected command %s", name)
	return "", nil
}

func wantRefused(t *testing.T, err error, names ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("passed, want a refusal naming %q", names)
	}
	for _, n := range names {
		if !strings.Contains(err.Error(), n) {
			t.Fatalf("refusal does not name %q:\n%v", n, err)
		}
	}
}

func wantPassed(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("refused, want a pass:\n%v", err)
	}
}

// ── Software comes only from Debian ──────────────────────

// Images no node check installs before v0.7.0.
func TestAptSourcesDebianImagesPass(t *testing.T) {
	t.Run("cloud image, backports listed", func(t *testing.T) {
		m := newFakeApt(t)
		m.debianImage()
		wantPassed(t, m.baseline().Sources)
	})
	t.Run("installer, no Signed-By, deb-src", func(t *testing.T) {
		m := newFakeApt(t)
		m.installerImage()
		wantPassed(t, m.baseline().Sources)
	})
	t.Run("cdrom, file and a provider mirror", func(t *testing.T) {
		m := newFakeApt(t)
		m.file("/etc/apt/sources.list", "deb cdrom:[Debian 13]/ trixie main\ndeb [signed-by=/usr/share/keyrings/debian-archive-keyring.pgp] file:/srv/mirror trixie main contrib\ndeb http://mirror.hetzner.com/debian/packages trixie main contrib non-free\n")
		m.targets = []fakeTarget{
			{entry: "/etc/apt/sources.list:1", uri: "cdrom:[Debian 13]/", suite: "trixie", component: "main", of: "deb"},
			{entry: "/etc/apt/sources.list:2", uri: "file:/srv/mirror/", suite: "trixie", component: "contrib", of: "deb"},
			{entry: "/etc/apt/sources.list:3", uri: "http://mirror.hetzner.com/debian/packages/", suite: "trixie", component: "non-free", of: "deb"},
		}
		wantPassed(t, m.baseline().Sources)
	})
	t.Run("a stray /etc/apt/trusted.gpg apt 3.0 does not read", func(t *testing.T) {
		m := newFakeApt(t)
		m.installerImage()
		m.file("/etc/apt/trusted.gpg", "old keys")
		wantPassed(t, m.baseline().Sources)
	})
	t.Run("a Debian key in the trusted folder as Signed-By", func(t *testing.T) {
		m := newFakeApt(t)
		m.oneSource("deb [signed-by=/etc/apt/trusted.gpg.d/debian-archive-trixie-stable.asc] http://deb.debian.org/debian trixie main")
		wantPassed(t, m.baseline().Sources)
	})
	t.Run("Debian's folder written with a slash", func(t *testing.T) {
		m := newFakeApt(t)
		m.debianImage()
		m.setting("/etc/apt/apt.conf.d/10slash", `Dir::Etc::sourceparts "/etc/apt/sources.list.d/";`)
		wantPassed(t, m.baseline().Sources)
	})
}

func TestAptSourcesRefusesOtherSources(t *testing.T) {
	for _, suite := range []string{"bookworm", "sid", "stable", "trixie-proposed-updates", "trixie-backports-sloppy"} {
		t.Run(suite, func(t *testing.T) {
			m := newFakeApt(t)
			m.debianImage()
			m.file("/etc/apt/sources.list.d/other.list", "deb [signed-by=/usr/share/keyrings/debian-archive-keyring.gpg] http://deb.debian.org/debian "+suite+" main\n")
			m.targets = append(m.targets, fakeTarget{entry: "/etc/apt/sources.list.d/other.list:1", uri: "http://deb.debian.org/debian/", suite: suite, component: "main", of: "deb"})
			wantRefused(t, m.baseline().Sources, "/etc/apt/sources.list.d/other.list", suite)
		})
	}
	t.Run("a flat repository", func(t *testing.T) {
		m := newFakeApt(t)
		m.debianImage()
		m.file("/etc/apt/sources.list.d/flat.list", "deb [signed-by=/usr/share/keyrings/debian-archive-keyring.gpg] http://deb.debian.org/debian ./\n")
		m.targets = append(m.targets, fakeTarget{entry: "/etc/apt/sources.list.d/flat.list:1", uri: "http://deb.debian.org/debian/", suite: "./", of: "deb"})
		wantRefused(t, m.baseline().Sources, "/etc/apt/sources.list.d/flat.list")
	})
	t.Run("a component outside Debian's", func(t *testing.T) {
		m := newFakeApt(t)
		m.debianImage()
		m.file("/etc/apt/sources.list.d/extras.list", "deb [signed-by=/usr/share/keyrings/debian-archive-keyring.gpg] http://deb.debian.org/debian trixie extras\n")
		m.targets = append(m.targets, fakeTarget{entry: "/etc/apt/sources.list.d/extras.list:1", uri: "http://deb.debian.org/debian/", suite: "trixie", component: "extras", of: "deb"})
		wantRefused(t, m.baseline().Sources, "/etc/apt/sources.list.d/extras.list")
	})
	t.Run("package files added by a setting", func(t *testing.T) {
		m := newFakeApt(t)
		m.debianImage()
		m.setting("/etc/apt/apt.conf.d/50with", `APT::Sources::With:: "/srv/extra/Packages";`)
		wantRefused(t, m.baseline().Sources, "/etc/apt/apt.conf.d/50with")
	})
}

func TestAptSourcesRefusesKeysThatAreNotDebians(t *testing.T) {
	signed := func(t *testing.T, signedBy string) *fakeAptMachine {
		m := newFakeApt(t)
		m.file("/etc/apt/sources.list.d/debian.sources", "Types: deb\nURIs: http://deb.debian.org/debian\nSuites: trixie\nComponents: main\nSigned-By: "+signedBy+"\n")
		m.targets = []fakeTarget{{entry: "/etc/apt/sources.list.d/debian.sources:1", uri: "http://deb.debian.org/debian/", suite: "trixie", component: "main", of: "deb"}}
		return m
	}
	t.Run("a key file no Debian package installed", func(t *testing.T) {
		m := signed(t, "/etc/apt/keyrings/mine.gpg")
		m.file("/etc/apt/keyrings/mine.gpg", "my key")
		wantRefused(t, m.baseline().Sources, "/etc/apt/sources.list.d/debian.sources", "/etc/apt/keyrings/mine.gpg")
	})
	t.Run("Debian's keyring path, file changed", func(t *testing.T) {
		m := signed(t, "/usr/share/keyrings/debian-archive-keyring.gpg")
		m.file("/usr/share/keyrings/debian-archive-keyring.pgp", "someone else's keys")
		wantRefused(t, m.baseline().Sources, "debian-archive-keyring")
	})
	t.Run("Debian's removed keys", func(t *testing.T) {
		m := signed(t, "/usr/share/keyrings/debian-archive-removed-keys.gpg")
		wantRefused(t, m.baseline().Sources, "debian-archive-removed-keys")
	})
	t.Run("an embedded key", func(t *testing.T) {
		m := signed(t, "\n -----BEGIN PGP PUBLIC KEY BLOCK-----\n .\n mQINBGcAAAA=\n -----END PGP PUBLIC KEY BLOCK-----")
		wantRefused(t, m.baseline().Sources, "/etc/apt/sources.list.d/debian.sources")
	})
	t.Run("fingerprints", func(t *testing.T) {
		m := signed(t, "4CB50190207B4758A3F73A796ED0E7B82643E131")
		wantRefused(t, m.baseline().Sources, "/etc/apt/sources.list.d/debian.sources", "4CB50190207B4758A3F73A796ED0E7B82643E131")
	})
	t.Run("one-line signed-by with a second key", func(t *testing.T) {
		m := newFakeApt(t)
		m.oneSource("deb [arch=amd64 signed-by=/usr/share/keyrings/debian-archive-keyring.gpg,/etc/apt/keyrings/extra.asc] http://deb.debian.org/debian trixie main")
		m.file("/etc/apt/keyrings/extra.asc", "extra")
		wantRefused(t, m.baseline().Sources, "/etc/apt/keyrings/extra.asc")
	})
	t.Run("a tab after the type", func(t *testing.T) {
		m := newFakeApt(t)
		m.oneSource("deb\t[signed-by=/etc/apt/keyrings/evil.gpg] http://deb.debian.org/debian trixie main")
		m.file("/etc/apt/keyrings/evil.gpg", "evil")
		wantRefused(t, m.baseline().Sources, "/etc/apt/keyrings/evil.gpg")
	})
	t.Run("a # inside the options", func(t *testing.T) {
		m := newFakeApt(t)
		m.oneSource("deb [lang=#x signed-by=/etc/apt/keyrings/evil.gpg] http://deb.debian.org/debian trixie main")
		m.file("/etc/apt/keyrings/evil.gpg", "evil")
		wantRefused(t, m.baseline().Sources, "/etc/apt/keyrings/evil.gpg")
	})
}

func TestAptSourcesRefusesTrustedFolderKeys(t *testing.T) {
	extra := func(m *fakeAptMachine) { m.file("/etc/apt/trusted.gpg.d/google.gpg", "google") }
	t.Run("an extra key", func(t *testing.T) {
		m := newFakeApt(t)
		m.installerImage()
		extra(m)
		wantRefused(t, m.baseline().Sources, "/etc/apt/trusted.gpg.d/google.gpg")
	})
	t.Run("a Debian key file changed", func(t *testing.T) {
		m := newFakeApt(t)
		m.installerImage()
		m.file("/etc/apt/trusted.gpg.d/debian-archive-trixie-stable.asc", "changed")
		wantRefused(t, m.baseline().Sources, "/etc/apt/trusted.gpg.d/debian-archive-trixie-stable.asc")
	})
	t.Run("an extra key, every source signed", func(t *testing.T) {
		// The folder counts only for a source without its own key.
		m := newFakeApt(t)
		m.debianImage()
		extra(m)
		wantPassed(t, m.baseline().Sources)
	})
	// apt ignores these, so the source trusts the folder.
	t.Run("Signed-By spelled in capitals on a one-line source", func(t *testing.T) {
		m := newFakeApt(t)
		m.oneSource("deb [Signed-By=/usr/share/keyrings/debian-archive-keyring.gpg] http://deb.debian.org/debian trixie main")
		extra(m)
		wantRefused(t, m.baseline().Sources, "/etc/apt/trusted.gpg.d/google.gpg")
	})
	t.Run("an empty Signed-By", func(t *testing.T) {
		m := newFakeApt(t)
		m.file("/etc/apt/sources.list.d/debian.sources", "Types: deb\nURIs: http://deb.debian.org/debian\nSuites: trixie\nComponents: main\nSigned-By:\n")
		m.targets = []fakeTarget{{entry: "/etc/apt/sources.list.d/debian.sources:1", uri: "http://deb.debian.org/debian/", suite: "trixie", component: "main", of: "deb"}}
		extra(m)
		wantRefused(t, m.baseline().Sources, "/etc/apt/trusted.gpg.d/google.gpg")
	})
	t.Run("files apt skips there", func(t *testing.T) {
		m := newFakeApt(t)
		m.installerImage()
		m.file("/etc/apt/trusted.gpg.d/google.gpg~", "old")
		m.file("/etc/apt/trusted.gpg.d/google.gpg.disabled", "old")
		m.file("/etc/apt/trusted.gpg.d/notes.txt", "text")
		m.file("/etc/apt/trusted.gpg.d/.hidden.gpg", "hidden")
		wantPassed(t, m.baseline().Sources)
	})
}

func TestAptSourcesRefusesTurnedOffSignatureChecks(t *testing.T) {
	t.Run("trusted on a one-line source", func(t *testing.T) {
		for _, v := range []string{"yes", "1", "0x1"} {
			m := newFakeApt(t)
			m.oneSource("deb [trusted=" + v + "] http://deb.debian.org/debian trixie main")
			wantRefused(t, m.baseline().Sources, aptFileSources, "trusted="+v)
		}
	})
	t.Run("trusted=no is not weaker", func(t *testing.T) {
		m := newFakeApt(t)
		m.oneSource("deb [trusted=no signed-by=/usr/share/keyrings/debian-archive-keyring.gpg] http://deb.debian.org/debian trixie main")
		wantPassed(t, m.baseline().Sources)
	})
	t.Run("an option apt lists for one source", func(t *testing.T) {
		m := newFakeApt(t)
		m.debianImage()
		m.file("/etc/apt/sources.list.d/weak.list", "deb [allow-weak=yes signed-by=/usr/share/keyrings/debian-archive-keyring.gpg] http://deb.debian.org/debian trixie main\n")
		m.targets = append(m.targets, fakeTarget{entry: "/etc/apt/sources.list.d/weak.list:1", uri: "http://deb.debian.org/debian/", suite: "trixie", component: "main", of: "deb", allow: "Allow-Weak"})
		wantRefused(t, m.baseline().Sources, "/etc/apt/sources.list.d/weak.list", "Allow-Weak")
	})
	t.Run("apt's global insecure setting", func(t *testing.T) {
		m := newFakeApt(t)
		m.debianImage()
		m.setting("/etc/apt/apt.conf.d/80insecure", `Acquire::AllowInsecureRepositories "true";`)
		for i := range m.targets {
			m.targets[i].allow = "Allow-Insecure"
		}
		err := m.baseline().Sources
		wantRefused(t, err, "/etc/apt/apt.conf.d/80insecure", "AllowInsecureRepositories")
		if strings.Contains(err.Error(), "debian.sources") {
			t.Fatalf("blamed a source for apt's own setting:\n%v", err)
		}
	})
	t.Run("another signature checker", func(t *testing.T) {
		m := newFakeApt(t)
		m.debianImage()
		m.setting("/etc/apt/apt.conf.d/90gpgv", `APT::Key::GPGVCommand "/usr/local/bin/always-good";`)
		wantRefused(t, m.baseline().Sources, "/etc/apt/apt.conf.d/90gpgv")
	})
}

func TestAptSourcesRefusesMovedFolders(t *testing.T) {
	for key, line := range map[string]string{
		"Dir::Etc::sourceparts": `Dir::Etc::sourceparts "/srv/sources.d";`,
		"Dir::Etc::Trusted":     `Dir::Etc::Trusted "/etc/apt/trusted.gpg";`,
	} {
		t.Run(key, func(t *testing.T) {
			m := newFakeApt(t)
			m.debianImage()
			m.setting("/etc/apt/apt.conf.d/10moved", line)
			_, value, _ := strings.Cut(line, `"`)
			m.folders[key] = strings.TrimSuffix(value, `";`)
			wantRefused(t, m.baseline().Sources, "/etc/apt/apt.conf.d/10moved")
		})
	}
}

func TestAptSourcesRefusesIncludes(t *testing.T) {
	m := newFakeApt(t)
	m.debianImage()
	m.file("/etc/apt/apt.conf.d/60keep", "#include \"/etc/apt/keep.conf\";\n")
	m.file("/etc/apt/keep.conf", "Unattended-Upgrade::Package-Blacklist:: \"tor\";\n")
	wantRefused(t, m.baseline().Sources, "/etc/apt/apt.conf.d/60keep")
}

func TestAptSourcesSkipsWhatAptSkips(t *testing.T) {
	m := newFakeApt(t)
	m.debianImage()
	bad := "deb [trusted=yes] http://example.org/x stable main\n"
	for _, name := range []string{"x.list.disabled", "x.list.bak", "x.list.save", "x.list.dpkg-old", "x.list~", "x.txt", ".x.list", "x.list.distUpgrade"} {
		m.file("/etc/apt/sources.list.d/"+name, bad)
	}
	m.file("/etc/apt/sources.list.d/off.sources", "# a stanza turned off\nEnabled: no\nTypes: deb\nURIs: http://example.org/x\nSuites: stable\nComponents: main\nTrusted: yes\nSigned-By: /etc/apt/keyrings/x.gpg\n")
	m.file("/etc/apt/sources.list.d/commented.list", "# deb [trusted=yes] http://example.org/x stable main\n")
	if err := os.MkdirAll(m.path("/etc/apt/sources.list.d/folder.list"), 0o755); err != nil {
		t.Fatal(err)
	}
	wantPassed(t, m.baseline().Sources)
}

func TestAptChecksReportUnreadable(t *testing.T) {
	m := newFakeApt(t)
	m.debianImage()
	if err := os.RemoveAll(m.path(aptFolderSources)); err != nil {
		t.Fatal(err)
	}
	m.file(aptFolderSources, "a file where apt wants a folder")
	wantRefused(t, m.baseline().Sources, aptFolderSources)
}

// ── Every package gets Debian's security updates ─────────

func TestAptUpdatesPassOnDebiansImages(t *testing.T) {
	m := newFakeApt(t)
	m.debianImage()
	// Debian's own unattended-upgrades files, which step 11 replaces, may
	// stop the nightly job before it does.
	m.file(paths.AutoUpgrades, "APT::Periodic::Update-Package-Lists \"0\";\nAPT::Periodic::Unattended-Upgrade \"0\";\n")
	m.file(paths.UnattendedUpgrades, "Unattended-Upgrade::Package-Blacklist {\n    \"linux-\";\n};\n")
	m.file("/etc/apt/apt.conf.d/50other", "// Unattended-Upgrade::Package-Blacklist { \"linux-\"; };\n/*\nAPT::Periodic::Enable \"0\";\n*/\n")
	m.file("/etc/apt/preferences.d/notes.txt", "not read by apt")
	m.file("/etc/apt/preferences.d/x.pref.disabled", "not read by apt")
	wantPassed(t, m.baseline().Updates)
}

func TestAptUpdatesRefusesPins(t *testing.T) {
	for _, name := range []string{"/etc/apt/preferences", "/etc/apt/preferences.d/linux", "/etc/apt/preferences.d/backports.pref"} {
		t.Run(name, func(t *testing.T) {
			m := newFakeApt(t)
			m.debianImage()
			m.file(name, "Package: linux-*\nPin: release n=trixie-backports\nPin-Priority: 500\n")
			wantRefused(t, m.baseline().Updates, name)
		})
	}
}

func TestAptUpdatesRefusesNightlySettings(t *testing.T) {
	for name, content := range map[string]string{
		"/etc/apt/apt.conf.d/99no-updates": "APT::Periodic::Unattended-Upgrade \"0\";\n",
		"/etc/apt/apt.conf.d/10periodic":   "APT { Periodic { Enable \"0\"; }; };\n",
		"/etc/apt/apt.conf.d/70note":       "/* a note */ APT::Periodic::Enable \"0\";\n",
		"/etc/apt/apt.conf":                "Unattended-Upgrade::Package-Blacklist { \"openssl\"; };\n",
		"/etc/apt/apt.conf.d/52clear":      "#clear Unattended-Upgrade::Origins-Pattern;\n",
		"/etc/apt/apt.conf.d/52only":       "Unattended-Upgrade::Package-Whitelist-Strict \"true\";\n",
	} {
		t.Run(name, func(t *testing.T) {
			m := newFakeApt(t)
			m.debianImage()
			m.file(name, content)
			wantRefused(t, m.baseline().Updates, name)
		})
	}
}

// Masking the job itself stops the nightly updates as surely as switching
// off its timer. The node check shows a masked timer.
func TestAptUpdatesRefusesAMaskedNightlyJob(t *testing.T) {
	m := newFakeApt(t)
	m.debianImage()
	m.timers["apt-daily-upgrade.service"] = "masked"
	wantRefused(t, m.baseline().Updates, "apt-daily-upgrade.service")
}

// A timer switched off with systemctl disable does not start after the next
// reboot. The node check shows a masked one.
func TestAptUpdatesRefusesADisabledTimer(t *testing.T) {
	m := newFakeApt(t)
	m.debianImage()
	m.timers["apt-daily.timer"] = "disabled"
	wantRefused(t, m.baseline().Updates, "apt-daily.timer")
}

func TestAptUpdatesRefusesBackportsPackages(t *testing.T) {
	m := newFakeApt(t)
	m.debianImage()
	m.pkgs = append(m.pkgs, "installed linux-image-6.16.3+deb13-amd64 6.16.3-1~bpo13+1", "config-files cockpit 337-1~bpo13+1")
	err := m.baseline().Updates
	wantRefused(t, err, "linux-image-6.16.3+deb13-amd64")
	if strings.Contains(err.Error(), "cockpit") {
		t.Fatalf("named a package that is not installed:\n%v", err)
	}
}

// ── apt uses no proxy ────────────────────────────────────

func TestAptProxyRefusesProxySettings(t *testing.T) {
	for name, line := range map[string]string{
		"/etc/apt/apt.conf.d/zz-direct":      `Acquire { http { Proxy::deb.debian.org "DIRECT"; }; };`,
		"/etc/apt/apt.conf.d/30autoproxy":    `Acquire::http::Proxy-Auto-Detect "/usr/bin/auto-apt-proxy";`,
		"/etc/apt/apt.conf.d/auto-apt-proxy": `Acquire::https::ProxyAutoDetect "/usr/bin/auto-apt-proxy";`,
		"/etc/apt/apt.conf.d/50tor":          `Acquire::tor::Proxy "socks5h://10.0.0.1:9050";`,
		"/etc/apt/apt.conf.d/50method":       `Binary::tor+https::Acquire::https::Proxy::deb.debian.org "DIRECT";`,
		"/etc/apt/apt.conf.d/50program":      `Dir::Bin::Methods::tor+https "/usr/lib/apt/methods/https";`,
	} {
		t.Run(name, func(t *testing.T) {
			m := newFakeApt(t)
			m.debianImage()
			m.file(name, line+"\n")
			wantRefused(t, m.baseline().Proxy, name)
		})
	}
	t.Run("set where vpn cannot name the file", func(t *testing.T) {
		m := newFakeApt(t)
		m.debianImage()
		m.dump = []string{`Acquire::https::Proxy::deb.debian.org "DIRECT";`}
		wantRefused(t, m.baseline().Proxy, "Acquire::https::Proxy::deb.debian.org")
	})
	t.Run("no_proxy for every service", func(t *testing.T) {
		m := newFakeApt(t)
		m.debianImage()
		m.env = append(m.env, "no_proxy=deb.debian.org")
		wantRefused(t, m.baseline().Proxy, "no_proxy")
	})
}

// ── Step 10 ──────────────────────────────────────────────

// stepTenMachine is an installer image in the middle of an install, with
// apt-transport-tor installed.
func stepTenMachine(t *testing.T) *fakeAptMachine {
	m := newFakeApt(t)
	m.installerImage()
	m.file(aptTorMethod, "method")
	return m
}

// afterStepTen makes the stand-in apt list vpn's file, as Debian's apt
// does once it is written.
func (m *fakeAptMachine) afterStepTen(sources string) {
	m.targets = nil
	_, rest, _ := strings.Cut(sources, "Components:")
	line, _, _ := strings.Cut(rest, "\n")
	for _, c := range strings.Fields(line) {
		m.targets = append(m.targets,
			fakeTarget{entry: paths.AptSources + ":1", uri: "tor+https://deb.debian.org/debian/", suite: "trixie", component: c, of: "deb"},
			fakeTarget{entry: paths.AptSources + ":1", uri: "tor+https://deb.debian.org/debian/", suite: "trixie-updates", component: c, of: "deb"},
			fakeTarget{entry: paths.AptSources + ":2", uri: "tor+https://deb.debian.org/debian-security/", suite: "trixie-security", component: c, of: "deb"})
	}
	m.dump = []string{`Dir::Bin::methods::http "false";`, `Dir::Bin::methods::https "false";`, `Acquire::tor::Proxy "socks5h://apt-transport-tor@127.0.0.1:9050";`}
}

// writer writes as WriteFileRoot does, failing the failAt'th write like a
// kill. after runs once a write succeeds.
func (m *fakeAptMachine) writer(failAt int, after func(path string)) func(string, []byte, os.FileMode) error {
	n := 0
	return func(path string, content []byte, perm os.FileMode) error {
		n++
		if n == failAt {
			return errors.New("killed")
		}
		if err := os.MkdirAll(filepath.Dir(m.path(path)), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(m.path(path), content, perm); err != nil {
			return err
		}
		if path == paths.AptSources {
			m.afterStepTen(string(content))
		}
		if after != nil {
			after(path)
		}
		return nil
	}
}

func (m *fakeAptMachine) stepTen(write func(string, []byte, os.FileMode) error) error {
	return aptHost{root: m.root, run: m.run}.configureAptTor(write)
}

func TestStepTenRefusesWithoutTorMethod(t *testing.T) {
	m := newFakeApt(t)
	m.installerImage()
	err := m.stepTen(func(path string, _ []byte, _ os.FileMode) error {
		t.Fatalf("the step wrote %s before it failed", path)
		return nil
	})
	wantRefused(t, err, aptTorMethod)
}

func TestStepTenFinishesAfterAKill(t *testing.T) {
	for failAt := 1; failAt <= 2; failAt++ {
		t.Run(fmt.Sprintf("killed at write %d", failAt), func(t *testing.T) {
			m := stepTenMachine(t)
			if err := m.stepTen(m.writer(failAt, nil)); err == nil {
				t.Fatal("the kill did not stop the step")
			}
			if failAt == 2 {
				if _, err := os.Stat(m.path(paths.AptTor)); err != nil {
					t.Fatal("apt's plain methods are not off before vpn's sources are written")
				}
			}
			wantPassed(t, m.stepTen(m.writer(0, nil)))
			for _, p := range []string{paths.AptTor, paths.AptSources, "/etc/apt/sources.list.disabled"} {
				if _, err := os.Stat(m.path(p)); err != nil {
					t.Fatalf("missing after the step: %v", err)
				}
			}
			if _, err := os.Stat(m.path(aptFileSources)); err == nil {
				t.Fatal("the image's sources.list is still read by apt")
			}
		})
	}
	t.Run("no sources.list at all", func(t *testing.T) {
		m := newFakeApt(t)
		m.debianImage()
		m.file(aptTorMethod, "method")
		wantPassed(t, m.stepTen(m.writer(0, nil)))
		if _, err := os.Stat(m.path("/etc/apt/sources.list.d/debian.sources.disabled")); err != nil {
			t.Fatalf("the image's file was not switched off: %v", err)
		}
	})
}

func TestStepTenRefusesARouteOutsideTor(t *testing.T) {
	for name, line := range map[string]string{
		"a plain method still on":           `Dir::Bin::Methods::https "/usr/lib/apt/methods/https";`,
		"another proxy":                     `Acquire::tor::Proxy::deb.debian.org "DIRECT";`,
		"the Tor method's own setting":      `Binary::tor+https::Acquire::https::Proxy::deb.debian.org "DIRECT";`,
		"the plain program for Tor sources": `Dir::Bin::Methods::tor+https "/usr/lib/apt/methods/https";`,
	} {
		t.Run(name, func(t *testing.T) {
			m := stepTenMachine(t)
			err := m.stepTen(m.writer(0, func(path string) {
				if path == paths.AptSources {
					m.setting("/etc/apt/apt.conf.d/zz-late", line)
				}
			}))
			wantRefused(t, err, "deb.debian.org")
		})
	}
	t.Run("an image source still listed", func(t *testing.T) {
		m := stepTenMachine(t)
		err := m.stepTen(m.writer(0, func(path string) {
			if path == paths.AptSources {
				m.targets = append(m.targets, fakeTarget{entry: "/etc/apt/sources.list.d/late.list:1", uri: "http://deb.debian.org/debian/", suite: "trixie", component: "main", of: "deb"})
			}
		}))
		wantRefused(t, err, "deb.debian.org")
	})
}
