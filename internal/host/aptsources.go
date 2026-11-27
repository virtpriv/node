package host

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/system"
	"github.com/virtpriv/node/internal/update/files"
)

// The suites and components of Debian 13. They change with the release that
// supports Debian 14.
var (
	debianSuites     = []string{"trixie", "trixie-updates", "trixie-security", "trixie-backports"}
	debianComponents = []string{"main", "contrib", "non-free", "non-free-firmware"}
)

const (
	debianKeyring       = "/usr/share/keyrings/debian-archive-keyring.pgp"
	debianKeyringMD5    = "/var/lib/dpkg/info/debian-archive-keyring.md5sums"
	debianKeyringName   = "debian-archive-keyring"
	debianRemovedKeys   = "debian-archive-removed-keys"
	dpkgStatus          = "/var/lib/dpkg/status"
	aptTorMethod        = "/usr/lib/apt/methods/tor+https"
	aptTorProxy         = "socks5h://apt-transport-tor@127.0.0.1:9050"
	aptDebianMain       = "tor+https://deb.debian.org/debian"
	aptDebianSecurity   = "tor+https://deb.debian.org/debian-security"
	aptFixDebian        = "or use a machine whose software comes only from Debian"
	aptFixUpdates       = "or use a fresh Debian 13 machine"
	aptFixProxy         = "so apt reaches Debian only through Tor"
	aptFolderSources    = "/etc/apt/sources.list.d"
	aptFolderSettings   = "/etc/apt/apt.conf.d"
	aptFolderPins       = "/etc/apt/preferences.d"
	aptFolderTrusted    = "/etc/apt/trusted.gpg.d"
	aptFileSources      = "/etc/apt/sources.list"
	aptNoSourcesFile    = "/dev/null"
	aptFileSettings     = "/etc/apt/apt.conf"
	aptFilePins         = "/etc/apt/preferences"
	aptWeakSourceOption = "turns off apt's signature check"
)

// aptTorSettings is vpn's apt settings file. Plain http and https are off, so
// any source outside Tor fails. The Tor proxy is apt's own default, written
// down so it can be seen. The last two lines point apt at vpn's own folder of
// sources and at no single sources file, so apt reads vpn's sources alone and
// the machine's own source files stay as they are.
const aptTorSettings = `// Virtual Private Node sends apt through Tor only. Plain http and https are off.
Dir::Bin::Methods::http "false";
Dir::Bin::Methods::https "false";
Acquire::tor::Proxy "` + aptTorProxy + `";
Acquire::http::Timeout "60";
Acquire::https::Timeout "60";
Acquire::Retries "3";
// apt reads its sources in vpn's own folder. The machine's source files stay as they were.
Dir::Etc::sourcelist "` + aptNoSourcesFile + `";
Dir::Etc::sourceparts "` + paths.AptSourcesDir + `";
`

// vpnDebianSources is vpn's sources file. Backports are left out, since
// Debian's security team does not cover them.
func vpnDebianSources(components []string) string {
	c := strings.Join(components, " ")
	return `# Virtual Private Node's sources, through Tor only. apt reads this folder alone.
# The machine's own source files stay as they were and are not read.
Types: deb
URIs: ` + aptDebianMain + `
Suites: trixie trixie-updates
Components: ` + c + `
Signed-By: ` + debianKeyring + `

Types: deb
URIs: ` + aptDebianSecurity + `
Suites: trixie-security
Components: ` + c + `
Signed-By: ` + debianKeyring + `
`
}

// vpnAptSettingFiles are the apt settings files vpn writes, in steps 9 and
// 10. The checks of the machine's own settings skip them. A fresh machine
// that holds one is refused before these checks run, so here they are vpn's.
var vpnAptSettingFiles = []string{paths.AptTor, paths.AptNightly, paths.AptRebootCheck}

// debianNightlyFiles are the two settings files of Debian's
// unattended-upgrades package. They are the machine's own and are read like
// its other settings files, but for what they set for the nightly job. vpn's
// own file is read after them and clears those settings before it sets its
// own.
var debianNightlyFiles = []string{paths.AutoUpgrades, paths.UnattendedUpgrades}

// nightlySetting reports whether a setting's name, in lower case, is one of
// the nightly job's or names all of them, as "#clear APT::Periodic" does.
func nightlySetting(name string) bool {
	return name == "apt::periodic" || name == "unattended-upgrade" ||
		strings.HasPrefix(name, "apt::periodic::") || strings.HasPrefix(name, "unattended-upgrade::")
}

// AptBaseline is the preflight's three apt answers. A nil error passes.
type AptBaseline struct {
	Sources, Updates, Proxy error
}

// CheckAptBaseline runs the preflight's three apt checks. It writes nothing.
func CheckAptBaseline() AptBaseline {
	return productionAptHost().baseline()
}

// ConfigureAptTor is install step 9. It writes vpn's sources file over Tor
// into vpn's own folder, then vpn's settings file, which switches off apt's
// plain methods and points apt at that folder, and checks the result with
// apt. The machine's own source files are not changed. Each part can run
// again after a kill.
func ConfigureAptTor() error {
	return productionAptHost().configureAptTor(system.WriteFileRoot)
}

// aptHost is what the checks read. That is the files under root, "" on a
// node, and the answers of Debian's tools.
type aptHost struct {
	root string
	run  func(name string, args ...string) (string, error)
}

func productionAptHost() aptHost { return aptHost{run: runAptTool} }

// aptCacheInMemory keeps the package caches apt builds in memory, so a
// read-only apt command never saves them to /var/cache/apt.
var aptCacheInMemory = []string{"-o", "Dir::Cache::pkgcache=", "-o", "Dir::Cache::srcpkgcache="}

// AptCacheInMemory is aptCacheInMemory for the installer's preflight.
func AptCacheInMemory() []string { return slices.Clone(aptCacheInMemory) }

func (h aptHost) path(p string) string { return filepath.Join(h.root, p) }

// baseline reads apt's settings once and runs the three checks on them.
func (h aptHost) baseline() AptBaseline {
	settings, err := h.aptSettings()
	var dump []aptSetting
	if err == nil {
		dump, err = h.aptDump()
	}
	if err != nil {
		return AptBaseline{err, err, err}
	}
	return AptBaseline{
		Sources: h.checkSources(settings, dump),
		Updates: h.checkUpdates(settings),
		Proxy:   h.checkProxy(settings, dump),
	}
}

// addFor adds one finding for each file that sets key, or one for apt's
// settings when no file the checks read does.
func (f *findings) addFor(s aptSettingFiles, key, why string) {
	files := s.filesSetting(key)
	if len(files) == 0 {
		files = []string{"apt's settings"}
	}
	for _, file := range files {
		f.add("found %s in %s, which %s", key, file, why)
	}
}

// ── Software comes only from Debian ──────────────────────

func (h aptHost) checkSources(settings aptSettingFiles, dump []aptSetting) error {
	var f findings
	places, moved := h.checkAptFolders(settings, &f)
	if moved {
		// apt reads its sources elsewhere, so the checks below would read
		// the wrong files.
		return f.err("remove", aptFixDebian)
	}
	for _, file := range settings.includes {
		f.add("found #include in %s, which vpn does not follow", file)
	}
	for _, file := range settings.files {
		for _, k := range settings.keys[file] {
			lk := strings.ToLower(k)
			switch {
			case strings.HasPrefix(lk, "apt::key::"):
				f.add("found %s in %s, which %s", k, file, aptWeakSourceOption)
			case strings.HasPrefix(lk, "apt::sources::with"):
				f.add("found %s in %s, which adds packages from outside Debian's sources", strings.TrimSuffix(k, "::"), file)
			}
		}
	}
	global := map[string]bool{}
	for _, s := range dump {
		if name := weakeningSetting(s.key); name != "" && aptTrue(s.value) {
			global[name] = true
			f.addFor(settings, s.key, aptWeakSourceOption)
		}
	}

	entries, err := h.aptSourceEntries(places)
	if err != nil {
		f.add("%v", err)
		return f.err("remove", aptFixDebian)
	}
	keys, err := h.debianKeyFiles()
	if err != nil {
		f.add("%v", err)
		return f.err("remove", aptFixDebian)
	}
	unsigned := false
	for _, e := range entries {
		if e.trusted != "" {
			f.add("found %s in %s, which %s", e.trusted, e.file, aptWeakSourceOption)
		}
		if len(e.signedBy) == 0 {
			unsigned = true
		}
		for _, k := range e.signedBy {
			if !h.isDebianKeyFile(k, keys) {
				f.add("found a key that is not Debian's in %s (%s)", e.file, k)
			}
		}
	}
	if unsigned {
		// A source without its own key trusts every key in this folder.
		h.checkTrustedFolder(keys, &f)
	}

	// apt's own list comes after the files above, so a file apt cannot read
	// is named by them first.
	targets, err := h.aptTargets()
	if err != nil {
		f.add("%v", err)
		return f.err("remove", aptFixDebian)
	}
	for _, t := range targets {
		if !slices.Contains(debianSuites, t.suite) || (t.component != "" && !slices.Contains(debianComponents, t.component)) {
			f.add("found a source that is not Debian's in %s (%s %s)", t.file, strings.TrimSuffix(t.uri, "/"), t.suite)
		}
		for _, name := range t.allow {
			if !global[name] {
				f.add("found %s in %s, which %s", name, t.file, aptWeakSourceOption)
			}
		}
	}
	return f.err("remove", aptFixDebian)
}

// aptDefaultFolders are the places Debian's apt reads its sources, keys and
// settings, as apt-config shell resolves them.
var aptDefaultFolders = map[string]string{
	aptKeySourcesFile:            aptFileSources,
	aptKeySourcesFolder:          aptFolderSources + "/",
	"Dir::Etc::trustedparts":     "/etc/apt/trusted.gpg.d/",
	"Dir::Etc::Trusted":          "",
	"Dir::Etc::main":             "/etc/apt/apt.conf",
	"Dir::Etc::parts":            "/etc/apt/apt.conf.d/",
	"Dir::Etc::preferences":      "/etc/apt/preferences",
	"Dir::Etc::preferencesparts": "/etc/apt/preferences.d/",
}

const (
	aptKeySourcesFile   = "Dir::Etc::sourcelist"
	aptKeySourcesFolder = "Dir::Etc::sourceparts"
)

// vpnSourcePlaces are apt's two answers once step 9 has written vpn's
// settings file, no single sources file and vpn's own folder.
var vpnSourcePlaces = map[string]string{
	aptKeySourcesFile:   aptNoSourcesFile,
	aptKeySourcesFolder: paths.AptSourcesDir + "/",
}

// aptSourcePlaces are the single file and the folder apt reads its sources
// in. The checks read these places.
type aptSourcePlaces struct{ file, folder string }

// checkAptFolders refuses settings that move where apt reads its sources,
// keys or settings, and reports whether any did. It returns the two places
// apt reads its sources in.
//
// Each of those two may be Debian's own or vpn's own, and only vpn's
// settings file may name it. A file of the machine's that names one is
// refused whatever it names, since one read after vpn's file would win.
func (h aptHost) checkAptFolders(settings aptSettingFiles, f *findings) (aptSourcePlaces, bool) {
	keys := make([]string, 0, len(aptDefaultFolders))
	for k := range aptDefaultFolders {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	args := []string{"shell"}
	for i, k := range keys {
		kind := "/f"
		if strings.HasSuffix(aptDefaultFolders[k], "/") {
			kind = "/d"
		}
		args = append(args, fmt.Sprintf("F%d", i), k+kind)
	}
	out, err := h.run("apt-config", args...)
	if err != nil {
		f.add("cannot read apt's settings: %v", err)
		return aptSourcePlaces{}, true
	}
	names := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if name, value, ok := strings.Cut(line, "="); ok {
			names[name] = strings.Trim(value, "'")
		}
	}
	got := map[string]string{}
	moved := false
	for i, k := range keys {
		got[k] = names[fmt.Sprintf("F%d", i)]
		vpns, isSourcePlace := vpnSourcePlaces[k]
		byMachine := isSourcePlace && len(settings.filesSetting(k)) > 0
		if !byMachine && (got[k] == aptDefaultFolders[k] || isSourcePlace && got[k] == vpns) {
			continue
		}
		moved = true
		why := "moves where apt reads its sources or keys"
		named := false
		for _, key := range []string{k, "Dir::Etc", "Dir"} {
			for _, file := range settings.filesSetting(key) {
				f.add("found %s in %s, which %s", key, file, why)
				named = true
			}
		}
		if !named {
			f.add("found %s in apt's settings, which %s", k, why)
		}
	}
	return aptSourcePlaces{
		file:   got[aptKeySourcesFile],
		folder: strings.TrimSuffix(got[aptKeySourcesFolder], "/"),
	}, moved
}

// weakeningSetting returns the name apt's list prints for a global setting
// that turns off signature checks, or "".
func weakeningSetting(key string) string {
	switch strings.ToLower(key) {
	case "acquire::allowinsecurerepositories":
		return "Allow-Insecure"
	case "acquire::allowweakrepositories":
		return "Allow-Weak"
	case "acquire::allowdowngradetoinsecurerepositories":
		return "Allow-Downgrade-To-Insecure"
	}
	return ""
}

// checkTrustedFolder requires every key file apt reads in trusted.gpg.d
// ("gpg" and "asc", apt 3.0.3 gpgv.cc line 402) to be one of Debian's
// keyring package's files there, unchanged by dpkg's record.
func (h aptHost) checkTrustedFolder(keys map[string]string, f *findings) {
	files, err := h.aptFolderFiles(aptFolderTrusted, "gpg", "asc")
	if err != nil {
		f.add("%v", err)
		return
	}
	for _, file := range files {
		sum, err := h.md5Of(file)
		if err != nil {
			f.add("%v", err)
			continue
		}
		if keys[file] != sum {
			f.add("found %s, which is not one of Debian's keys", file)
		}
	}
}

// isDebianKeyFile reports whether a Signed-By value names one of the key
// files Debian's keyring package installed, unchanged. A link, such as the
// package's own .gpg names, counts by the file it leads to.
func (h aptHost) isDebianKeyFile(value string, keys map[string]string) bool {
	if !strings.HasPrefix(value, "/") {
		return false
	}
	resolved, err := filepath.EvalSymlinks(h.path(value))
	if err != nil {
		return false
	}
	node := resolved
	if h.root != "" {
		base, err := filepath.EvalSymlinks(h.root)
		if err != nil {
			return false
		}
		node = strings.TrimPrefix(resolved, base)
	}
	want, ok := keys[node]
	if !ok || strings.Contains(filepath.Base(node), debianRemovedKeys) {
		return false
	}
	sum, err := h.md5Of(node)
	return err == nil && sum == want
}

// debianKeyFiles reads dpkg's checksums of the keyring package's files, those
// in /usr/share/keyrings from its md5sums and its settings files in
// trusted.gpg.d from dpkg's status as installed.
func (h aptHost) debianKeyFiles() (map[string]string, error) {
	keys := map[string]string{}
	data, err := os.ReadFile(h.path(debianKeyringMD5))
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", debianKeyringMD5, unwrapPath(err))
	}
	for _, line := range strings.Split(string(data), "\n") {
		sum, file, ok := strings.Cut(line, "  ")
		if ok && strings.HasPrefix(file, "usr/share/keyrings/") {
			keys["/"+file] = sum
		}
	}
	data, err = os.ReadFile(h.path(dpkgStatus))
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", dpkgStatus, unwrapPath(err))
	}
	for _, stanza := range strings.Split(string(data), "\n\n") {
		if !strings.HasPrefix(stanza, "Package: "+debianKeyringName+"\n") {
			continue
		}
		_, rest, _ := strings.Cut(stanza, "\nConffiles:\n")
		for _, line := range strings.Split(rest, "\n") {
			fields := strings.Fields(line)
			if !strings.HasPrefix(line, " ") || len(fields) < 2 {
				break
			}
			if len(fields) == 2 { // an obsolete file has a third field
				keys[fields[0]] = fields[1]
			}
		}
	}
	return keys, nil
}

func (h aptHost) md5Of(file string) (string, error) {
	fd, err := os.Open(h.path(file))
	if err != nil {
		return "", fmt.Errorf("cannot read %s: %w", file, unwrapPath(err))
	}
	defer fd.Close()
	sum := md5.New()
	if _, err := io.Copy(sum, fd); err != nil {
		return "", fmt.Errorf("cannot read %s: %w", file, unwrapPath(err))
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// unwrapPath drops the path Go adds to a file error, since the finding names
// the file itself.
func unwrapPath(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// ── apt's list of sources ────────────────────────────────

type aptTarget struct {
	file, uri, suite, component, of string
	allow                           []string // Allow-* options apt prints as true
}

// aptTargets asks apt for every index it would fetch, each with the file
// and entry it comes from and the options that weaken its checks.
// --no-release-info lists sources whose Release file was never fetched. The
// package caches stay in memory, so apt writes nothing.
func (h aptHost) aptTargets() ([]aptTarget, error) {
	out, err := h.run("apt-get", append([]string{"indextargets", "--no-release-info"}, aptCacheInMemory...)...)
	if err != nil {
		return nil, fmt.Errorf("cannot read apt's list of sources: %w", err)
	}
	var targets []aptTarget
	seen := map[string]bool{}
	for _, stanza := range strings.Split(out, "\n\n") {
		fields := deb822Fields(stanza)
		entry := fields["sourcesentry"]
		if i := strings.LastIndex(entry, ":"); i > 0 {
			entry = entry[:i]
		}
		if entry == "" {
			continue
		}
		t := aptTarget{file: entry, uri: fields["repo-uri"], suite: fields["release"],
			component: fields["component"], of: fields["target-of"]}
		for _, name := range []string{"Allow-Insecure", "Allow-Weak", "Allow-Downgrade-To-Insecure"} {
			if aptTrue(fields[strings.ToLower(name)]) {
				t.allow = append(t.allow, name)
			}
		}
		id := fmt.Sprint(t)
		if !seen[id] {
			seen[id] = true
			targets = append(targets, t)
		}
	}
	return targets, nil
}

// deb822Fields reads one stanza's fields, names lowercased, continuation
// lines joined with a newline.
func deb822Fields(stanza string) map[string]string {
	fields := map[string]string{}
	last := ""
	for _, line := range strings.Split(stanza, "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		if (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) && last != "" {
			fields[last] += "\n" + strings.TrimSpace(line)
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		last = strings.ToLower(strings.TrimSpace(name))
		fields[last] = strings.TrimSpace(value)
	}
	return fields
}

// aptSourceEntry is what the narrow scan reads from one source entry, its
// keys and a trusted option. Its address and suite come from apt's own list.
type aptSourceEntry struct {
	file     string
	signedBy []string
	trusted  string // the option as written, when it turns checks off
}

// aptSourceFiles lists the source files apt reads, its single sources file
// and, in its sources folder, files ending in .list or .sources (apt 3.0.3
// sourcelist.cc line 503). Before step 9 the two places are the machine's,
// sources.list and sources.list.d. After it they are vpn's, no file and vpn's
// folder.
func (h aptHost) aptSourceFiles(places aptSourcePlaces) ([]string, error) {
	var files []string
	if ok, err := h.aptRegularFile(places.file); err != nil {
		return nil, err
	} else if ok {
		files = append(files, places.file)
	}
	parts, err := h.aptFolderFiles(places.folder, "list", "sources")
	if err != nil {
		return nil, err
	}
	return append(files, parts...), nil
}

// aptSourceEntries scans every source file apt reads. A stanza with
// "Enabled: no" and a commented line are skipped, as apt skips them.
func (h aptHost) aptSourceEntries(places aptSourcePlaces) ([]aptSourceEntry, error) {
	files, err := h.aptSourceFiles(places)
	if err != nil {
		return nil, err
	}
	var entries []aptSourceEntry
	for _, file := range files {
		data, err := os.ReadFile(h.path(file))
		if err != nil {
			return nil, fmt.Errorf("cannot read %s: %w", file, unwrapPath(err))
		}
		if strings.HasSuffix(file, ".sources") {
			entries = append(entries, deb822SourceEntries(file, string(data))...)
		} else {
			entries = append(entries, oneLineSourceEntries(file, string(data))...)
		}
	}
	return entries, nil
}

// deb822SourceEntries reads the stanzas of a .sources file. An empty
// Signed-By sets no key, so the source trusts apt's key folder, as in apt
// 3.0.3 (debmetaindex.cc line 809).
func deb822SourceEntries(file, data string) []aptSourceEntry {
	var entries []aptSourceEntry
	for _, stanza := range strings.Split(strings.ReplaceAll(data, "\r\n", "\n"), "\n\n") {
		fields := deb822Fields(stanza)
		if len(fields) == 0 || (fields["enabled"] != "" && !aptTrue(fields["enabled"])) {
			continue
		}
		e := aptSourceEntry{file: file, signedBy: signedByValues(fields["signed-by"])}
		if aptTrue(fields["trusted"]) {
			e.trusted = "Trusted " + fields["trusted"]
		}
		entries = append(entries, e)
	}
	return entries
}

// oneLineSourceEntries reads the lines of a .list file as apt 3.0.3 does
// (sourcelist.cc lines 384 to 409). A "#" ends the line unless it is inside
// the options, the type ends at a space or tab, and option names match
// exactly, so apt ignores "Signed-By=".
func oneLineSourceEntries(file, data string) []aptSourceEntry {
	var entries []aptSourceEntry
	for _, line := range strings.Split(data, "\n") {
		for i := 0; i < len(line); i++ {
			if line[i] == '#' && strings.Count(line[:i], "[") <= strings.Count(line[:i], "]") {
				line = line[:i]
				break
			}
		}
		line = strings.TrimSpace(line)
		i := strings.IndexAny(line, " \t\v")
		if i < 0 || (line[:i] != "deb" && line[:i] != "deb-src") {
			continue
		}
		e := aptSourceEntry{file: file}
		rest := strings.TrimLeft(line[i:], " \t\v")
		if strings.HasPrefix(rest, "[") {
			opts, _, _ := strings.Cut(rest[1:], "]")
			for _, opt := range strings.Fields(opts) {
				name, value, _ := strings.Cut(opt, "=")
				switch {
				case name == "signed-by":
					e.signedBy = append(e.signedBy, signedByValues(value)...)
				case name == "trusted" && aptTrue(value):
					e.trusted = opt
				}
			}
		}
		entries = append(entries, e)
	}
	return entries
}

// signedByValues splits a Signed-By value into its key files or
// fingerprints. An embedded key is kept whole, so it never passes as a path.
func signedByValues(v string) []string {
	if strings.Contains(v, "BEGIN PGP") {
		return []string{"an embedded key"}
	}
	return strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' })
}

// aptTrue is apt 3.0.3's StringToBool (strutl.cc line 847) for the values
// that turn an option on, a number that reads as 1 included.
func aptTrue(v string) bool {
	v = strings.TrimSpace(v)
	if n, err := strconv.ParseInt(v, 0, 64); err == nil {
		return n == 1
	}
	switch strings.ToLower(v) {
	case "yes", "true", "with", "on", "enable":
		return true
	}
	return false
}

// ── Files apt reads ──────────────────────────────────────

// aptRegularFile reports whether a single file apt reads exists. apt reads
// it only when it is a regular file (RealFileExists, following a link).
func (h aptHost) aptRegularFile(file string) (bool, error) {
	fi, err := os.Stat(h.path(file))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("cannot read %s: %w", file, unwrapPath(err))
	}
	return fi.Mode().IsRegular(), nil
}

// aptFolderFiles lists the files apt reads in a folder, by apt 3.0.3's
// GetListOfFilesInDir (fileutl.cc line 439), sorted as apt sorts them. A
// hidden file, anything but a regular file, a name apt does not accept and an
// extension outside exts are skipped. "" in exts accepts a name without one.
// A missing folder holds nothing.
func (h aptHost) aptFolderFiles(folder string, exts ...string) ([]string, error) {
	list, err := os.ReadDir(h.path(folder))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", folder, unwrapPath(err))
	}
	var files []string
	for _, e := range list {
		name := e.Name()
		if strings.HasPrefix(name, ".") || !aptAcceptsName(name, exts) {
			continue
		}
		file := folder + "/" + name
		if ok, err := h.aptRegularFile(file); err != nil {
			return nil, err
		} else if ok {
			files = append(files, file)
		}
	}
	slices.Sort(files)
	return files, nil
}

func aptAcceptsName(name string, exts []string) bool {
	ext := ""
	if i := strings.LastIndex(name, "."); i >= 0 {
		ext = name[i+1:]
	}
	if !slices.Contains(exts, ext) {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '_' || r == '-' || r == ':' || r == '.') {
			return false
		}
	}
	return !strings.HasSuffix(name, ".")
}

// ── apt's settings ───────────────────────────────────────

type aptSetting struct {
	key, value string
}

// aptDump reads apt's settings as apt applies them.
func (h aptHost) aptDump() ([]aptSetting, error) {
	out, err := h.run("apt-config", "dump")
	if err != nil {
		return nil, fmt.Errorf("cannot read apt's settings: %w", err)
	}
	var settings []aptSetting
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		value = strings.TrimSuffix(strings.TrimSpace(value), ";")
		settings = append(settings, aptSetting{key: key, value: strings.Trim(value, `"`)})
	}
	return settings, nil
}

// aptSettingFiles holds the setting names each of the machine's own settings
// files sets, so a finding can name its file. vpn's own files are left out,
// and so is what Debian's two nightly files set for the nightly job.
type aptSettingFiles struct {
	files    []string
	keys     map[string][]string
	includes []string // files holding #include, which the checks do not follow
}

// filesSetting lists the files that set key, ignoring case as apt does.
func (s aptSettingFiles) filesSetting(key string) []string {
	var out []string
	for _, file := range s.files {
		for _, k := range s.keys[file] {
			if strings.EqualFold(k, key) {
				out = append(out, file)
				break
			}
		}
	}
	return out
}

// aptSettings reads every settings file apt reads, apt.conf.d's files ending
// in .conf or without an extension (configuration.cc line 1169), then
// apt.conf.
func (h aptHost) aptSettings() (aptSettingFiles, error) {
	s := aptSettingFiles{keys: map[string][]string{}}
	files, err := h.aptFolderFiles(aptFolderSettings, "conf", "")
	if err != nil {
		return s, err
	}
	if ok, err := h.aptRegularFile(aptFileSettings); err != nil {
		return s, err
	} else if ok {
		files = append(files, aptFileSettings)
	}
	for _, file := range files {
		if slices.Contains(vpnAptSettingFiles, file) {
			continue
		}
		keys, include, err := h.aptConfKeys(file)
		if err != nil {
			return s, err
		}
		if slices.Contains(debianNightlyFiles, file) {
			keys = slices.DeleteFunc(keys, func(k string) bool { return nightlySetting(strings.ToLower(k)) })
		}
		s.files = append(s.files, file)
		s.keys[file] = keys
		if include {
			s.includes = append(s.includes, file)
		}
	}
	return s, nil
}

// aptConfKeys lists the setting names one settings file sets, read by the
// rules of apt 3.0.3's ReadConfigFile (configuration.cc line 879). Comments
// are "//", "#" and "/* */" outside quotes, blocks are in braces, and a list
// value is "name::". A "#clear" counts as setting the name it clears. It
// also reports a "#include".
func (h aptHost) aptConfKeys(file string) (keys []string, include bool, err error) {
	data, err := os.ReadFile(h.path(file))
	if err != nil {
		return nil, false, fmt.Errorf("cannot read %s: %w", file, unwrapPath(err))
	}
	var stack []string
	var buf strings.Builder
	inComment := false
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := aptStripComments(strings.TrimSpace(scanner.Text()), &inComment)
		inQuote := false
		start := 0
		for i, r := range line {
			if r == '"' {
				inQuote = !inQuote
			}
			if inQuote || (r != '{' && r != ';' && r != '}') {
				continue
			}
			if frag := strings.TrimSpace(line[start:i]); frag != "" {
				if buf.Len() > 0 {
					buf.WriteByte(' ')
				}
				buf.WriteString(frag)
			}
			start = i + 1
			stmt := buf.String()
			buf.Reset()
			if stmt == "" {
				if r == '}' && len(stack) > 0 {
					stack = stack[:len(stack)-1]
				}
				continue
			}
			tag, word, hasWord := strings.Cut(stmt, " ")
			tag = strings.Trim(tag, `"`)
			switch {
			case r == '{':
				stack = append(stack, tag)
				continue
			case tag == "#include":
				include = true
				continue
			case tag == "#clear":
				keys = append(keys, strings.Trim(strings.TrimSpace(word), `"`))
				continue
			}
			item := tag
			if !hasWord {
				item = "" // a list value
			}
			if len(stack) > 0 {
				item = strings.Join(stack, "::") + "::" + item
			}
			keys = append(keys, item)
			if r == '}' && len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
		if frag := strings.TrimSpace(line[start:]); frag != "" {
			if buf.Len() > 0 {
				buf.WriteByte(' ')
			}
			buf.WriteString(frag)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, false, fmt.Errorf("cannot read %s: %w", file, err)
	}
	return keys, include, nil
}

// aptStripComments removes the comments of one line as apt does. "#clear",
// "#include" and "#x-apt-configure-index" are directives, not comments.
func aptStripComments(line string, inComment *bool) string {
	outsideQuotes := func(s string, i int) bool { return strings.Count(s[:i], `"`)%2 == 0 }
	if *inComment {
		end := strings.Index(line, "*/")
		if end < 0 {
			return ""
		}
		line = line[end+2:]
		*inComment = false
	}
	for i := 0; i+1 < len(line); i++ {
		if line[i] == '/' && line[i+1] == '/' && outsideQuotes(line, i) {
			line = line[:i]
			break
		}
	}
	for i := 0; i < len(line); i++ {
		if line[i] != '#' || !outsideQuotes(line, i) {
			continue
		}
		rest := line[i+1:]
		if strings.HasPrefix(rest, "clear") || strings.HasPrefix(rest, "include") || strings.HasPrefix(rest, "x-apt-configure-index") {
			continue
		}
		line = line[:i]
		break
	}
	var out strings.Builder
	for i := 0; i+1 < len(line); i++ {
		if line[i] != '/' || line[i+1] != '*' || !outsideQuotes(line, i) {
			continue
		}
		end := strings.Index(line[i+2:], "*/")
		if end < 0 {
			*inComment = true
			return out.String() + line[:i]
		}
		out.WriteString(line[:i])
		line = line[i+2+end+2:]
		i = -1
	}
	return out.String() + line
}

// ── Every package gets Debian's security updates ─────────

func (h aptHost) checkUpdates(settings aptSettingFiles) error {
	var f findings
	holds, err := h.run("apt-mark", append([]string{"showhold"}, aptCacheInMemory...)...)
	if err != nil {
		f.add("cannot read apt's held packages: %v", err)
	} else if names := strings.Fields(holds); len(names) > 0 {
		f.add("found held packages (%s)", strings.Join(names, ", "))
	}

	var pins []string
	if ok, err := h.aptRegularFile(aptFilePins); err != nil {
		f.add("%v", err)
	} else if ok {
		pins = append(pins, aptFilePins)
	}
	// apt 3.0.3 policy.cc line 394 reads files ending in .pref or without one.
	parts, err := h.aptFolderFiles(aptFolderPins, "pref", "")
	if err != nil {
		f.add("%v", err)
	}
	for _, pin := range append(pins, parts...) {
		f.add("found %s, which changes which Debian updates install", pin)
	}

	for _, file := range settings.files {
		for _, k := range settings.keys[file] {
			lk := strings.ToLower(k)
			// "#clear APT::Periodic" names the whole tree, "#clear APT" its parent.
			if lk == "apt" || nightlySetting(lk) {
				f.add("found %s in %s, which stops or narrows the nightly updates", strings.TrimSuffix(k, "::"), file)
			}
		}
	}

	// The nightly job runs from Debian's two apt timers. Only an enabled
	// timer starts again after a restart, and only a running one starts the
	// job before it. The job's own units have no install section, so they
	// are never enabled, but a masked one never runs.
	units := append(slices.Clone(system.AptTimers), debianAptJobUnits...)
	states, err := h.run("systemctl", append([]string{"show", "--property=" + system.AptTimerProperties + ",ExecStart,DropInPaths"}, units...)...)
	if err != nil {
		f.add("cannot read the nightly update timers: %v", err)
	} else {
		parsed := system.ParseUnitProperties(states)
		starts := jobStarts(states)
		for _, timer := range system.AptTimers {
			switch state, err := system.JudgeAptTimer(parsed[timer]); {
			case err != nil:
				f.add("%v", err)
			case state == system.AptTimerOff:
				f.add("found %s switched off, which stops the nightly updates", timer)
			case state == system.AptTimerStopped:
				f.add("found %s stopped, which stops the nightly updates", timer)
			}
		}
		for _, unit := range debianAptJobUnits {
			if strings.HasPrefix(parsed[unit]["UnitFileState"], "masked") {
				f.add("found %s switched off, which stops the nightly updates", unit)
				continue
			}
			if got := starts[unit]; !slices.Equal(got, []string{debianAptJobStart[unit]}) {
				where := "in its settings"
				if dropIns := parsed[unit]["DropInPaths"]; dropIns != "" {
					where = "in " + strings.Join(strings.Fields(dropIns), " and ")
				}
				f.add("found %s set to start %q %s, where Debian's nightly job starts %q", unit, strings.Join(got, " and "), where, debianAptJobStart[unit])
			}
		}
	}

	// Debian's backports add ~bpo to every version (backports.debian.org,
	// Contribute, "Append ~bpo${release}+${build} to the version number").
	out, err := h.run("dpkg-query", "-W", "-f=${db:Status-Status} ${binary:Package} ${Version}\n")
	if err != nil {
		f.add("cannot read the installed packages: %v", err)
	}
	var backports []string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == "installed" && strings.Contains(fields[2], "~bpo") {
			backports = append(backports, fields[1])
		}
	}
	if len(backports) > 0 {
		f.add("found packages from Debian's backports, which get no security team updates (%s)", strings.Join(backports, ", "))
	}
	return f.err("undo", aptFixUpdates)
}

// CheckNightlyValues is the fact "Debian's nightly job runs with vpn's
// values", asked of apt's merged settings right after step 10 writes them and
// when the install finishes. The start check reads the machine's files
// instead, since Debian's own 20auto-upgrades can hold "0" before step 10
// writes vpn's file, which is read after it.
func CheckNightlyValues() error {
	h := productionAptHost()
	settings, err := h.aptSettings()
	var dump []aptSetting
	if err == nil {
		dump, err = h.aptDump()
	}
	if err != nil {
		return err
	}
	return nightlyVerdict(settings, dump)
}

// nightlyVerdict holds when the nightly settings in apt's answer are
// exactly vpn's: each of its values, its origins, and no other
// APT::Periodic:: or Unattended-Upgrade:: setting, the rule the start check
// applies to the machine's files. Other settings of unattended-upgrade stop
// the job (APT::Periodic::Enable "0", InstallOnShutdown) or narrow it
// (Package-Blacklist, Package-Whitelist, Update-Days).
func nightlyVerdict(settings aptSettingFiles, dump []aptSetting) error {
	const why = "stops or narrows the nightly updates"
	want := map[string]string{}
	for _, v := range nightlyValues {
		want[v.key] = v.value
	}
	var f findings
	var origins []string
	seen := map[string]bool{}
	for _, s := range dump {
		lk := strings.ToLower(s.key)
		if !strings.HasPrefix(lk, "apt::periodic::") && !strings.HasPrefix(lk, "unattended-upgrade::") {
			continue
		}
		switch {
		case s.key == "Unattended-Upgrade::Origins-Pattern::":
			origins = append(origins, s.value)
		case s.key == "Unattended-Upgrade::Origins-Pattern" && s.value == "":
		case s.value != "" && want[s.key] == s.value && !seen[s.key]:
			// None of vpn's values is empty, so an empty one is another
			// setting's.
			seen[s.key] = true
		default:
			// A list's entries are named by the list.
			name := strings.TrimSuffix(s.key, "::")
			files := append(settings.filesSetting(name), settings.filesSetting(name+"::")...)
			if len(files) == 0 {
				files = []string{"apt's settings"}
			}
			for _, file := range files {
				f.add("found %s in %s, which %s", name, file, why)
			}
		}
	}
	for _, v := range nightlyValues {
		if !seen[v.key] && len(settings.filesSetting(v.key)) == 0 {
			f.add("found no %s in apt's settings, which vpn sets to %s", v.key, v.value)
		}
	}
	if !slices.Equal(origins, vpnOrigins) {
		f.addFor(settings, "Unattended-Upgrade::Origins-Pattern", why)
	}
	return f.err("undo", aptFixUpdates)
}

// debianAptJobStart is the one command each of the nightly job's services
// starts in Debian's apt package.
var debianAptJobStart = map[string]string{
	"apt-daily.service":         "/usr/lib/apt/apt.systemd.daily update",
	"apt-daily-upgrade.service": "/usr/lib/apt/apt.systemd.daily install",
}

// jobStarts reads each unit's commands from systemctl show, which prints one
// ExecStart line for each command, holding the program's path and then the
// command line it runs with.
// A drop-in adds a command to a oneshot service unless it first empties the
// list, so a service can hold more than one.
func jobStarts(out string) map[string][]string {
	starts := map[string][]string{}
	for block := range strings.SplitSeq(out, "\n\n") {
		var id string
		var cmds []string
		for line := range strings.SplitSeq(block, "\n") {
			if v, ok := strings.CutPrefix(line, "Id="); ok {
				id = v
			}
			if v, ok := strings.CutPrefix(line, "ExecStart="); ok {
				path, argv, _ := strings.Cut(v, " ; argv[]=")
				path = strings.TrimPrefix(path, "{ path=")
				argv, _, _ = strings.Cut(argv, " ; ")
				// "ExecStart=@path argv" runs path under another name.
				if first, _, _ := strings.Cut(argv, " "); first != path {
					argv = "@" + path + " " + argv
				}
				cmds = append(cmds, argv)
			}
		}
		if id != "" {
			starts[id] = cmds
		}
	}
	return starts
}

// ── apt uses no proxy ────────────────────────────────────

// checkProxy refuses any apt proxy setting in the machine's own files, any
// setting of an apt method or of the programs apt runs, and no_proxy for
// every service. apt 3.0.3 takes a server's own proxy first, where "DIRECT"
// connects directly, and drops the proxy for a server no_proxy names
// (methods/http.cc lines 436 to 469). A method also reads its own
// Binary::<name>:: settings (methods/aptmethod.h line 128), and
// Dir::Bin::Methods::<name> picks the program for a method
// (acquire-worker.cc line 98). Each would let a tor+https source skip Tor.
func (h aptHost) checkProxy(settings aptSettingFiles, dump []aptSetting) error {
	var f findings
	for _, file := range settings.files {
		for _, k := range settings.keys[file] {
			lk := strings.ToLower(k)
			switch {
			case isAptProxySetting(withoutBinary(k)):
				f.add("found an apt proxy setting in %s (%s)", file, k)
			case strings.HasPrefix(lk, "binary::") || strings.HasPrefix(lk, "dir::bin::"):
				f.add("found %s in %s, which changes how apt reaches Debian", k, file)
			}
		}
	}
	// apt's own answer, for a setting no file above shows, such as one of
	// vpn's files changed or APT_CONFIG. vpn's own Tor proxy is apt's
	// default.
	for _, s := range dump {
		if isAptProxySetting(withoutBinary(s.key)) && !isVPNTorProxy(s) && len(settings.filesSetting(s.key)) == 0 {
			f.add("found an apt proxy setting (%s)", s.key)
		}
	}

	env, err := h.run("systemctl", "show-environment")
	if err != nil {
		f.add("cannot read the settings systemd gives every service: %v", err)
	}
	for _, line := range strings.Split(env, "\n") {
		if strings.HasPrefix(line, "no_proxy=") {
			f.add("found no_proxy in the settings systemd gives every service")
		}
	}
	return f.err("remove", aptFixProxy)
}

// isAptProxySetting matches Acquire::<method>::Proxy, a server's own
// Proxy::<host>, and the automatic ones (Proxy-Auto-Detect, ProxyAutoDetect).
func isAptProxySetting(key string) bool {
	parts := strings.Split(strings.ToLower(key), "::")
	return len(parts) >= 3 && parts[0] == "acquire" && strings.HasPrefix(parts[2], "proxy")
}

func isVPNTorProxy(s aptSetting) bool {
	return strings.EqualFold(s.key, "Acquire::tor::Proxy") && s.value == aptTorProxy
}

// withoutBinary drops a Binary::<name>:: prefix, which apt moves to the top
// of the settings of the program it names.
func withoutBinary(key string) string {
	parts := strings.SplitN(key, "::", 3)
	if len(parts) == 3 && strings.EqualFold(parts[0], "Binary") {
		return parts[2]
	}
	return key
}

// ── Step 9 ───────────────────────────────────────────────

func (h aptHost) configureAptTor(write func(string, []byte, os.FileMode) error) error {
	if _, err := os.Stat(h.path(aptTorMethod)); err != nil {
		return fmt.Errorf("apt's Tor method is missing (%s)", aptTorMethod)
	}
	// The components the machine's Debian sources use, so those packages
	// keep updating. Read before anything changes. A rerun after a kill
	// before the settings file is written still reads the machine's sources.
	// A rerun after it reads vpn's file and gets the same.
	targets, err := h.aptTargets()
	if err != nil {
		return err
	}
	var components []string
	for _, c := range debianComponents {
		for _, t := range targets {
			if t.of == "deb" && t.component == c && slices.Contains(debianSuites, t.suite) {
				components = append(components, c)
				break
			}
		}
	}
	if len(components) == 0 {
		components = []string{"main"}
	}

	// vpn's folder and its sources file come first. apt does not read them
	// yet.
	folder := h.path(paths.AptSourcesDir)
	if err := os.MkdirAll(folder, 0o755); err != nil {
		return err
	}
	if err := os.Chmod(folder, 0o755); err != nil {
		return err
	}
	if err := files.Sync(filepath.Dir(folder)); err != nil {
		return err
	}
	if err := write(paths.AptSources, []byte(vpnDebianSources(components)), 0o644); err != nil {
		return err
	}
	// The switch, in one write. Before it apt is as the earlier steps left
	// it. After it apt's plain methods are off and it reads vpn's sources
	// alone.
	if err := write(paths.AptTor, []byte(aptTorSettings), 0o644); err != nil {
		return err
	}
	return h.verifyAptTor()
}

// CheckAptThroughTor is the fact "apt reaches Debian through Tor only",
// step 9's own check, asked again when the install finishes.
func CheckAptThroughTor() error {
	return productionAptHost().verifyAptTor()
}

// verifyAptTor checks the step's result with apt's own answers. Only vpn's
// two Tor sources are listed, the plain methods are off, no other program
// serves a method, and no proxy is set but vpn's.
func (h aptHost) verifyAptTor() error {
	fail := func(setting string) error {
		return fmt.Errorf("apt would not reach deb.debian.org through Tor (%s)", setting)
	}
	targets, err := h.aptTargets()
	if err != nil {
		return err
	}
	for _, t := range targets {
		uri := strings.TrimSuffix(t.uri, "/")
		if t.file != paths.AptSources || (uri != aptDebianMain && uri != aptDebianSecurity) {
			return fail(t.file + " " + uri)
		}
	}
	dump, err := h.aptDump()
	if err != nil {
		return err
	}
	off := map[string]bool{}
	for _, s := range dump {
		k := strings.ToLower(withoutBinary(s.key))
		switch {
		case (k == "dir::bin::methods::http" || k == "dir::bin::methods::https") && strings.EqualFold(k, s.key):
			off[k] = s.value == "false"
		case strings.HasPrefix(k, "dir::bin::methods::"):
			return fail(s.key + " " + s.value)
		case isAptProxySetting(k) && !isVPNTorProxy(s):
			return fail(s.key + " " + s.value)
		}
	}
	for _, k := range []string{"dir::bin::methods::http", "dir::bin::methods::https"} {
		if !off[k] {
			return fail(k)
		}
	}
	return nil
}
