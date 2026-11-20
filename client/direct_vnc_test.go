package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestDirectVNCLifecycle(t *testing.T) {
	for _, attach := range []bool{false, true} {
		name := "unclaimed"
		if attach {
			name = "attached"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("SSH_AUTH_SOCK", "")
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				conn.Write([]byte("RFB 003.008\n"))
				io.Copy(conn, conn)
			}()
			desktop := &Desktop{}
			defer desktop.Disconnect()
			addr := listener.Addr().(*net.TCPAddr)
			connection, err := desktop.Connect(Profile{Transport: "vnc", Host: "127.0.0.1", Port: addr.Port, VNCPassword: "fixture"})
			if err != nil {
				t.Fatal(err)
			}
			if connection.Files || connection.HostKey != nil || connection.URL == "" || connection.Password != "fixture" {
				t.Fatal("direct VNC unexpectedly needs SSH")
			}
			if _, err := desktop.List("/"); err == nil {
				t.Fatal("direct VNC exposed SFTP")
			}
			if attach {
				ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
				defer cancel()
				ws, _, err := websocket.Dial(ctx, connection.URL, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {"mygo://localhost"}}})
				if err != nil {
					t.Fatal(err)
				}
				defer ws.CloseNow()
				stream := websocket.NetConn(ctx, ws, websocket.MessageBinary)
				banner := make([]byte, 12)
				if _, err := io.ReadFull(stream, banner); err != nil || string(banner) != "RFB 003.008\n" {
					t.Fatalf("VNC banner: %q, %v", banner, err)
				}
				if _, err := stream.Write(banner); err != nil {
					t.Fatal(err)
				}
				if _, err := io.ReadFull(stream, banner); err != nil {
					t.Fatal(err)
				}
				p, err := desktop.Performance(connection.ID)
				if err != nil || p.RTTMillis != nil || p.ReceivedBytes < 12 || p.SentBytes < 12 {
					t.Fatalf("direct VNC metrics: %+v, %v", p, err)
				}
			}
			desktop.Disconnect()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("direct desktop stream leaked after disconnect")
			}
		})
	}
}
