package installer

import (
	"github.com/virtpriv/node/internal/host"
	"github.com/virtpriv/node/internal/logger"
)

// The owner has a password and usable sudo before root SSH is disabled.
// Recheck sudo on resume even when identity.access was already recorded.
func installSSHHardening() error {
	if err := host.VerifyOperatorSudo(); err != nil {
		return err
	}
	if err := host.ApplySSHHardening("yes"); err != nil {
		return err
	}
	logger.Install("SSH hardening applied: root login disabled; owner password login enabled")
	return nil
}
