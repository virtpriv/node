package host

// The system journal, kept on disk. Debian 13 keeps it there by default, but
// a provider's image can add a journald file that keeps it in memory. Then the
// root helper's record of privileged actions and the logs an owner reads are
// lost at every restart. vpn writes its own journald file, named to sort after
// the machine's files, which is systemd's way to override one setting, and
// leaves the machine's files as they are.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/virtpriv/node/internal/logger"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/system"
)

// journaldSettings is vpn's journald file. No size is set, so Debian's
// default applies: 10% of the disk, at most 4 GiB.
const journaldSettings = `# Virtual Private Node keeps the system journal on disk, so logs survive a restart.
# Its name sorts last, so this setting wins over the machine's own files.
[Journal]
Storage=persistent
`

// CheckJournalStorage is the install check "system logs can be kept on
// disk". It writes nothing.
func CheckJournalStorage() error {
	out, err := runAptTool("systemd-analyze", "cat-config", "--tldr", "systemd/journald.conf")
	if err != nil {
		return fmt.Errorf("cannot read journald's settings: %w", err)
	}
	return journalStorageVerdict(out)
}

// journalStorageVerdict reads systemd-analyze cat-config's output. With
// --tldr it leaves out every comment and empty line of the files, so a line
// that starts with "# " is the heading of the next file, in the order journald
// reads them. systemd sorts the files in the journald.conf.d folders by name,
// whatever their folder, and the last value of a setting wins. So only a file
// whose name sorts after vpn's can override vpn's Storage=. Each such file
// that sets another value, after the last one that sets persistent, is
// named. The main journald.conf is read before every one of them.
func journalStorageVerdict(catConfig string) error {
	type setting struct{ file, value string }
	ours := filepath.Base(paths.JournaldDropIn)
	var later []setting
	file, section, files := "", "", 0
	for _, raw := range strings.Split(catConfig, "\n") {
		line := strings.TrimSpace(raw)
		if path, ok := strings.CutPrefix(line, "# "); ok {
			file, section = path, ""
			files++
			continue
		}
		if strings.HasPrefix(line, "[") {
			section = line
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || section != "[Journal]" || strings.TrimSpace(key) != "Storage" {
			continue
		}
		if filepath.Base(filepath.Dir(file)) == "journald.conf.d" && filepath.Base(file) > ours {
			later = append(later, setting{file, strings.TrimSpace(value)})
		}
	}
	if files == 0 {
		return errors.New("cannot read journald's settings: systemd-analyze printed no settings file")
	}
	var f findings
	for _, s := range later {
		if s.value == "persistent" {
			f = nil
			continue
		}
		f.add("found Storage=%s in %s, which comes after vpn's own setting", s.value, s.file)
	}
	return f.err("remove", "so system logs can be kept on disk")
}

// SetupJournalAccess is the journal.access step: keep the system journal on
// disk, since an audit record that vanishes at a restart is not much of a
// record, and let the admin user read it.
func SetupJournalAccess() error {
	if err := keepJournalOnDisk(); err != nil {
		return err
	}

	// systemd-journal membership grants read on ALL system
	// journals, sshd's included. The TUI shows the node's service
	// logs from them. Read-only: members cannot write or rewrite
	// journal files. -aG, never -G: a bare -G REPLACES the
	// supplementary group set.
	if err := system.RunRoot("usermod", "-aG",
		"systemd-journal", paths.AdminUser); err != nil {
		return err
	}
	// Postcondition: the membership is really there.
	out, err := system.RunOutput("id", "-nG", paths.AdminUser)
	if err != nil {
		return fmt.Errorf("verify group membership: %w", err)
	}
	for _, g := range strings.Fields(out) {
		if g == "systemd-journal" {
			return nil
		}
	}
	return fmt.Errorf(
		"%s is not in systemd-journal after usermod (groups: %s)",
		paths.AdminUser, out)
}

// keepJournalOnDisk writes vpn's journald file, restarts journald, moves the
// journal kept in memory to disk and checks that the journal file is there.
// Each part can run again after a kill.
func keepJournalOnDisk() error {
	if err := os.MkdirAll(filepath.Dir(paths.JournaldDropIn), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(paths.JournaldDropIn), err)
	}
	if err := system.WriteFileRoot(paths.JournaldDropIn, []byte(journaldSettings), 0o644); err != nil {
		return err
	}
	// Debian's README.Debian way to create the folder. The tmpfiles pass
	// gives it the journal group and its read rights.
	if err := os.MkdirAll(paths.JournalDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", paths.JournalDir, err)
	}
	if err := system.RunRoot("systemd-tmpfiles", "--create",
		"--prefix", paths.JournalDir); err != nil {
		return err
	}
	// journald reads its settings only when it starts. Running services keep
	// their connection to it across the restart.
	if err := system.RunRoot("systemctl", "restart", "systemd-journald"); err != nil {
		return err
	}
	if err := system.RunRoot("journalctl", "--flush"); err != nil {
		return err
	}
	id, err := os.ReadFile(paths.MachineID)
	if err != nil {
		return fmt.Errorf("read %s: %w", paths.MachineID, err)
	}
	machine := strings.TrimSpace(string(id))
	file := filepath.Join(paths.JournalDir, machine, "system.journal")
	if _, err := os.Stat(file); err != nil {
		return fmt.Errorf("system logs are still kept in memory after restarting journald, %s is missing", file)
	}
	// A journal file can be left from an earlier boot. journald removes the
	// folder it keeps in memory only when the flush to disk worked.
	memory := filepath.Join(paths.JournalRuntimeDir, machine)
	if _, err := os.Stat(memory); err == nil {
		return fmt.Errorf("system logs are still kept in memory after restarting journald, %s is still there", memory)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("check %s: %w", memory, err)
	}
	logger.Install("System logs are kept on disk in %s", paths.JournalDir)
	return nil
}
