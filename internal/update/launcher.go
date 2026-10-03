package update

import (
	"os"
	"os/exec"
	"path/filepath"
)

// Launch is a small, retained entry point. It reads only the installed side's
// part of the record and checks the approved target bytes; the target
// implements its own host changes.
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
	cmd := exec.Command(path, "update-worker")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}
