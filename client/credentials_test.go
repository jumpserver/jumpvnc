package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func identityFixture(t *testing.T, filename string) ssh.Signer {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if filename != "" {
		block, err := ssh.MarshalPrivateKey(key, "fixture")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, pem.EncodeToMemory(block), 0600); err != nil {
			t.Fatal(err)
		}
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func TestServerIdentityConfirmation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	address := "desktop.example:5922"
	key := identityFixture(t, "").PublicKey()
	profile := Profile{}
	check := profile.hostKeyCallback(address, address)
	err := check(address, targetAddr(address), key)
	var challenge *HostKeyChallenge
	if !errors.As(err, &challenge) || challenge.Host != address || challenge.Fingerprint != ssh.FingerprintSHA256(key) {
		t.Fatalf("missing confirmation: %v", err)
	}
	filename := filepath.Join(dir, ".ssh", "known_hosts")
	if _, err := os.Stat(filename); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unconfirmed identity saved")
	}
	profile.TrustHostKey = "wrong fingerprint"
	if err := profile.hostKeyCallback(address, address)(address, targetAddr(address), key); err == nil {
		t.Fatal("accepted changed confirmation")
	}
	profile.TrustHostKey = challenge.Fingerprint
	if err := profile.hostKeyCallback(address, address)(address, targetAddr(address), key); err != nil {
		t.Fatal(err)
	}
	if err := check(address, targetAddr(address), key); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filename)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("identity permissions: %v, %v", info, err)
	}
	before, _ := os.ReadFile(filename)
	changed := identityFixture(t, "").PublicKey()
	profile.TrustHostKey = ssh.FingerprintSHA256(changed)
	if err := profile.hostKeyCallback(address, address)(address, targetAddr(address), changed); err == nil {
		t.Fatal("replaced existing server identity")
	}
	after, _ := os.ReadFile(filename)
	if string(before) != string(after) {
		t.Fatal("changed identity overwrote trust record")
	}
}

func TestAutomaticIdentityAndHostKeyHandshake(t *testing.T) {
	for _, source := range []string{"file", "agent", "empty-agent-with-file"} {
		t.Run(source, func(t *testing.T) { testAutomaticIdentity(t, source) })
	}
}

func testAutomaticIdentity(t *testing.T, source string) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("SSH_AUTH_SOCK", "")
	if source != "agent" {
		identityFixture(t, filepath.Join(dir, ".ssh", "id_ed25519"))
	}
	if source != "file" {
		socketDir, err := os.MkdirTemp("/tmp", "svnc-agent-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(socketDir) })
		listener, err := net.Listen("unix", filepath.Join(socketDir, "agent"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { listener.Close() })
		_, privateKey, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		keyring := agent.NewKeyring()
		if source == "agent" {
			if err := keyring.Add(agent.AddedKey{PrivateKey: privateKey}); err != nil {
				t.Fatal(err)
			}
		}
		go func() {
			for {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				go func() { defer conn.Close(); agent.ServeAgent(keyring, conn) }()
			}
		}()
		t.Setenv("SSH_AUTH_SOCK", listener.Addr().String())
	}
	host := identityFixture(t, "")
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 3 {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			cfg := &ssh.ServerConfig{PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) { return nil, nil }}
			cfg.AddHostKey(host)
			conn, _, _, err := ssh.NewServerConn(raw, cfg)
			if err == nil {
				conn.Wait()
				conn.Close()
			}
			raw.Close()
		}
	}()
	t.Cleanup(func() { listener.Close(); <-done })
	addr := listener.Addr().(*net.TCPAddr)
	profile := Profile{Host: "127.0.0.1", Port: addr.Port, Transport: "subsystem"}
	desktop := &Desktop{}
	connection, err := desktop.Connect(profile)
	if err != nil || connection.HostKey == nil || connection.URL != "" {
		t.Fatalf("missing identity challenge: %+v, %v", connection, err)
	}
	profile.TrustHostKey = connection.HostKey.Fingerprint
	client, err := profile.dial(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	client.Close()
	profile.TrustHostKey = ""
	client, err = profile.dial(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	client.Close()
}

func TestAutomaticJumpHostIdentities(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	first := Profile{Jump: "first"}
	second := Profile{Jump: "second"}
	address := "127.0.0.1:5922"
	if first.hostKeyAddress(address) == second.hostKeyAddress(address) {
		t.Fatal("different jump hosts shared a loopback identity")
	}
	if first.hostKeyAddress(address) == first.hostKeyAddress("127.0.0.1:5923") {
		t.Fatal("different ports shared an identity")
	}
	if (Profile{}).hostKeyAddress(address) != address {
		t.Fatal("changed direct server identity")
	}
	if (Profile{Jump: "first", HostKeyAlias: "existing"}).hostKeyAddress(address) != "existing:22" {
		t.Fatal("changed existing identity alias")
	}
	for _, profile := range []Profile{first, second} {
		key := identityFixture(t, "").PublicKey()
		keyAddress := profile.hostKeyAddress(address)
		var challenge *HostKeyChallenge
		if err := profile.hostKeyCallback(keyAddress, address)(address, targetAddr(address), key); !errors.As(err, &challenge) {
			t.Fatalf("missing jump server confirmation: %v", err)
		}
		profile.TrustHostKey = challenge.Fingerprint
		if err := profile.hostKeyCallback(keyAddress, address)(address, targetAddr(address), key); err != nil {
			t.Fatal(err)
		}
		profile.TrustHostKey = ""
		if err := profile.hostKeyCallback(keyAddress, address)(address, targetAddr(address), key); err != nil {
			t.Fatalf("saved jump server identity rejected: %v", err)
		}
	}
}

func TestStoredPasswordFollowsPinnedServerIdentity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".ssh", "sshvnc-g15")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"client", "known_hosts", "vnc-password.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	p := preconfiguredProfile()
	p.Host, p.Jump = "desktop.example", ""
	if password, err := p.password(); err != nil || password != "fixture" {
		t.Fatalf("direct access lost pinned credentials: %v", err)
	}
	p.HostKeyAlias = "different-server"
	if _, err := p.password(); err == nil {
		t.Fatal("released credentials for a different pinned server")
	}
	lib, err := newLibraryStore(t.TempDir()).read()
	if err != nil || len(lib.Servers) != 0 {
		t.Fatalf("new library seeded a loopback development connection: %v", err)
	}
}

func TestSSHPasswordAuthentication(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SSH_AUTH_SOCK", "")
	host := identityFixture(t, "")
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		raw, err := listener.Accept()
		if err != nil {
			return
		}
		defer raw.Close()
		config := &ssh.ServerConfig{PasswordCallback: func(meta ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if meta.User() == "entered" && string(password) == "fixture-password" {
				return nil, nil
			}
			return nil, errors.New("denied")
		}}
		config.AddHostKey(host)
		conn, _, _, err := ssh.NewServerConn(raw, config)
		if err == nil {
			conn.Wait()
			conn.Close()
		}
	}()
	addr := listener.Addr().(*net.TCPAddr)
	p := Profile{Transport: "forward", Host: "127.0.0.1", Port: addr.Port, VNCPort: 5900, User: "entered", TrustHostKey: ssh.FingerprintSHA256(host.PublicKey())}
	_, cleanup, err := p.authMethods()
	cleanup()
	var challenge *AuthChallenge
	if !errors.As(err, &challenge) {
		t.Fatal("missing SSH credentials did not request a password")
	}
	p.SSHPassword = "fixture-password"
	client, err := p.dial(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	client.Close()
	<-done
}
