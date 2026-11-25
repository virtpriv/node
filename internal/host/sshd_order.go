package host

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/virtpriv/node/internal/paths"
)

// sshdMainConfig is Debian's main sshd file. Its first setting is the line
// that includes the folder vpn's own file is in.
const sshdMainConfig = "/etc/ssh/sshd_config"

// CheckSSHReadsVPNFirst is the check "sshd reads vpn's SSH settings first".
// sshd keeps the first value it reads, and a Match block in an earlier file
// beats vpn's own for the logins it matches, so an earlier file is refused by
// its name, whatever it holds. Each finding is a paragraph of its own and
// ends with its own fix, so the owner fixes all of them in one round.
func CheckSSHReadsVPNFirst() error {
	return sshReadOrder(sshdMainConfig, filepath.Dir(paths.SSHDDropIn), filepath.Base(paths.SSHDDropIn))
}

func sshReadOrder(mainFile, dir, ours string) error {
	data, err := os.ReadFile(mainFile)
	if err != nil {
		return fmt.Errorf("cannot read %s: %w", mainFile, err)
	}
	var found []string
	first, included := 0, false
	for n, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || strings.HasPrefix(f[0], "#") {
			continue
		}
		// The keyword is case blind, the path is not. More patterns may follow.
		if strings.EqualFold(f[0], "Include") && len(f) > 1 && f[1] == dir+"/*.conf" {
			included = true
			break
		}
		if first == 0 {
			first = n + 1
		}
	}
	switch {
	case !included:
		found = append(found, fmt.Sprintf("found no line in %s that includes %s, so sshd would not read vpn's own file\n\nUse a machine with Debian's own %s", mainFile, dir, mainFile))
	case first != 0:
		found = append(found, fmt.Sprintf("found a setting on line %d of %s, above the line that includes %s. sshd reads it before vpn's own file\n\nMove it below that line, so vpn's SSH settings hold for every login", first, mainFile, dir))
	}
	// sshd reads the folder's *.conf files in byte order and skips hidden
	// ones. v0.6.x's file also sorts earlier. The install refuses it before
	// its start checks run.
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		found = append(found, fmt.Sprintf("cannot list %s: %v", dir, err))
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".conf") && !strings.HasPrefix(name, ".") && name < ours {
			found = append(found, fmt.Sprintf("found %s, which sshd reads before vpn's own file\n\nRename it so its name sorts after %s, so vpn's SSH settings hold for every login", filepath.Join(dir, name), ours))
		}
	}
	if len(found) == 0 {
		return nil
	}
	return errors.New(strings.Join(found, "\n\n"))
}
