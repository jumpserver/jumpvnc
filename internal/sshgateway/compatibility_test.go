package sshgateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"sshvnc/internal/config"
)

func TestVNCCompatibilityLifecycle(t *testing.T) {
	backend, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	backendDone := make(chan struct{})
	go func() {
		defer close(backendDone)
		conn, err := backend.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		conn.Write([]byte("RFB 003.008\n"))
		io.Copy(conn, conn)
	}()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	s := &Server{cfg: config.Config{
		SSH: config.SSH{AllowVNC: true, Limits: config.Limits{MaxConnections: 1, HandshakesPerMinutePerIP: 10, IdleTimeoutSeconds: 1}},
		VNC: config.VNC{Listen: backend.Addr().String(), Compatibility: config.VNCCompatibility{Enabled: true}},
	}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.ServeVNC(ctx, listener) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("compatibility listener did not stop")
		}
		backend.Close()
		<-backendDone
	}()
	viewer, err := net.DialTimeout("tcp4", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer viewer.Close()
	viewer.SetDeadline(time.Now().Add(5 * time.Second))
	banner := make([]byte, 12)
	if _, err := io.ReadFull(viewer, banner); err != nil || string(banner) != "RFB 003.008\n" {
		t.Fatalf("banner: %q, %v", banner, err)
	}
	payload := []byte{1, 2, 0, 255, 128}
	viewer.Write(payload)
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(viewer, got); err != nil || string(got) != string(payload) {
		t.Fatalf("RFB authentication bytes changed: %v, %v", got, err)
	}
	extra, err := net.DialTimeout("tcp4", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer extra.Close()
	extra.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := extra.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("excess connection was not closed: %v", err)
	}
	cancel()
	if _, err := viewer.Read(make([]byte, 1)); err == nil {
		t.Fatal("active viewer survived shutdown")
	}
}

func TestVNCCompatibilityDisabled(t *testing.T) {
	for _, allowVNC := range []bool{false, true} {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		s := &Server{cfg: config.Config{SSH: config.SSH{AllowVNC: allowVNC}}}
		if err := s.ServeVNC(context.Background(), listener); err == nil {
			t.Fatal("disabled compatibility endpoint accepted")
		}
		if conn, err := net.DialTimeout("tcp4", listener.Addr().String(), time.Second); err == nil {
			conn.Close()
			t.Fatal("disabled compatibility listener remains open")
		}
	}
}
