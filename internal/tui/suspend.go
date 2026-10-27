package tui

import "golang.org/x/sys/unix"

// processGroups reports the TUI's process group and session. It is a
// variable so tests can stand in for the terminal.
var processGroups = func() (pgid, sid int, err error) {
	if pgid, err = unix.Getpgid(0); err != nil {
		return 0, 0, err
	}
	sid, err = unix.Getsid(0)
	return pgid, sid, err
}

// canSuspend reports whether a shell can take the TUI back after ctrl+z. A
// shell with job control runs the TUI in a process group of its own and
// resumes it with fg. Started as the session's own group, from a login file
// without job control or as a single command over SSH, nothing would resume
// it: the stop signal is dropped and Bubble Tea would wait forever. When the
// answer cannot be read the key is ignored too.
func canSuspend() bool {
	pgid, sid, err := processGroups()
	return err == nil && pgid != sid
}
