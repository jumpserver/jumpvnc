package vnc

import (
	"net"
	"os"
	"testing"
)

func TestConsoleDialerChecksPeerProcess(t *testing.T) {
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	address := listener.Addr().String()
	conn, err := dialConsoleProcess(t.Context(), address, uint32(os.Getpid()))
	if err != nil {
		t.Fatalf("rejecting owned console backend: %v", err)
	}
	conn.Close()
	if wrong, err := dialConsoleProcess(t.Context(), address, ^uint32(0)); err == nil {
		wrong.Close()
		t.Fatal("accepted console backend owned by another process")
	}
}
