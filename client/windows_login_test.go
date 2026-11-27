package main

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

type loginRequestRecorder struct {
	ssh.Channel
	request func(string, []byte) (bool, error)
}

func (r loginRequestRecorder) SendRequest(name string, wantReply bool, payload []byte) (bool, error) {
	return r.request(name, payload)
}

func TestWindowsLoginNegotiation(t *testing.T) {
	for _, tc := range []struct {
		name                               string
		active, supported, ready, accepted bool
		want                               string
		requests                           int
		failAt                             int
	}{
		{name: "legacy", requests: 1},
		{name: "already unlocked", supported: true, active: true, want: "active", requests: 2},
		{name: "unavailable", supported: true, requests: 3},
		{name: "rejected", supported: true, ready: true, requests: 4},
		{name: "submitted", supported: true, ready: true, accepted: true, want: "submitted", requests: 4},
		{name: "connection lost at capability", failAt: 1, requests: 1},
		{name: "connection lost at account check", supported: true, failAt: 2, requests: 2},
		{name: "connection lost at readiness", supported: true, failAt: 3, requests: 3},
		{name: "connection lost waiting for submit reply", supported: true, ready: true, failAt: 4, want: "unconfirmed", requests: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const user = `.\操作员`
			const pass = "test-密碼"
			calls := 0
			channel := loginRequestRecorder{request: func(name string, body []byte) (bool, error) {
				calls++
				if name != "windows-login@jumpvnc" && bytes.Contains(body, []byte(pass)) {
					t.Fatal("password sent before capability and readiness checks")
				}
				if calls == tc.failAt {
					return false, io.EOF
				}
				switch name {
				case "windows-login-supported@jumpvnc":
					if calls != 1 || len(body) != 0 {
						t.Fatal("capability leaked metadata")
					}
					return tc.supported, nil
				case "windows-login-active@jumpvnc":
					var request struct{ Account string }
					if calls != 2 || ssh.Unmarshal(body, &request) != nil || request.Account != user {
						t.Fatal("bad account probe")
					}
					return tc.active, nil
				case "windows-login-ready@jumpvnc":
					if calls != 3 || len(body) != 0 {
						t.Fatal("bad readiness check")
					}
					return tc.ready, nil
				case "windows-login@jumpvnc":
					var request struct{ Account, Password string }
					if calls != 4 || ssh.Unmarshal(body, &request) != nil || request.Account != user || request.Password != pass {
						t.Fatal("wrong credential on submit")
					}
					return tc.accepted, nil
				default:
					t.Fatalf("unexpected request %s", name)
					return false, nil
				}
			}}
			secret := []byte(pass)
			s := &session{ctx: context.Background(), desktopChannel: channel, pendingLogin: &windowsLogin{account: user, password: secret, expiry: time.NewTimer(time.Minute)}}
			s.desktopAttached.Store(true)
			state, err := s.submitWindowsLogin()
			if state != tc.want || (err == nil) != (tc.want != "") || calls != tc.requests {
				t.Fatalf("state=%q err=%v requests=%d", state, err, calls)
			}
			if _, err := s.submitWindowsLogin(); err == nil || calls != tc.requests || !bytes.Equal(secret, make([]byte, len(secret))) {
				t.Fatal("replayed or retained a one-shot secret")
			}
		})
	}
}

func TestWindowsDesktopActiveAfterReconnect(t *testing.T) {
	const account = `.\列车员`
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s := &session{id: "new-window-session", ctx: ctx}
	d := &Desktop{current: s}
	if _, err := d.sessionByID("other-window-session"); err == nil {
		t.Fatal("another window's session ID was accepted")
	}
	if _, err := d.sessionByID(s.id); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Viewer{}).WindowsDesktopActive(context.Background(), s.id, account); err == nil {
		t.Fatal("desktop check without caller window succeeded")
	}
	if _, err := s.windowsDesktopActive(account); err == nil {
		t.Fatal("missing channel accepted")
	}
	calls := 0
	s.desktopChannel = loginRequestRecorder{request: func(name string, body []byte) (bool, error) {
		calls++
		var payload struct{ Account string }
		if name != "windows-login-active@jumpvnc" || ssh.Unmarshal(body, &payload) != nil || payload.Account != account ||
			!bytes.Equal(body, ssh.Marshal(struct{ Account string }{account})) {
			t.Fatalf("account-only status check sent unexpected data on %s", name)
		}
		if calls == 3 {
			return false, io.EOF
		}
		return calls == 1, nil
	}}
	if _, err := s.windowsDesktopActive(account); err == nil {
		t.Fatal("unattached desktop accepted")
	}
	s.desktopAttached.Store(true)
	for _, invalid := range []string{"", `other\account`} {
		if _, err := s.windowsDesktopActive(invalid); err == nil {
			t.Fatal("invalid account accepted")
		}
	}
	for _, want := range []bool{true, false} {
		if active, err := s.windowsDesktopActive(account); active != want || err != nil {
			t.Fatalf("active=%t err=%v want %t", active, err, want)
		}
	}
	if _, err := s.windowsDesktopActive(account); err == nil {
		t.Fatal("lost reply reported as a valid status")
	}
	cancel()
	if _, err := s.windowsDesktopActive(account); err == nil || calls != 3 {
		t.Fatalf("closed connection accepted, calls=%d", calls)
	}
}

func TestWindowsLoginRejectsUnencryptedAndTunnelProfiles(t *testing.T) {
	for _, transport := range []string{"vnc", "forward", ""} {
		p := Profile{Host: "localhost", Port: 22, VNCPort: 5900, Transport: transport, WindowsAccount: "fixture", WindowsPassword: "fixture"}
		if err := p.validateTransport(); err == nil {
			t.Fatalf("Windows credentials accepted on transport %q", transport)
		}
	}
}
