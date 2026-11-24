package host

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The order check is a function of one folder and one file. Each case is a
// machine vpn could meet: Debian's own files pass, and anything sshd reads
// before vpn's own file is refused and named.
func TestSSHReadOrder(t *testing.T) {
	const ours = "00-vpn-hardening.conf"
	debian := "\n# This is the sshd server system-wide configuration file.\n\n  # an indented comment\nInclude %s/*.conf\n\nKbdInteractiveAuthentication no\nUsePAM yes\n"
	for _, tc := range []struct {
		name  string
		main  string // %s is the folder, %S the folder in upper case
		files []string
		named []string // empty: the check passes
	}{
		{name: "Debian's own files", main: debian, files: []string{ours, "50-cloud-init.conf"}},
		{name: "a file sshd skips", main: debian, files: []string{".00-hidden.conf", "00-cloud.conf.bak", "00-cloud", ours}},
		{name: "a second pattern after Debian's", main: "Include %s/*.conf /etc/ssh/other/*.conf\n", files: []string{ours}},
		{name: "a comment after the pattern", main: "Include %s/*.conf # Debian\n", files: []string{ours}},
		{name: "a keyword in other case", main: "include %s/*.conf\n", files: []string{ours}},
		{name: "an earlier file", main: debian, files: []string{"00-cloud.conf", ours}, named: []string{"00-cloud.conf"}},
		{name: "a setting above the include line", main: "# a comment\nPasswordAuthentication yes\nInclude %s/*.conf\n", files: []string{ours}, named: []string{"line 2"}},
		{name: "no include line", main: "PasswordAuthentication no\n", files: []string{ours}, named: []string{"no line"}},
		{name: "the path in upper case", main: "Include %S/*.conf\n", files: []string{ours}, named: []string{"no line"}},
		{name: "a pattern before Debian's", main: "Include /etc/ssh/early/*.conf %s/*.conf\n", files: []string{ours}, named: []string{"no line"}},
		{name: "two findings at once", main: "UsePAM yes\nInclude %s/*.conf\n", files: []string{"00-Zeta.conf", "00-cloud.conf", ours}, named: []string{"line 1", "00-Zeta.conf", "00-cloud.conf"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "sshd_config.d")
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, name := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte("Match all\nKbdInteractiveAuthentication yes\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			main := filepath.Join(root, "sshd_config")
			body := strings.NewReplacer("%s", dir, "%S", strings.ToUpper(dir)).Replace(tc.main)
			if err := os.WriteFile(main, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			err := sshReadOrder(main, dir, ours)
			if len(tc.named) == 0 {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("passed, want %v named", tc.named)
			}
			// Each finding is a paragraph that ends with its own fix.
			if got := strings.Count(err.Error(), "\n\n") + 1; got != 2*len(tc.named) {
				t.Errorf("%d paragraphs, want a finding and a fix for each of %v: %q", got, tc.named, err)
			}
			for _, want := range tc.named {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("%q does not name %s", err, want)
				}
			}
		})
	}
}

// A file or a folder that cannot be read is not a pass.
func TestSSHReadOrderRefusesWhatItCannotRead(t *testing.T) {
	root := t.TempDir()
	if err := sshReadOrder(filepath.Join(root, "missing"), root, "00-vpn-hardening.conf"); err == nil || !strings.Contains(err.Error(), "cannot read") {
		t.Fatalf("got %v, want a refusal that keeps its cause", err)
	}
	main := filepath.Join(root, "sshd_config")
	if err := os.WriteFile(main, []byte("Include "+main+"/*.conf\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := sshReadOrder(main, main, "00-vpn-hardening.conf"); err == nil || !strings.Contains(err.Error(), "cannot list") {
		t.Fatalf("got %v, want a refusal for a folder that cannot be listed", err)
	}
}
