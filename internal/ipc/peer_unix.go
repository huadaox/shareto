//go:build unix

package ipc

import (
	"fmt"
	"net"
	"os"
)

func currentUserID() uint32 {
	return uint32(os.Getuid())
}

func verifyPeerUID(conn net.Conn, expected uint32) error {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return fmt.Errorf("IPC requires a Unix domain socket")
	}
	raw, err := unixConn.SyscallConn()
	if err != nil {
		return err
	}
	var uid uint32
	var credentialErr error
	if err := raw.Control(func(fd uintptr) {
		uid, credentialErr = socketPeerUID(int(fd))
	}); err != nil {
		return err
	}
	if credentialErr != nil {
		return credentialErr
	}
	if uid != expected {
		return fmt.Errorf("IPC peer UID %d does not match user UID %d", uid, expected)
	}
	return nil
}
