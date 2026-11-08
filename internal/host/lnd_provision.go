package host

import (
	"fmt"
	"os"
	"os/user"
	"strconv"
	"time"

	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/system"
)

func createLNDDirs(username string) error {
	dirs := []struct {
		path  string
		owner string
		mode  os.FileMode
	}{
		{paths.LNDDir, "root:" + username, 0750},
		{paths.LNDDataDir, username + ":" + username, 0750},
	}
	for _, d := range dirs {
		if err := system.RunRoot("mkdir", "-p", d.path); err != nil {
			return err
		}
		if err := system.RunRoot("chown", d.owner, d.path); err != nil {
			return err
		}
		if err := system.RunRoot("chmod", fmt.Sprintf("%o", d.mode), d.path); err != nil {
			return err
		}
	}
	return nil
}

// WriteLNDService writes the LND unit without auto-unlock. The install is
// its only caller, and auto-unlock is turned on later from the TUI, which
// writes its own unit.
func WriteLNDService() error {
	return system.WriteFileRoot(paths.LNDService,
		[]byte(LNDServiceUnit(lndUser, false)), 0644)
}

// StartLNDWithNewTLSFiles is the install's first start of LND. LND writes
// its TLS pair at its first start without a sync, so a power cut soon after
// can leave both files empty, and LND then refuses every start. The install
// records this step only after it returns, once the pair is complete and on
// disk. A resume after a cut runs the step again from no TLS files. Any
// pair here was left by an earlier pass of this install, before anything
// used it. A file with another owner, mode or type is not removed, and the
// step fails.
func StartLNDWithNewTLSFiles() error {
	uid, gid, err := lndIDs()
	if err != nil {
		return err
	}
	if err := system.RunRoot("systemctl", "daemon-reload"); err != nil {
		return err
	}
	if err := system.RunRoot("systemctl", "stop", "lnd"); err != nil {
		return err
	}
	for _, name := range []string{paths.LNDTLSCert, paths.LNDTLSKey} {
		if err := removeExactFile(name, 0o600, uid, gid); err != nil {
			return fmt.Errorf("remove LND TLS file left by an earlier pass: %w", err)
		}
	}
	if err := system.RunRoot("systemctl", "enable", "lnd"); err != nil {
		return err
	}
	if err := system.RunRoot("systemctl", "restart", "lnd"); err != nil {
		return err
	}
	if _, err := waitLNDTLSFiles(paths.LNDTLSCert, paths.LNDTLSKey,
		os.ReadFile, 120*time.Second); err != nil {
		return err
	}
	return syncLNDFiles(paths.LNDTLSCert, paths.LNDTLSKey)
}

// lndIDs returns the uid and gid of LND's service account.
func lndIDs() (uid, gid int, err error) {
	account, err := user.Lookup(lndUser)
	if err != nil {
		return 0, 0, fmt.Errorf("resolve %s service account: %w", lndUser, err)
	}
	if uid, err = strconv.Atoi(account.Uid); err != nil {
		return 0, 0, fmt.Errorf("parse %s uid %q: %w", lndUser, account.Uid, err)
	}
	if gid, err = strconv.Atoi(account.Gid); err != nil {
		return 0, 0, fmt.Errorf("parse %s gid %q: %w", lndUser, account.Gid, err)
	}
	return uid, gid, nil
}
