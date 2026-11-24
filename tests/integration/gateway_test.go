package integration

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"jumpvnc/internal/config"
	"jumpvnc/internal/sshgateway"
)

func startGateway(t *testing.T, mode string, options ...func(*config.Config)) (*ssh.Client, string, string) {
	t.Helper()
	state := t.TempDir()
	root := filepath.Join(state, "exchange")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	host, clientKey := newKey(t), newKey(t)
	privateKey, err := ssh.MarshalPrivateKey(host, "integration host")
	if err != nil {
		t.Fatal(err)
	}
	hostKeyFile := filepath.Join(state, "host")
	if err := os.WriteFile(hostKeyFile, pem.EncodeToMemory(privateKey), 0600); err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(host)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(clientKey)
	if err != nil {
		t.Fatal(err)
	}
	authorizedKeys := filepath.Join(state, "authorized_keys")
	if err := os.WriteFile(authorizedKeys, ssh.MarshalAuthorizedKey(signer.PublicKey()), 0600); err != nil {
		t.Fatal(err)
	}

	backend := listen(t)
	backendDone := make(chan struct{})
	go func() {
		defer close(backendDone)
		var workers sync.WaitGroup
		defer workers.Wait()
		for {
			conn, err := backend.Accept()
			if err != nil {
				return
			}
			workers.Go(func() {
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				conn.Write([]byte("RFB 003.008\n"))
				io.Copy(conn, conn)
			})
		}
	}()
	t.Cleanup(func() {
		backend.Close()
		<-backendDone
	})

	cfg := config.Config{
		SSH: config.SSH{
			User: "desktop", HostKeyFile: hostKeyFile, AuthorizedKeysFile: authorizedKeys, AllowVNC: true,
			Limits: config.Limits{
				MaxConnections: 4, MaxChannelsPerConnection: 2, HandshakesPerMinutePerIP: 10,
				HandshakeTimeoutSeconds: 3, IdleTimeoutSeconds: 5,
			},
		},
		VNC:  config.VNC{Listen: backend.Addr().String()},
		SFTP: config.SFTP{Mode: mode, Root: root, MaxFileSizeMiB: 1, MaxHandles: 2},
	}
	for _, option := range options {
		option(&cfg)
	}
	gateway, err := sshgateway.New(cfg, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gateway.Close() })

	listener := listen(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- gateway.Serve(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("gateway did not shut down")
		}
	})

	clientCfg := &ssh.ClientConfig{
		User: "other-user", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.FixedHostKey(hostSigner.PublicKey()), Timeout: 3 * time.Second,
	}
	if client, err := ssh.Dial("tcp4", listener.Addr().String(), clientCfg); err == nil {
		client.Close()
		t.Fatal("accepted a different SSH username")
	}
	clientCfg.User = cfg.SSH.User
	client, err := ssh.Dial("tcp4", listener.Addr().String(), clientCfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	return client, root, cfg.VNC.Listen
}

func newKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func listen(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	return listener
}

func TestGatewayPolicies(t *testing.T) {
	client, _, target := startGateway(t, "read_write")
	_, port, err := net.SplitHostPort(target)
	if err != nil {
		t.Fatal(err)
	}
	for _, rejected := range []string{"localhost:" + port, "127.0.0.1:1", "127.0.0.2:" + port, "[::1]:" + port} {
		conn, err := client.Dial("tcp", rejected)
		var denied *ssh.OpenChannelError
		if !errors.As(err, &denied) || denied.Reason != ssh.Prohibited {
			if conn != nil {
				conn.Close()
			}
			t.Fatalf("Dial(%q) returned %v", rejected, err)
		}
	}
	if l, err := client.Listen("tcp", "127.0.0.1:0"); err == nil {
		l.Close()
		t.Fatal("remote forwarding was accepted")
	}

	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	for _, req := range []struct {
		kind    string
		payload []byte
	}{
		{"pty-req", nil}, {"shell", nil}, {"auth-agent-req@openssh.com", nil},
		{"exec", ssh.Marshal(struct{ Command string }{"true"})},
		{"subsystem", ssh.Marshal(struct{ Name string }{"other"})},
	} {
		if ok, err := session.SendRequest(req.kind, true, req.payload); ok || err != nil {
			t.Fatalf("%s reply = %v, %v", req.kind, ok, err)
		}
	}

	tunnel, err := client.Dial("tcp", target)
	if err != nil {
		t.Fatal(err)
	}
	defer tunnel.Close()
	banner := make([]byte, 12)
	if _, err := io.ReadFull(tunnel, banner); err != nil || string(banner) != "RFB 003.008\n" {
		t.Fatalf("read banner: %q, %v", banner, err)
	}
	if extra, err := client.NewSession(); err == nil {
		extra.Close()
		t.Fatal("connection channel limit was not enforced")
	}
}

func TestSFTPLocation(t *testing.T) {
	for _, request := range []string{"sftp-location@jumpvnc", "sftp-location@sshvnc"} {
		for _, mode := range []string{"read_write", "disabled"} {
			t.Run(request+"/"+mode, func(t *testing.T) {
				client, root, _ := startGateway(t, mode)
				ok, data, err := client.SendRequest(request, true, nil)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "disabled" {
					if ok || len(data) != 0 {
						t.Fatal("disclosed a disabled SFTP directory")
					}
					return
				}
				var location struct {
					Root string `json:"root"`
					Home bool   `json:"home"`
				}
				if !ok || json.Unmarshal(data, &location) != nil || location.Root != root || location.Home {
					t.Fatalf("unexpected location: %s", data)
				}
				if ok, _, err := client.SendRequest(request, true, []byte("invalid")); err != nil || ok {
					t.Fatalf("accepted malformed request: %v", err)
				}
			})
		}
	}
}

func TestSFTPIsolationAndSizeLimit(t *testing.T) {
	client, root, _ := startGateway(t, "read_write")
	files, err := sftp.NewClient(client)
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	outside := filepath.Join(filepath.Dir(root), "outside")
	if err := os.WriteFile(outside, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	for name, link := range map[string]func(string, string) error{"symlink": os.Symlink, "hardlink": os.Link} {
		if err := link(outside, filepath.Join(root, name)); err != nil {
			t.Logf("cannot create %s fixture: %v", name, err)
			continue
		}
		if f, err := files.Open("/" + name); err == nil {
			f.Close()
			t.Fatalf("%s open was accepted", name)
		}
		if f, err := files.Create("/" + name); err == nil {
			f.Close()
			t.Fatalf("%s truncation was accepted", name)
		}
	}
	for _, name := range []string{"/../outside", "/folder/../../outside", "/C:/outside", "/..\\outside", "/file:stream"} {
		if f, err := files.Open(name); err == nil {
			f.Close()
			t.Fatalf("unexpected access to %q", name)
		}
	}
	if err := os.Symlink(filepath.Dir(root), filepath.Join(root, "outside-dir")); err == nil {
		if f, err := files.Open("/outside-dir/outside"); err == nil {
			f.Close()
			t.Fatal("a symlink in the middle of a path escaped the exchange directory")
		}
	} else {
		t.Logf("cannot create directory symlink fixture: %v", err)
	}
	if err := files.Symlink("/file", "/newlink"); err == nil {
		t.Fatal("SFTP symlink creation was accepted")
	}
	f, err := files.Create("/可传输.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte("data"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte("invalid"), 1<<20); err == nil {
		t.Fatal("write beyond max file size succeeded")
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := files.PosixRename("/可传输.txt", "/done"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "done"))
	if err != nil || string(data) != "data" {
		t.Fatalf("exchange content: %q, %v", data, err)
	}
	data, err = os.ReadFile(outside)
	if err != nil || string(data) != "private" {
		t.Fatalf("outside file changed: %q, %v", data, err)
	}
}

func TestSFTPAccessModes(t *testing.T) {
	for _, mode := range []string{"read_only", "write_only", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			client, root, _ := startGateway(t, mode)
			if err := os.WriteFile(filepath.Join(root, "fixture"), []byte("data"), 0600); err != nil {
				t.Fatal(err)
			}
			files, err := sftp.NewClient(client)
			if mode == "disabled" {
				if err == nil {
					files.Close()
					t.Fatal("SFTP was enabled")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer files.Close()
			f, readErr := files.Open("/fixture")
			if f != nil {
				f.Close()
			}
			f, writeErr := files.OpenFile("/upload", os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
			if f != nil {
				f.Close()
			}
			_, listErr := files.ReadDir("/")
			if mode == "read_only" && (readErr != nil || writeErr == nil || listErr != nil) ||
				mode == "write_only" && (readErr == nil || writeErr != nil || listErr == nil) {
				t.Fatalf("%s read=%v write=%v list=%v", mode, readErr, writeErr, listErr)
			}
		})
	}
}

// The desktop subsystem, legacy Guacamole tunnel and SFTP share one SSH
// connection while desktop authorization remains independent of file access.
func TestVNCSubsystemWithExistingProtocols(t *testing.T) {
	for _, mode := range []string{"disabled", "read_write"} {
		t.Run(mode, func(t *testing.T) {
			client, _, target := startGateway(t, mode, func(c *config.Config) { c.SSH.Limits.MaxChannelsPerConnection = 4 })
			channel, requests, err := client.OpenChannel("session", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer channel.Close()
			go ssh.DiscardRequests(requests)
			if ok, err := channel.SendRequest("subsystem", true, ssh.Marshal(struct{ Name string }{"vnc"})); err != nil || !ok {
				t.Fatalf("VNC subsystem: %v, %v", ok, err)
			}
			checkDesktopStream(t, channel)
			if ok, err := channel.SendRequest("shell", true, nil); err != nil || ok {
				t.Fatalf("shell request on VNC channel: %v, %v", ok, err)
			}
			if mode != "disabled" {
				files, err := sftp.NewClient(client)
				if err != nil {
					t.Fatal(err)
				}
				defer files.Close()
				if _, err := files.ReadDir("/"); err != nil {
					t.Fatal(err)
				}
			} else if files, err := sftp.NewClient(client); err == nil {
				files.Close()
				t.Fatal("SFTP enabled by VNC subsystem")
			}
			tunnel, err := client.Dial("tcp", target)
			if err != nil {
				t.Fatal(err)
			}
			defer tunnel.Close()
			checkDesktopStream(t, tunnel)
		})
	}
}

func checkDesktopStream(t *testing.T, stream io.ReadWriter) {
	t.Helper()
	banner := make([]byte, 12)
	if _, err := io.ReadFull(stream, banner); err != nil || string(banner) != "RFB 003.008\n" {
		t.Fatalf("RFB banner = %q, %v", banner, err)
	}
	// Preserve arbitrary binary bytes, including RFB authentication messages.
	payload := []byte{0, 1, 2, 128, 255, 0}
	if _, err := stream.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(stream, got); err != nil || string(got) != string(payload) {
		t.Fatalf("RFB round trip = %v, %v", got, err)
	}
}

func TestVNCSubsystemDeniedWithoutDesktopPermission(t *testing.T) {
	client, _, target := startGateway(t, "read_write", func(c *config.Config) { c.SSH.AllowVNC = false })
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err := session.RequestSubsystem("vnc"); err == nil {
		t.Fatal("VNC subsystem bypassed desktop policy")
	}
	if conn, err := client.Dial("tcp", target); err == nil {
		conn.Close()
		t.Fatal("legacy forwarding bypassed desktop policy")
	}
	files, err := sftp.NewClient(client)
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
}

func TestVNCSubsystemRejectsUnavailableBackend(t *testing.T) {
	unavailable := listen(t)
	address := unavailable.Addr().String()
	unavailable.Close()
	client, _, _ := startGateway(t, "disabled", func(c *config.Config) { c.VNC.Listen = address })
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err := session.RequestSubsystem("vnc"); err == nil {
		t.Fatal("accepted unavailable desktop")
	}
}
