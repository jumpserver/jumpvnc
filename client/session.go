package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type session struct {
	ssh     *ssh.Client
	files   *sftp.Client
	bridge  *http.Server
	ctx     context.Context
	cancel  context.CancelFunc
	closing sync.Once
	busy    atomic.Bool
}

func openSession(p Profile) (*session, string, error) {
	ctx, cancel := context.WithCancel(context.Background())
	client, err := p.dial(ctx)
	if err != nil {
		cancel()
		return nil, "", err
	}
	s := &session{ctx: ctx, cancel: cancel, ssh: client}
	timer := time.AfterFunc(sshTimeout, func() { client.Close() })
	s.files, err = sftp.NewClient(client)
	timer.Stop()
	if err != nil {
		s.close()
		return nil, "", fmt.Errorf("打开 SFTP: %w", err)
	}
	url, err := s.startBridge(p.VNCPort)
	if err != nil {
		s.close()
		return nil, "", err
	}
	go func() {
		client.Wait()
		s.close()
	}()
	go s.keepAlive()
	return s, url, nil
}

func (s *session) close() {
	s.closing.Do(func() {
		s.cancel()
		s.ssh.Close()
		if s.files != nil {
			s.files.Close()
		}
		if s.bridge != nil {
			s.bridge.Close()
		}
	})
}

func (s *session) keepAlive() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			timer := time.AfterFunc(sshTimeout, func() { s.close() })
			_, _, err := s.ssh.SendRequest("keepalive@openssh.com", true, nil)
			timer.Stop()
			if err != nil {
				s.close()
				return
			}
		}
	}
}

func (s *session) startBridge(port int) (string, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("打开本机 VNC 桥接: %w", err)
	}
	token := rand.Text()
	address := listener.Addr().String()
	target := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	var attached atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("GET /rfb/"+token, func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		// WKWebView can serialize its bundled custom-scheme origin as null.
		if r.Host != address || (origin != "mygo://localhost" && origin != "null") {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if !attached.CompareAndSwap(false, true) {
			http.Error(w, "VNC session already in use", http.StatusConflict)
			return
		}
		s.serveVNC(w, r, target)
	})
	s.bridge = &http.Server{Handler: mux, ReadHeaderTimeout: 4 * time.Second}
	go func() { s.bridge.Serve(listener) }()
	return "ws://" + address + "/rfb/" + token, nil
}

func (s *session) serveVNC(w http.ResponseWriter, r *http.Request, target string) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // Origin and session token were checked before upgrading.
	})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	vnc, err := s.ssh.DialContext(ctx, "tcp", target)
	if err != nil {
		conn.Close(websocket.StatusPolicyViolation, "VNC channel unavailable")
		return
	}
	defer vnc.Close()
	stop := context.AfterFunc(ctx, func() { vnc.Close() })
	defer stop()
	ws := websocket.NetConn(ctx, conn, websocket.MessageBinary)
	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(vnc, ws)
		done <- err
		vnc.Close()
	}()
	_, err = io.Copy(ws, vnc)
	if err == nil || errors.Is(err, net.ErrClosed) {
		conn.Close(websocket.StatusNormalClosure, "")
	}
	cancel()
	<-done
}
