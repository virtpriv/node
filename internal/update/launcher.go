package update

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/virtpriv/node/internal/system"
)

// Launch is a small, retained entry point. It reads only the installed side's
// part of the record and checks the approved target bytes. The target
// implements its own host changes. Restarts wait while the target runs, so
// Debian's nightly restart never lands in the middle of an update. If they
// cannot be held off, the update still runs, since an update survives a
// restart and a stuck one would wait for the owner.
func Launch() error {
	if err := requireRoot(); err != nil {
		return err
	}
	j, err := loadRecord(Root)
	if err != nil || j == nil {
		return err
	}
	if !j.relaunch() {
		return nil
	}
	path := filepath.Join(j.dir(Root), "vpn")
	if err := verifyHash(path, j.WorkerHash); err != nil {
		return err
	}
	if inhibitor, err := system.InhibitShutdown("updating the node"); err != nil {
		fmt.Fprintf(os.Stderr, "vpn update: cannot hold off a restart, the update goes on: %v\n", err)
	} else {
		defer inhibitor.Release()
	}
	cmd := exec.Command(path, "update-worker")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}
