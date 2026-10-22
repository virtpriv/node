package host

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/virtpriv/node/internal/paths"
)

// AdminLoginObserved checks the first-login contract: any accepted SSH
// authentication for vpn, including password authentication, since the given
// time, or in the whole log when it is zero. su is not evidence, nor does this
// prove an independent administrator path. Root work has its own bound.
func AdminLoginObserved(since time.Time) (bool, error) {
	if os.Geteuid() != 0 {
		return false, errors.New("SSH login evidence requires root")
	}
	return observeAdminLogin(10*time.Second, since, exec.CommandContext)
}

func observeAdminLogin(timeout time.Duration, since time.Time, command func(context.Context, string, ...string) *exec.Cmd) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	args := []string{"-u", "ssh"}
	if !since.IsZero() {
		args = append(args, fmt.Sprintf("--since=@%d", since.Unix()))
	}
	// Do not use --grep: journalctl's no-match exit status is also used for
	// failures. An empty successful stream means pending, a failed read is unknown.
	cmd := command(ctx, "/usr/bin/journalctl", append(args, "--no-pager", "-o", "cat", "--quiet")...)
	journal := &loginEvidenceWriter{}
	cmd.Stdout = journal
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return false, fmt.Errorf("read SSH login evidence: %w", err)
	}
	return journal.found || journalShowsAdminLogin(string(journal.line)), nil
}

// Stream the journal without retaining its history. Oversized records fail the
// observation instead of silently classifying incomplete evidence as pending.
type loginEvidenceWriter struct {
	line  []byte
	found bool
}

func (w *loginEvidenceWriter) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 && !w.found {
		line, rest, complete := bytes.Cut(p, []byte{'\n'})
		if len(w.line)+len(line) > 1<<20 {
			return 0, errors.New("SSH journal record exceeds observation limit")
		}
		w.line = append(w.line, line...)
		if !complete {
			break
		}
		w.found = journalShowsAdminLogin(string(w.line))
		w.line = w.line[:0]
		p = rest
	}
	return n, nil
}

func journalShowsAdminLogin(journal string) bool {
	needle := " for " + paths.AdminUser + " from "
	for _, line := range strings.Split(journal, "\n") {
		if strings.Contains(line, "Accepted ") && strings.Contains(line, needle) {
			return true
		}
	}
	return false
}
