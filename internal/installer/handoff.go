package installer

import (
	"fmt"

	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/system"
)

// printConnectInstructions leaves the login command in the shell after every
// install, attended or not. The owner opens the node TUI by logging in over SSH.
func printConnectInstructions() {
	ip := system.PublicIPv4()
	target := paths.AdminUser + "@<your-server-ip>"
	if ip != "" {
		target = paths.AdminUser + "@" + ip
	}
	fmt.Printf("\n  Install complete.\n\n  Connect to your node:\n\n    ssh %s\n\n", target)
}
