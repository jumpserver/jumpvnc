package integration

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"jumpvnc/internal/config"
	"jumpvnc/internal/sshgateway"
)

const loginAccount = `.\Operator`
const samplePassword = "example-only"

type consoleLoginRecorder struct {
	calls atomic.Int32
}

func (*consoleLoginRecorder) Available() bool            { return true }
func (*consoleLoginRecorder) Active(account string) bool { return account == "ActiveUser" }
func (*consoleLoginRecorder) Ready() bool                { return true }
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

func TestConsoleLoginChannelPolicies(t *testing.T) {
	payload := ssh.Marshal(struct{ Account, Password string }{loginAccount, samplePassword})
	for _, mode := range []string{"exclusive", "shared", "view-only", "no-provider", "bad-password", "none", "rfb33", "unprepared", "invalid-account"} {
		t.Run(mode, func(t *testing.T) {
			address := rfbLoginFixture(t, mode)
			recorder := &consoleLoginRecorder{}
			client, _, _ := startGatewayWithSetup(t, "disabled", func(s *sshgateway.Server) {
				if mode != "no-provider" {
					s.SetConsoleLogin(recorder)
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
