//go:build windows

package ipc

import (
	"fmt"
	"net"
)

func currentUserID() uint32 {
	return 0
}

func verifyPeerUID(conn net.Conn, expected uint32) error {
	_, ok := conn.(*net.UnixConn)
	if !ok {
		return fmt.Errorf("IPC requires a Unix domain socket")
	}
	return nil
}
