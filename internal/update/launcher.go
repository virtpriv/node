package update

import (
	"os"
	"os/exec"
	"path/filepath"
)

// Launch is a small, retained entry point. It understands the job envelope and
// checks the approved target bytes; the target implements its own host changes.
func Launch() error {
	if err := requireUpdateHost(); err != nil {
		return err
	}
	j, err := loadJob(Root)
	if err != nil || j == nil {
		return err
	}
	if !j.active() || j.Phase == "failed" {
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
