//go:build linux

package vpnd

import (
	"net"
	"syscall"
)

// peerCredentials carries the kernel's statement about the
// process on the other end of a unix socket connection.
type peerCredentials struct {
	Uid uint32
	Gid uint32
	Pid int32
}

// peerCred reads SO_PEERCRED from a live connection without
// stealing its fd. The uid/gid are kernel-recorded facts about
// the connecting process at connect time; the pid is a
// snapshot that may be reused, so it is logged as context but
// never used for decisions.
func peerCred(c *net.UnixConn) (*peerCredentials, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return nil, err
	}
	var cred *syscall.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd),
			syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return nil, err
	}
	if credErr != nil {
		return nil, credErr
	}
	return &peerCredentials{
		Uid: cred.Uid, Gid: cred.Gid, Pid: cred.Pid,
	}, nil
}
