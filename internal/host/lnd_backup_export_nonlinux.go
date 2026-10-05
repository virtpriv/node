//go:build !linux

package host

import (
	"fmt"

	"github.com/virtpriv/node/internal/config"
)

// PublishLNDBackup is available only on the certified Linux target because
// its path-resolution and publication guarantees use Linux openat2.
func PublishLNDBackup(network string) error {
	if err := config.ValidateNetwork(network); err != nil {
		return err
	}
	return fmt.Errorf(
		"LND backup publication is supported only on Linux, not %q",
		network)
}
