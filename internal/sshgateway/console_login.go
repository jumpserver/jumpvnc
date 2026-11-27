package sshgateway

import (
	"bytes"
	"context"
	"encoding/binary"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"

	"jumpvnc/internal/vnc"
)

// A console authenticated by a Viewer must not be shared underneath that Viewer.
type desktopAccess struct {
	mu        sync.Mutex
	count     int
	exclusive bool
	lastTry   time.Time
}

func (a *desktopAccess) enter() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.exclusive {
		return false
	}
	a.count++
	return true
}

func (a *desktopAccess) leave(exclusive bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.count--
	if exclusive {
		a.exclusive = false
	}
}

func (a *desktopAccess) claim() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.exclusive || a.count != 1 || time.Since(a.lastTry) < 5*time.Second {
		return false
	}
	a.exclusive = true
	a.lastTry = time.Now()
	return true
}

// Observe only the trusted backend's prefix: RFB 3.8, one mandatory VNCAuth
// type, its 16-byte challenge and the successful SecurityResult. Other RFB
// variants can still display a desktop but never authorize a Windows login.
type rfbAuthProof struct {
	prefix [34]byte
	n      int
	ok     atomic.Bool
}

func (p *rfbAuthProof) Write(data []byte) (int, error) {
	if p.n < len(p.prefix) {
		p.n += copy(p.prefix[p.n:], data)
		if p.n == len(p.prefix) {
			p.ok.Store(bytes.Equal(p.prefix[:14], []byte("RFB 003.008\n\x01\x02")) && binary.BigEndian.Uint32(p.prefix[30:]) == 0)
			clear(p.prefix[:])
		}
	}
	return len(data), nil
}

type desktopLogin struct {
	provider  vnc.ConsoleLogin
	proof     rfbAuthProof
	account   string
	prepared  bool
	used      bool
	exclusive bool
}

func (l *desktopLogin) handle(ctx context.Context, s *Server, req *ssh.Request, log *slog.Logger) {
	defer clear(req.Payload)
	ok := false
	defer func() {
		if req.WantReply {
			req.Reply(ok, nil)
		}
	}()
	if !req.WantReply || s.cfg.VNC.ViewOnly || s.cfg.VNC.Compatibility.Enabled ||
		s.cfg.VNC.Engine != "libvncserver" || !l.proof.ok.Load() || l.used || ctx.Err() != nil || !l.provider.Available() {
		return
	}
	switch req.Type {
	case "windows-login-supported@jumpvnc":
		ok = len(req.Payload) == 0
	case "windows-login-active@jumpvnc":
		account, rest, valid := loginField(req.Payload)
		l.account, l.prepared = "", false
		if valid && len(rest) == 0 && validAccount(account) {
			l.account = string(account)
			ok = l.provider.Active(l.account)
		}
	case "windows-login-ready@jumpvnc":
		l.prepared = len(req.Payload) == 0 && l.provider.Ready(l.account)
		ok = l.prepared
	case "windows-managed-login@jumpvnc":
		if s.Managed == nil || !s.Managed.Valid() || len(req.Payload) != 0 || !l.prepared || l.account != ManagedAccount || !l.provider.Ready(l.account) || !s.desktops.claim() {
			return
		}
		l.exclusive, l.used = true, true
		password, err := s.Managed.Password()
		if err != nil {
			log.Warn("windows_login.managed_credential_unavailable")
			return
		}
		defer clear(password)
		ok = l.submit(ctx, l.account, password, log)
	case "windows-login@jumpvnc":
		if !l.prepared {
			return
		}
		l.used = true
		account, rest, valid := loginField(req.Payload)
		if !valid || !validAccount(account) || (l.account != "" && string(account) != l.account) {
			return
		}
		password, rest, valid := loginField(rest)
		if !valid || len(rest) != 0 || !l.provider.Ready(string(account)) || !s.desktops.claim() {
			return
		}
		l.exclusive = true
		ok = l.submit(ctx, string(account), password, log)
	}
}

func (l *desktopLogin) submit(ctx context.Context, account string, password []byte, log *slog.Logger) bool {
	loginCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := l.provider.Submit(loginCtx, account, password); err != nil {
		log.Warn("windows_login.failed", "error", err)
		return false
	}
	log.Info("windows_login.submitted")
	return true
}

func loginField(b []byte) ([]byte, []byte, bool) {
	if len(b) < 4 {
		return nil, nil, false
	}
	n := binary.BigEndian.Uint32(b)
	if n == 0 || n > 1024 || n > uint32(len(b)-4) {
		return nil, nil, false
	}
	value := b[4 : 4+n]
	return value, b[4+n:], utf8.Valid(value) && bytes.IndexByte(value, 0) < 0
}

func validAccount(account []byte) bool {
	name := bytes.TrimPrefix(account, []byte(`.\`))
	return len(name) > 0 && !bytes.ContainsAny(name, `\/@`) && bytes.IndexFunc(name, unicode.IsControl) < 0
}
