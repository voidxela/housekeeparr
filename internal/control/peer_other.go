//go:build !unix

package control

import "net"

func umask(m int) int { return m }

func peerIsOwner(net.Conn) bool { return true }
