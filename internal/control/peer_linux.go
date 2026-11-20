//go:build linux

package control

import (
	"net"
	"os"
	"syscall"
)

func umask(m int) int { return syscall.Umask(m) }

// peerIsOwner checks the connecting process's user (SO_PEERCRED): the
// socket's 0600 mode already limits it to this user; this also refuses a
// connection passed on by another user's process.
func peerIsOwner(c net.Conn) bool {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return false
	}
	var cred *syscall.Ucred
	var cerr error
	if err := raw.Control(func(fd uintptr) { cred, cerr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED) }); err != nil || cerr != nil {
		return false
	}
	return int(cred.Uid) == os.Getuid()
}
