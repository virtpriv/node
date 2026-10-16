package host

// Compressed swap in memory, from Debian's systemd-zram-generator package
// with its own defaults: half the memory, at most 4 GiB. When memory runs
// short, the kernel compresses memory nobody is using into it instead of
// stopping a program. It never writes to the disk. Debian sets it up again
// at every boot, so vpn writes no settings file. Swap the host already has
// is left as it is, and zram is used before it.

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/virtpriv/node/internal/logger"
	"github.com/virtpriv/node/internal/system"
)

var (
	errZramNotOn = errors.New("zram swap is not on after starting it")
	errZramUnit  = errors.New("zram unit is broken")
)

type zramOps struct {
	moduleAvailable func() bool
	swaps           func() (string, error)
	install         func() error
	unitState       func() (string, error)
	start           func() error
	log             func(string)
}

// SetUpCompressedSwap installs the package through apt, which goes through
// Tor by then, and turns zram on now, without a reboot. A kernel without zram,
// or zram switched off on the machine, installs as before. Any other failure
// is returned, so a resume tries again.
func SetUpCompressedSwap() error {
	return setUpCompressedSwap(zramOps{
		moduleAvailable: func() bool {
			_, err := system.RunOutput("modinfo", "-n", "zram")
			return err == nil
		},
		swaps: func() (string, error) {
			data, err := os.ReadFile("/proc/swaps")
			return string(data), err
		},
		install: func() error {
			return system.RunRoot("apt-get", "install", "-y", "-qq", "systemd-zram-generator")
		},
		unitState: func() (string, error) {
			return system.RunOutput("systemctl", "show", "--property=LoadState", "--value", "dev-zram0.swap")
		},
		start: func() error {
			return system.RunRoot("systemctl", "start", "dev-zram0.swap")
		},
		log: func(line string) { logger.Install("%s", line) },
	})
}

func setUpCompressedSwap(ops zramOps) error {
	swaps, err := ops.swaps()
	if err != nil {
		return fmt.Errorf("read swap list: %w", err)
	}
	// zram set up by the provider, or by this step before an interrupted
	// install, stays as it is. It is checked first, because zram built into
	// a provider's kernel is on without a module modinfo can find.
	if zramOn(swaps) {
		ops.log("compressed swap is already on, left as it is")
		return nil
	}
	if !ops.moduleAvailable() {
		ops.log("this kernel has no zram, no compressed swap")
		return nil
	}
	if err := ops.install(); err != nil {
		return fmt.Errorf("install systemd-zram-generator: %w", err)
	}
	// The package makes no zram unit when zram is switched off on purpose,
	// by the kernel option systemd.zram=0 or a settings file, or inside a
	// container. A masked unit is switched off by the owner. That choice is
	// kept. A unit in any other state is broken and fails the step.
	state, err := ops.unitState()
	if err != nil {
		return fmt.Errorf("look for the zram unit: %w", err)
	}
	switch state {
	case "loaded":
	case "not-found", "masked":
		ops.log("zram is switched off on this machine, no compressed swap")
		return nil
	default:
		return fmt.Errorf("%w: load state %q", errZramUnit, state)
	}
	if err := ops.start(); err != nil {
		return fmt.Errorf("start compressed swap: %w", err)
	}
	swaps, err = ops.swaps()
	if err != nil {
		return fmt.Errorf("read swap list: %w", err)
	}
	if !zramOn(swaps) {
		return errZramNotOn
	}
	ops.log("compressed swap is on")
	return nil
}

// zramOn reports whether /proc/swaps lists a zram device. Its first line is
// a header and each later line starts with the swap's name.
func zramOn(swaps string) bool {
	lines := strings.Split(swaps, "\n")
	for _, line := range lines[min(1, len(lines)):] {
		if fields := strings.Fields(line); len(fields) > 0 && strings.HasPrefix(fields[0], "/dev/zram") {
			return true
		}
	}
	return false
}
