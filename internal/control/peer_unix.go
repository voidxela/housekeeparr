//go:build unix && !linux

package control

import (
	"net"
	"syscall"
)

func umask(m int) int { return syscall.Umask(m) }

// peerIsOwner relies on the socket's 0600 mode on platforms without
// SO_PEERCRED here.
func peerIsOwner(net.Conn) bool { return true }
