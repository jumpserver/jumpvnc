package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	sshconfig "github.com/kevinburke/ssh_config"
	"golang.org/x/crypto/ssh"
)

func TestJumpConfig(t *testing.T) {
	setTestHome(t, t.TempDir())
	config, err := sshconfig.Decode(strings.NewReader(`
Host gateway
  HostName gateway.example
  User remote
  Port 2222
  IdentityFile ~/.ssh/%r-%n
  UserKnownHostsFile ~/.ssh/jump_hosts
Host internal
  ProxyJump gateway
Host command
  ProxyCommand ssh gateway -W %h:%p
Host loop
  ProxyJump loop
`))
	if err != nil {
		t.Fatal(err)
	}
	p, _, err := jumpProfile("override@gateway:2200", config)
	if err != nil || p.Host != "gateway.example" || p.User != "override" || p.Port != 2200 || p.Identity != "~/.ssh/override-gateway" || p.KnownHosts != "~/.ssh/jump_hosts" {
		t.Fatalf("config: %+v, %v", p, err)
	}
	route, err := jumpRoute("internal", config, make(map[string]bool), 0)
	if err != nil || len(route) != 2 || route[0].Host != "gateway.example" || route[1].Host != "internal" {
		t.Fatalf("route: %+v, %v", route, err)
	}
	for _, spec := range []string{"command", "loop", "user:password@gateway", "gateway:0", "gateway:65536", "gateway/path", "-bad", "a b"} {
		if _, err := jumpRoute(spec, config, make(map[string]bool), 0); err == nil {
			t.Errorf("accepted invalid jump: %s", spec)
		}
	}
}

func startJumpTestServer(t *testing.T, username string, identity ssh.PublicKey, forward string) (string, ssh.PublicKey) {
	t.Helper()
	host := identityFixture(t, "")
	config := &ssh.ServerConfig{PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if meta.User() != username || !bytes.Equal(key.Marshal(), identity.Marshal()) {
			return nil, errors.New("wrong identity")
		}
		return nil, nil
	}}
	config.AddHostKey(host)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	workers.Go(func() {
		for {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			workers.Go(func() {
				defer raw.Close()
				stop := context.AfterFunc(t.Context(), func() { raw.Close() })
				defer stop()
				conn, channels, requests, err := ssh.NewServerConn(raw, config)
				if err != nil {
					return
				}
				defer conn.Close()
				go ssh.DiscardRequests(requests)
				for request := range channels {
					var payload struct {
						Host       string
						Port       uint32
						OriginHost string
						OriginPort uint32
					}
					if request.ChannelType() != "direct-tcpip" || ssh.Unmarshal(request.ExtraData(), &payload) != nil || net.JoinHostPort(payload.Host, strconv.Itoa(int(payload.Port))) != forward {
						request.Reject(ssh.Prohibited, "not allowed")
						continue
					}
					target, err := net.Dial("tcp", forward)
					if err != nil {
						request.Reject(ssh.ConnectionFailed, err.Error())
						continue
					}
					channel, requests, err := request.Accept()
					if err != nil {
						target.Close()
						continue
					}
					go ssh.DiscardRequests(requests)
					workers.Go(func() {
						defer target.Close()
						defer channel.Close()
						io.Copy(target, channel)
					})
					workers.Go(func() {
						defer target.Close()
						defer channel.Close()
						io.Copy(channel, target)
					})
				}
			})
		}
	})
	t.Cleanup(func() { listener.Close(); workers.Wait() })
	return listener.Addr().String(), host.PublicKey()
}

func TestGoJumpWithoutLocalSSH(t *testing.T) {
	for _, multiple := range []bool{false, true} {
		t.Run(fmt.Sprintf("multiple=%t", multiple), func(t *testing.T) { testGoJumpWithoutLocalSSH(t, multiple) })
	}
}

func testGoJumpWithoutLocalSSH(t *testing.T, multiple bool) {
	home := t.TempDir()
	setTestHome(t, home)
	t.Setenv("SSH_AUTH_SOCK", "")
	// No executable, including ssh, is available to this connection.
	t.Setenv("PATH", t.TempDir())
	jumpIdentity := filepath.Join(home, ".ssh", "gateway_key")
	jumpKey := identityFixture(t, jumpIdentity)
	identity := filepath.Join(home, ".ssh", "desktop_key")
	key := identityFixture(t, identity)
	target, targetKey := startJumpTestServer(t, "desktop", key.PublicKey(), "")
	jump, jumpHostKey := startJumpTestServer(t, "gateway", jumpKey.PublicKey(), target)
	host, port, _ := net.SplitHostPort(jump)
	config := fmt.Sprintf("Host test-gateway\n HostName %s\n Port %s\n User gateway\n IdentityFile ~/.ssh/gateway_key\n", host, port)
	type confirmation struct {
		host string
		key  ssh.PublicKey
	}
	expected := []confirmation{{jump, jumpHostKey}, {"test-gateway → " + target, targetKey}}
	if multiple {
		outer, outerKey := startJumpTestServer(t, "gateway", jumpKey.PublicKey(), jump)
		host, port, _ := net.SplitHostPort(outer)
		config += fmt.Sprintf(" ProxyJump outer\nHost outer\n HostName %s\n Port %s\n User gateway\n IdentityFile ~/.ssh/gateway_key\n", host, port)
		expected = append([]confirmation{{outer, outerKey}}, expected...)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	host, port, _ = net.SplitHostPort(target)
	portNumber, _ := strconv.Atoi(port)
	p := Profile{Host: host, Port: portNumber, User: "desktop", Transport: "subsystem", Jump: "test-gateway", Identity: identity}
	for _, expected := range expected {
		client, err := p.dial(t.Context())
		if client != nil {
			client.Close()
		}
		var challenge *HostKeyChallenge
		if !errors.As(err, &challenge) || challenge.Host != expected.host || challenge.Fingerprint != ssh.FingerprintSHA256(expected.key) {
			t.Fatalf("expected confirmation for %s: %v", expected.host, err)
		}
		p.TrustHost, p.TrustHostKey = challenge.Host, challenge.Fingerprint
	}
	client, err := p.dial(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	client.Close()
	p.TrustHost, p.TrustHostKey = "", ""
	client, err = p.dial(t.Context())
	if err != nil {
		t.Fatalf("saved host identities: %v", err)
	}
	client.Close()
	// Cancellation must close an in-progress route instead of leaking a hop.
	ctx, cancel := context.WithTimeout(t.Context(), time.Nanosecond)
	defer cancel()
	if client, err := p.dial(ctx); err == nil {
		client.Close()
		t.Fatal("cancelled route succeeded")
	}
}
