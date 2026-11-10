//go:build !linux

package vpnd

import (
	"errors"
	"net"
)

// peerCredentials carries the kernel's statement about the
// process on the other end of a unix socket connection.
type peerCredentials struct {
	Uid uint32
	Gid uint32
	Pid int32
}

// peerCred exists on non-Linux only so the tree compiles for
// development. vpnd runs only on the node's
// Debian target, where the Linux implementation applies; here
// it refuses every connection.
func peerCred(c *net.UnixConn) (*peerCredentials, error) {
	return nil, errors.New(
		"peer credentials are only supported on Linux")
}
