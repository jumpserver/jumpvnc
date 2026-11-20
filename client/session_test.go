package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Desktop access works independently of SFTP, over either transport.
func TestDesktopSessionWithoutSFTP(t *testing.T) {
	for _, transport := range []string{"subsystem", "forward", "", "subsystem_denied"} {
		t.Run(transport, func(t *testing.T) { testDesktopSession(t, transport) })
	}
}

func testDesktopSession(t *testing.T, transport string) {
	_, hostKey, _ := ed25519.GenerateKey(rand.Reader)
	host, err := ssh.NewSignerFromKey(hostKey)
	if err != nil {
		t.Fatal(err)
	}
	_, clientKey, _ := ed25519.GenerateKey(rand.Reader)
	key, err := ssh.MarshalPrivateKey(clientKey, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	identity := filepath.Join(dir, "identity")
	if err := os.WriteFile(identity, pem.EncodeToMemory(key), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	hosts := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(hosts, []byte(knownhosts.Line([]string{listener.Addr().String()}, host.PublicKey())+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	serverConfig := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, _ ssh.PublicKey) (*ssh.Permissions, error) { return nil, nil }}
	serverConfig.AddHostKey(host)
	var forwards, subsystems atomic.Int32
	var handlers sync.WaitGroup
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		raw, err := listener.Accept()
		if err != nil {
			return
		}
		conn, channels, requests, err := ssh.NewServerConn(raw, serverConfig)
		if err != nil {
			raw.Close()
			return
		}
		defer conn.Close()
		go ssh.DiscardRequests(requests)
		defer handlers.Wait()
		for channel := range channels {
			if channel.ChannelType() != "session" && channel.ChannelType() != "direct-tcpip" {
				channel.Reject(ssh.Prohibited, "fixture")
				continue
			}
			if channel.ChannelType() == "direct-tcpip" {
				forwards.Add(1)
			}
			stream, requests, err := channel.Accept()
			if err != nil {
				continue
			}
			handlers.Go(func() {
				defer stream.Close()
				if channel.ChannelType() == "session" {
					request, ok := <-requests
					if !ok {
						return
					}
					var subsystem struct{ Name string }
					allowed := request.Type == "subsystem" && ssh.Unmarshal(request.Payload, &subsystem) == nil && subsystem.Name == "vnc" && transport != "subsystem_denied"
					request.Reply(allowed, nil)
					if !allowed {
						return
					}
					subsystems.Add(1)
				}
				go ssh.DiscardRequests(requests)
				stream.Write([]byte("RFB 003.008\n"))
				io.Copy(stream, stream)
			})
		}
	}()
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	portNumber, _ := strconv.Atoi(port)
	profile := Profile{Host: "127.0.0.1", Port: portNumber, User: "desktop", Identity: identity, KnownHosts: hosts, Transport: transport, VNCPort: 5900}
	if transport == "subsystem_denied" {
		profile.Transport = "subsystem"
	}
	if profile.Transport == "subsystem" {
		profile.VNCPort = 0
	}
	s, url, err := openSession(profile)
	if transport == "subsystem_denied" {
		if err == nil {
			s.close()
			t.Fatal("rejected subsystem silently succeeded")
		}
		workers.Wait()
		if forwards.Load() != 0 {
			t.Fatal("subsystem rejection fell back to forwarding")
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	t.Cleanup(func() { s.close(); workers.Wait() })
	if s.files != nil || url == "" || s.ctx.Err() != nil {
		t.Fatalf("unexpected session: files=%v, url=%s", s.files, url)
	}
	d := &Desktop{current: s}
	if _, err := d.List("/"); err == nil {
		t.Error("file access should fail without SFTP")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {"mygo://localhost"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	stream := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	banner := make([]byte, 12)
	if _, err := io.ReadFull(stream, banner); err != nil || string(banner) != "RFB 003.008\n" {
		t.Fatalf("desktop bridge: %q, %v", banner, err)
	}
	payload := []byte{0, 255, 128, 42}
	stream.Write(payload)
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(stream, got); err != nil || string(got) != string(payload) {
		t.Fatalf("desktop data: %v, %v", got, err)
	}
	if profile.Transport == "subsystem" {
		if subsystems.Load() != 1 || forwards.Load() != 0 {
			t.Fatal("desktop did not use only the subsystem")
		}
	} else if forwards.Load() != 1 || subsystems.Load() != 0 {
		t.Fatal("legacy profile did not use forwarding")
	}
}

func TestFileSessionAfterDisconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := &Desktop{current: &session{ctx: ctx}}
	if _, err := d.fileSession(); err == nil {
		t.Fatal("disconnected file session accepted")
	}
}
