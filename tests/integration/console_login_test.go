package integration

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"jumpvnc/internal/config"
	"jumpvnc/internal/sshgateway"
	"jumpvnc/internal/vnc"
)

const loginAccount = `.\Operator`
const samplePassword = "example-only"

type consoleLoginRecorder struct {
	calls atomic.Int32
}

func (*consoleLoginRecorder) Available() bool            { return true }
func (*consoleLoginRecorder) Active(account string) bool { return account == "ActiveUser" }
func (*consoleLoginRecorder) Ready(account string) bool {
	return account == "" || account == loginAccount
}
func (r *consoleLoginRecorder) Submit(_ context.Context, account string, password []byte) error {
	r.calls.Add(1)
	if account != loginAccount || !bytes.Equal(password, []byte(samplePassword)) {
		return io.ErrUnexpectedEOF
	}
	return nil
}

// Exchange just the RFB authentication messages the gateway observes, with a
// fixed test response and an explicit server-side authentication outcome.
func rfbLoginFixture(t *testing.T, mode string) string {
	t.Helper()
	backend := listen(t)
	var workers sync.WaitGroup
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer workers.Wait()
		for {
			conn, err := backend.Accept()
			if err != nil {
				return
			}
			workers.Go(func() {
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				version := "RFB 003.008\n"
				if mode == "rfb33" {
					version = "RFB 003.003\n"
				}
				if _, err := io.WriteString(conn, version); err != nil {
					return
				}
				var versionReply [12]byte
				if _, err := io.ReadFull(conn, versionReply[:]); err != nil || string(versionReply[:]) != version {
					return
				}
				if mode == "rfb33" {
					conn.Write([]byte{0, 0, 0, 2})
				} else {
					security := byte(2)
					if mode == "none" {
						security = 1
					}
					conn.Write([]byte{1, security})
					var choice [1]byte
					if _, err := io.ReadFull(conn, choice[:]); err != nil || choice[0] != security {
						return
					}
				}
				if mode != "none" {
					conn.Write(bytes.Repeat([]byte{3}, 16))
					var response [16]byte
					if _, err := io.ReadFull(conn, response[:]); err != nil {
						return
					}
					result := uint32(1)
					if bytes.Equal(response[:], bytes.Repeat([]byte{5}, 16)) {
						result = 0
					}
					binary.Write(conn, binary.BigEndian, result)
				} else {
					// Long enough for the gate to inspect the complete prefix,
					// including after the security result for the None type.
					conn.Write(make([]byte, 36))
				}
				io.Copy(io.Discard, conn)
			})
		}
	}()
	t.Cleanup(func() { backend.Close(); <-done })
	return backend.Addr().String()
}

func openDesktopChannel(t *testing.T, client *ssh.Client) ssh.Channel {
	t.Helper()
	ch, requests, err := client.OpenChannel("session", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ch.Close() })
	go ssh.DiscardRequests(requests)
	if ok, err := ch.SendRequest("subsystem", true, ssh.Marshal(struct{ Name string }{"vnc"})); err != nil || !ok {
		t.Fatalf("could not start VNC: %t, %v", ok, err)
	}
	return ch
}

func expectLoginRequest(t *testing.T, ch ssh.Channel, name string, payload []byte, want bool) {
	t.Helper()
	ok, err := ch.SendRequest("windows-login"+name+"@jumpvnc", true, payload)
	if err != nil || ok != want {
		t.Fatalf("login%s response=%t, want %t, err=%v", name, ok, want, err)
	}
}

func authenticateFixture(t *testing.T, ch ssh.Channel, mode string) {
	t.Helper()
	read := func(n int) []byte {
		t.Helper()
		buf := make([]byte, n)
		if _, err := io.ReadFull(ch, buf); err != nil {
			t.Fatal(err)
		}
		return buf
	}
	ch.Write(read(12))
	if mode == "rfb33" {
		read(4)
	} else {
		ch.Write(read(2)[1:])
	}
	if mode == "none" {
		read(36)
		return
	}
	read(16)
	value := byte(5)
	if mode == "bad-password" {
		value = 0
	}
	ch.Write(bytes.Repeat([]byte{value}, 16))
	code := binary.BigEndian.Uint32(read(4))
	if (code != 0) != (mode == "bad-password") {
		t.Fatal("unexpected fixture authentication result", code)
	}
}

func dialFixture(address string, login vnc.ConsoleLogin) func(context.Context) (vnc.Desktop, error) {
	return func(ctx context.Context) (vnc.Desktop, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp4", address)
		if err != nil {
			return vnc.Desktop{}, err
		}
		return vnc.Desktop{TCPConn: conn.(*net.TCPConn), Login: login}, nil
	}
}

func TestConsoleWorkerRestartKeepsSSHAndSFTP(t *testing.T) {
	address := rfbLoginFixture(t, "exclusive")
	workers := make(chan vnc.Backend, 1)
	manager := vnc.SuperviseConsole(t.Context(), func(ctx context.Context) (vnc.Backend, error) {
		select {
		case worker := <-workers:
			return worker, nil
		case <-ctx.Done():
			return vnc.Backend{}, ctx.Err()
		}
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(manager.Stop)
	client, _, _ := startGatewayWithSetup(t, "read_write", func(s *sshgateway.Server) {
		s.SetVNCDialer(manager.Dial)
	}, func(c *config.Config) { c.VNC.Engine = "libvncserver" })
	files, err := sftp.NewClient(client)
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	checkFiles := func() {
		t.Helper()
		if _, err := files.ReadDir("/"); err != nil {
			t.Fatal(err)
		}
	}
	checkFiles()
	firstExit := make(chan error, 1)
	workers <- vnc.Backend{Dial: dialFixture(address, &consoleLoginRecorder{}), Exited: firstExit, Stop: func() {}}
	first := openDesktopChannel(t, client)
	authenticateFixture(t, first, "exclusive")
	expectLoginRequest(t, first, "-supported", nil, true)
	firstExit <- nil
	closed := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, first)
		closed <- err
	}()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("retired desktop channel is still open")
	}
	checkFiles()
	workers <- vnc.Backend{Dial: dialFixture(address, nil), Stop: func() {}}
	second := openDesktopChannel(t, client)
	authenticateFixture(t, second, "exclusive")
	expectLoginRequest(t, second, "-supported", nil, false)
	checkFiles()
}

func TestConsoleLoginChannelPolicies(t *testing.T) {
	payload := ssh.Marshal(struct{ Account, Password string }{loginAccount, samplePassword})
	for _, mode := range []string{"exclusive", "shared", "view-only", "no-provider", "bad-password", "none", "rfb33", "unprepared", "invalid-account", "changed-account"} {
		t.Run(mode, func(t *testing.T) {
			address := rfbLoginFixture(t, mode)
			recorder := &consoleLoginRecorder{}
			client, _, _ := startGatewayWithSetup(t, "disabled", func(s *sshgateway.Server) {
				if mode != "no-provider" {
					s.SetVNCDialer(dialFixture(address, recorder))
				}
			}, func(c *config.Config) {
				c.VNC.Listen, c.VNC.Engine = address, "libvncserver"
				c.VNC.ViewOnly = mode == "view-only"
			})
			ch := openDesktopChannel(t, client)
			expectLoginRequest(t, ch, "-supported", nil, false)
			authenticateFixture(t, ch, mode)
			switch mode {
			case "view-only", "no-provider", "bad-password", "none", "rfb33":
				expectLoginRequest(t, ch, "-supported", nil, false)
				expectLoginRequest(t, ch, "-ready", nil, false)
				expectLoginRequest(t, ch, "", payload, false)
			case "unprepared":
				expectLoginRequest(t, ch, "", payload, false)
			case "invalid-account":
				expectLoginRequest(t, ch, "-ready", nil, true)
				invalid := ssh.Marshal(struct{ Account, Password string }{`OTHER\Operator`, samplePassword})
				expectLoginRequest(t, ch, "", invalid, false)
			case "changed-account":
				expectLoginRequest(t, ch, "-active", ssh.Marshal(struct{ Account string }{loginAccount}), false)
				expectLoginRequest(t, ch, "-ready", nil, true)
				expectLoginRequest(t, ch, "", ssh.Marshal(struct{ Account, Password string }{"OtherUser", samplePassword}), false)
			case "shared":
				other, err := client.Dial("tcp", address)
				if err != nil {
					t.Fatal(err)
				}
				defer other.Close()
				expectLoginRequest(t, ch, "-ready", nil, true)
				expectLoginRequest(t, ch, "", payload, false)
			case "exclusive":
				expectLoginRequest(t, ch, "-supported", nil, true)
				expectLoginRequest(t, ch, "-active", ssh.Marshal(struct{ Account string }{"ActiveUser"}), true)
				expectLoginRequest(t, ch, "-ready", nil, false)
				expectLoginRequest(t, ch, "-active", ssh.Marshal(struct{ Account string }{loginAccount}), false)
				expectLoginRequest(t, ch, "-ready", nil, true)
				expectLoginRequest(t, ch, "", payload, true)
				expectLoginRequest(t, ch, "", payload, false)
				if other, err := client.Dial("tcp", address); err == nil {
					other.Close()
					t.Fatal("another desktop was admitted during an exclusive login session")
				}
			}
			want := int32(0)
			if mode == "exclusive" {
				want = 1
			}
			if calls := recorder.calls.Load(); calls != want {
				t.Fatalf("Windows credential submissions = %d, want %d", calls, want)
			}
		})
	}
}

type managedRecorder struct{ consoleLoginRecorder }

func (*managedRecorder) Ready(account string) bool { return account == sshgateway.ManagedAccount }
func (r *managedRecorder) Submit(_ context.Context, account string, password []byte) error {
	if account != sshgateway.ManagedAccount || string(password) != samplePassword {
		return io.ErrUnexpectedEOF
	}
	r.calls.Add(1)
	return nil
}

func TestManagedAccountAndLocalDisconnect(t *testing.T) {
	address := rfbLoginFixture(t, "exclusive")
	login := &managedRecorder{}
	var gateway *sshgateway.Server
	var key string
	client, _, _ := startGatewayWithSetup(t, "read_write", func(s *sshgateway.Server) {
		gateway = s
		s.SetVNCDialer(dialFixture(address, login))
		s.Managed = &sshgateway.ManagedLogin{Fingerprint: key, Valid: func() bool { return true }, Password: func() ([]byte, error) { return []byte(samplePassword), nil }}
	}, func(cfg *config.Config) {
		cfg.VNC.Engine = "libvncserver"
		cfg.SSH.Limits.MaxChannelsPerConnection = 6
		data, err := os.ReadFile(cfg.SSH.AuthorizedKeysFile)
		if err != nil {
			t.Fatal(err)
		}
		pub, _, _, _, err := ssh.ParseAuthorizedKey(data)
		if err != nil {
			t.Fatal(err)
		}
		key = ssh.FingerprintSHA256(pub)
	})
	files, err := sftp.NewClient(client)
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	if len(gateway.Connections.List()) != 0 {
		t.Fatal("SFTP counted as remote control")
	}
	ch := openDesktopChannel(t, client)
	managed := func(want bool, payload []byte) {
		t.Helper()
		ok, err := ch.SendRequest("windows-managed-login@jumpvnc", true, payload)
		if err != nil || ok != want {
			t.Fatalf("managed reply %v: %v", ok, err)
		}
	}
	managed(false, nil)
	authenticateFixture(t, ch, "exclusive")
	other := openDesktopChannel(t, client)
	authenticateFixture(t, other, "exclusive")
	items := gateway.Connections.List()
	if len(items) != 2 || !items[0].Authenticated || !items[1].Authenticated || items[1].User != "desktop" {
		t.Fatalf("desktop list: %+v", items)
	}
	gateway.Connections.Disconnect([]string{items[1].ID})
	awaitClosed := func(stream ssh.Channel) {
		t.Helper()
		done := make(chan error, 1)
		go func() { var b [1]byte; _, err := stream.Read(b[:]); done <- err }()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("still receiving data")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("remote connection stayed open")
		}
	}
	awaitClosed(other)
	other.Close()
	deadline := time.Now().Add(time.Second)
	for len(gateway.Connections.List()) != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	expectLoginRequest(t, ch, "-active", ssh.Marshal(struct{ Account string }{"Administrator"}), false)
	expectLoginRequest(t, ch, "-ready", nil, false)
	expectLoginRequest(t, ch, "-active", ssh.Marshal(struct{ Account string }{sshgateway.ManagedAccount}), false)
	expectLoginRequest(t, ch, "-ready", nil, true)
	managed(false, []byte("client-provided"))
	managed(true, nil)
	managed(false, nil)
	if login.calls.Load() != 1 {
		t.Fatal("unexpected Windows submit count")
	}
	gateway.Connections.Disconnect([]string{items[0].ID, items[1].ID})
	awaitClosed(ch)
	if _, err := files.ReadDir("/"); err != nil {
		t.Fatalf("disconnect interrupted file service: %v", err)
	}
}
