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
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type session struct {
	id           string
	started      time.Time
	received     atomic.Int64
	sent         atomic.Int64
	rttMu        sync.RWMutex
	rtt          time.Duration
	rttAt        time.Time
	ssh          *ssh.Client
	files        *sftp.Client
	fileLocation *FileLocation
	bridge       *http.Server
	ctx          context.Context
	cancel       context.CancelFunc
	closing      sync.Once
	busy         atomic.Bool
}

func openSession(p Profile) (*session, string, error) {
	p = p.withDefaults()
	ctx, cancel := context.WithCancel(context.Background())
	if err := p.validateTransport(); err != nil {
		cancel()
		return nil, "", err
	}
	if p.Transport == "vnc" {
		raw, err := (&net.Dialer{Timeout: sshTimeout}).DialContext(ctx, "tcp", net.JoinHostPort(strings.Trim(p.Host, "[]"), strconv.Itoa(p.Port)))
		if err != nil {
			cancel()
			return nil, "", fmt.Errorf("连接 VNC: %w", err)
		}
		s := &session{id: rand.Text(), started: time.Now(), ctx: ctx, cancel: cancel}
		url, err := s.startBridge(raw)
		if err != nil {
			raw.Close()
			s.close()
			return nil, "", err
		}
		// Also close an unclaimed desktop stream when its window closes.
		context.AfterFunc(ctx, func() { raw.Close() })
		return s, url, nil
	}
	client, err := p.dial(ctx)
	if err != nil {
		cancel()
		return nil, "", err
	}
	s := &session{id: rand.Text(), started: time.Now(), ctx: ctx, cancel: cancel, ssh: client}
	timer := time.AfterFunc(sshTimeout, func() { client.Close() })
	s.files, err = sftp.NewClient(client)
	if err == nil {
		s.fileLocation = readFileLocation(client)
	}
	timer.Stop()
	// File access is optional. A denied SFTP subsystem must not deny desktop access.
	rawVNC, err := s.openDesktop(ctx, p)
	if err != nil {
		s.close()
		return nil, "", err
	}
	url, err := s.startBridge(rawVNC)
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
		if s.ssh != nil {
			s.ssh.Close()
		}
		if s.files != nil {
			s.files.Close()
		}
		if s.bridge != nil {
			s.bridge.Close()
		}
	})
}

func (s *session) keepAlive() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		start := time.Now()
		timer := time.AfterFunc(sshTimeout, func() { s.close() })
		ok, _, err := s.ssh.SendRequest("keepalive@openssh.com", true, nil)
		timer.Stop()
		if err != nil {
			s.close()
			return
		}
		if ok {
			s.rttMu.Lock()
			s.rtt, s.rttAt = time.Since(start), time.Now()
			s.rttMu.Unlock()
		}
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *session) startBridge(rawVNC io.ReadWriteCloser) (string, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("打开本机 VNC 桥接: %w", err)
	}
	token := rand.Text()
	address := listener.Addr().String()
	var attached atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("GET /rfb/"+token, func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		// WebView2 serves the bundled page at http://mygo.localhost;
		// WKWebView can serialize its bundled custom-scheme origin as null.
		if r.Host != address || (origin != "mygo://localhost" && origin != "http://mygo.localhost" && origin != "null") {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if !attached.CompareAndSwap(false, true) {
			http.Error(w, "VNC session already in use", http.StatusConflict)
			return
		}
		s.serveVNC(w, r, rawVNC)
	})
	s.bridge = &http.Server{Handler: mux, ReadHeaderTimeout: 4 * time.Second}
	go func() { s.bridge.Serve(listener) }()
	return "ws://" + address + "/rfb/" + token, nil
}

func (s *session) serveVNC(w http.ResponseWriter, r *http.Request, rawVNC io.ReadWriteCloser) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // Origin and session token were checked before upgrading.
	})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	vnc := &measuredConn{Conn: rawVNC, received: &s.received, sent: &s.sent}
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

func (s *session) openDesktop(ctx context.Context, p Profile) (io.ReadWriteCloser, error) {
	if p.Transport != "subsystem" {
		host := p.VNCHost
		if host == "" {
			host = "127.0.0.1"
		}
		target := net.JoinHostPort(strings.Trim(host, "[]"), strconv.Itoa(p.VNCPort))
		return s.ssh.DialContext(ctx, "tcp", target)
	}
	ctx, cancel := context.WithTimeout(ctx, sshTimeout)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { s.ssh.Close() })
	defer stop()
	channel, requests, err := s.ssh.OpenChannel("session", nil)
	if err != nil {
		return nil, fmt.Errorf("打开桌面通道: %w", err)
	}
	go ssh.DiscardRequests(requests)
	ok, err := channel.SendRequest("subsystem", true, ssh.Marshal(struct{ Name string }{"vnc"}))
	if !stop() || ctx.Err() != nil {
		channel.Close()
		return nil, ctx.Err()
	}
	if err != nil || !ok {
		channel.Close()
		if err != nil {
			return nil, fmt.Errorf("打开 VNC subsystem: %w", err)
		}
		return nil, errors.New("服务端拒绝 VNC subsystem；旧 Agent 可选择 VNC 协议并启用 SSH 隧道")
	}
	return channel, nil
}
