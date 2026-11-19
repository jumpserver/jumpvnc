package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// An Agent may grant desktop forwarding while rejecting the SFTP subsystem.
func TestDesktopSessionWithoutSFTP(t *testing.T) {
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
		for channel := range channels {
			if channel.ChannelType() != "session" {
				channel.Reject(ssh.Prohibited, "fixture")
				continue
			}
			stream, requests, err := channel.Accept()
			if err != nil {
				continue
			}
			for request := range requests {
				request.Reply(false, nil)
				stream.Close()
				break
			}
		}
	}()
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	portNumber, _ := strconv.Atoi(port)
	s, url, err := openSession(Profile{Host: "127.0.0.1", Port: portNumber, User: "desktop", Identity: identity, KnownHosts: hosts, VNCPort: 5900})
	if err != nil {
		t.Fatal(err)
	}
	if s.files != nil || url == "" || s.ctx.Err() != nil {
		t.Fatalf("unexpected session: files=%v, url=%s", s.files, url)
	}
	d := &Desktop{current: s}
	if _, err := d.List("/"); err == nil {
		t.Error("file access should fail without SFTP")
	}
	s.close()
	workers.Wait()
}

func TestFileSessionAfterDisconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := &Desktop{current: &session{ctx: ctx}}
	if _, err := d.fileSession(); err == nil {
		t.Fatal("disconnected file session accepted")
	}
}
