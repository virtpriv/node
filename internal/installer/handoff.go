package installer

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/virtpriv/node/internal/logger"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/system"
	"golang.org/x/term"
)

// HandoffToAdminConsole opens the owner TUI through a separate pseudo-terminal.
// runuser supplies the identity, login environment and PAM session. The original
// terminal retains its owner, and the original SSH login retains its identity.
// Installation is already complete; failure leaves connection instructions.
func HandoffToAdminConsole() {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		printConnectInstructions()
		return
	}
	// Set this after runuser clears the environment, only for the handoff TUI.
	cmd := exec.Command("/usr/sbin/runuser", "--pty", "--login", paths.AdminUser,
		"--command", "VPN_TUI_NO_SUSPEND=1 "+paths.BinaryPath)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		logger.Install("handoff: owner console ended with error: %v", err)
		printConnectInstructions()
	}
}

// printConnectInstructions also handles unattended runs and unavailable terminals.
func printConnectInstructions() {
	ip := system.PublicIPv4()
	target := paths.AdminUser + "@<your-server-ip>"
	if ip != "" {
		target = paths.AdminUser + "@" + ip
	}
	fmt.Printf("\n  Install complete.\n\n  Connect to your node:\n\n    ssh %s\n\n", target)
}
