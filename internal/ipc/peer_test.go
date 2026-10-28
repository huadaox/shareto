//go:build unix

package ipc

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestPeerCredentialsOnBothEnds(t *testing.T) {
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	path, err := filepath.Rel(workingDir, filepath.Join(t.TempDir(), "peer.sock"))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	for _, conn := range []net.Conn{client, server} {
		if err := verifyPeerUID(conn, uint32(os.Getuid())); err != nil {
			t.Fatalf("kernel peer credentials did not match the current user: %v", err)
		}
		if err := verifyPeerUID(conn, uint32(os.Getuid())+1); err == nil {
			t.Fatal("mismatched peer credentials were accepted")
		}
	}
}
