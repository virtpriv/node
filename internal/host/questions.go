package host

// The questions behind vpn's promises to owners. Each fact is asked of the
// program that decides it, and an answer vpn cannot read refuses, as one
// that does not hold does. A fact is asked from the first moment it can
// hold: before the install changes anything, right after the step that
// writes its part, when the install finishes and at a later change of the
// part.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
)

const (
	// aptToolTimeout limits apt's tools and the programs the apt checks ask.
	aptToolTimeout = 60 * time.Second
	// questionTimeout limits sshd, ss, iptables-save and fail2ban-client,
	// which had no limit before. About thirty times the slowest of them on an
	// emulated VM.
	questionTimeout = 30 * time.Second
)

// askTool runs one of Debian's tools in the C locale, and systemd's tools
// without colors or links, whatever the caller's environment asks, and stops
// it after limit. When it fails, the error carries the tool's own message,
// which names the file at fault.
func askTool(limit time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "SYSTEMD_COLORS=0", "SYSTEMD_URLIFY=0")
	cmd.WaitDelay = 2 * time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("%s did not answer within %d seconds", name, int(limit.Seconds()))
		}
		if msg := strings.Join(strings.Fields(stderr.String()), " "); msg != "" {
			return "", fmt.Errorf("%s: %s", name, msg)
		}
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// runAptTool is askTool with apt's limit.
func runAptTool(name string, args ...string) (string, error) {
	return askTool(aptToolTimeout, name, args...)
}

// findings collects one check's findings, each a paragraph of its own.
type findings []string

func (f *findings) add(format string, args ...any) {
	s := fmt.Sprintf(format, args...)
	if !slices.Contains(*f, s) {
		*f = append(*f, s)
	}
}

// err joins the findings and then the fix, which starts with undo, the word
// for what to do with a finding. A file or answer that could not be read gets
// no fix, since undoing it is not the advice.
func (f findings) err(undo, fix string) error {
	if len(f) == 0 {
		return nil
	}
	found := 0
	for _, s := range f {
		if !strings.HasPrefix(s, "cannot read ") {
			found++
		}
	}
	text := strings.Join(f, "\n\n")
	switch {
	case found == 1:
		text += "\n\n" + undo + " it, " + fix
	case found > 1:
		text += "\n\n" + undo + " them, " + fix
	}
	return errors.New(text)
}
