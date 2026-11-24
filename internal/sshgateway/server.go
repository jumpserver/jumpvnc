package sshgateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"

	"jumpvnc/internal/config"
	"jumpvnc/internal/sftpfs"
)

type Server struct {
	cfg            config.Config
	log            *slog.Logger
	signer         ssh.Signer
	keys           map[string]struct{}
	files          *sftpfs.Filesystem
	dialVNCBackend func(context.Context) (*net.TCPConn, error)
}

// SetVNCDialer configures backend verification before the server begins serving.
func (s *Server) SetVNCDialer(dial func(context.Context) (*net.TCPConn, error)) {
	s.dialVNCBackend = dial
}

func (s *Server) dialVNC(ctx context.Context) (*net.TCPConn, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if s.dialVNCBackend != nil {
		return s.dialVNCBackend(ctx)
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp4", s.cfg.VNC.Listen)
	if err != nil {
		return nil, err
	}
	return conn.(*net.TCPConn), nil
}

func New(cfg config.Config, log *slog.Logger) (*Server, error) {
	signer, keys, err := loadAuth(cfg.SSH)
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, log: log, signer: signer, keys: keys}
	if cfg.SFTP.Mode != "disabled" {
		s.files, err = sftpfs.Open(cfg.SFTP)
		if err != nil {
			return nil, fmt.Errorf("open SFTP root: %w", err)
		}
	}
	return s, nil
}

func (s *Server) Close() error {
	if s.files != nil {
		return s.files.Close()
	}
	return nil
}

func (s *Server) HostFingerprint() string {
	return ssh.FingerprintSHA256(s.signer.PublicKey())
}

func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	ctx, cancel := context.WithCancel(ctx)
	stopListener := context.AfterFunc(ctx, func() { listener.Close() })
	var active sync.WaitGroup
	defer func() {
		cancel()
		stopListener()
		listener.Close()
		active.Wait()
	}()

	slots := make(chan struct{}, s.cfg.SSH.Limits.MaxConnections)
	rate := newHandshakeRate(s.cfg.SSH.Limits.HandshakesPerMinutePerIP)
	var nextID int64
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		ip, _, err := net.SplitHostPort(conn.RemoteAddr().String())
		if err != nil {
			conn.Close()
			continue
		}
		log := s.log.With("connection_id", nextID, "source_ip", ip)
		nextID++
		if len(slots) == cap(slots) || !rate.allow(ip, time.Now()) {
			conn.Close()
			// Avoid logging per rejected TCP connection under load.
			continue
		}
		slots <- struct{}{}
		active.Go(func() {
			defer func() { <-slots }()
			s.serveConn(ctx, conn, log)
		})
	}
}

func (s *Server) serveConn(ctx context.Context, raw net.Conn, log *slog.Logger) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer raw.Close()
	stop := context.AfterFunc(ctx, func() { raw.Close() })
	defer stop()

	limits := s.cfg.SSH.Limits
	if err := raw.SetDeadline(time.Now().Add(time.Duration(limits.HandshakeTimeoutSeconds) * time.Second)); err != nil {
		return
	}
	transport := &idleConn{Conn: raw, timeout: time.Duration(limits.IdleTimeoutSeconds) * time.Second}
	conn, channels, requests, err := ssh.NewServerConn(transport, s.authConfig(log))
	if err != nil {
		log.Info("ssh.connection", "result", "handshake_failed")
		return
	}
	transport.enable()
	log = log.With("user", conn.User())

	var handlers sync.WaitGroup
	defer func() {
		cancel()
		conn.Close()
		handlers.Wait()
	}()
	handlers.Go(func() {
		for req := range requests {
			allowed := req.Type == "keepalive@openssh.com" && len(req.Payload) == 0
			var payload []byte
			if (req.Type == "sftp-location@jumpvnc" || req.Type == "sftp-location@sshvnc") && len(req.Payload) == 0 && s.files != nil {
				allowed = true
				payload, _ = json.Marshal(struct {
					Root string `json:"root"`
					Home bool   `json:"home"`
				}{s.cfg.SFTP.Root, s.cfg.SFTP.Home})
			}
			if req.WantReply {
				req.Reply(allowed, payload)
			}
			if !allowed {
				log.Info("policy.denied", "request", req.Type)
			}
		}
	})
	slots := make(chan struct{}, limits.MaxChannelsPerConnection)
	var nextID int
	for channel := range channels {
		channelLog := log.With("channel_id", nextID, "channel_type", channel.ChannelType())
		nextID++
		switch channel.ChannelType() {
		case "direct-tcpip", "session":
		default:
			channel.Reject(ssh.UnknownChannelType, "unsupported channel")
			channelLog.Info("policy.denied", "reason", "unsupported_channel")
			continue
		}

		select {
		case slots <- struct{}{}:
			handlers.Go(func() {
				defer func() { <-slots }()
				if channel.ChannelType() == "direct-tcpip" {
					s.forward(ctx, channel, channelLog)
					return
				}
				s.session(ctx, channel, channelLog)
			})
		default:
			channel.Reject(ssh.ResourceShortage, "too many channels")
			channelLog.Info("policy.denied", "reason", "channel_limit")
		}
	}
}

type idleConn struct {
	net.Conn
	timeout time.Duration
	active  atomic.Bool
}

func (c *idleConn) enable() {
	c.SetDeadline(time.Now().Add(c.timeout))
	c.active.Store(true)
}

func (c *idleConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 && c.active.Load() {
		c.SetReadDeadline(time.Now().Add(c.timeout))
	}
	return n, err
}

func (c *idleConn) Write(p []byte) (int, error) {
	if c.active.Load() {
		c.SetWriteDeadline(time.Now().Add(c.timeout))
	}
	n, err := c.Conn.Write(p)
	if n > 0 && c.active.Load() {
		c.SetReadDeadline(time.Now().Add(c.timeout))
	}
	return n, err
}

// handshakeRate bounds both admissions per IP and memory used by the IP table.
type handshakeRate struct {
	max     int
	expires time.Time
	seen    map[string]int
}

func newHandshakeRate(max int) handshakeRate {
	return handshakeRate{max: max, seen: make(map[string]int)}
}

func (r *handshakeRate) allow(ip string, now time.Time) bool {
	if !now.Before(r.expires) {
		clear(r.seen)
		r.expires = now.Add(time.Minute)
	}
	count := r.seen[ip]
	if count >= r.max || (count == 0 && len(r.seen) >= 8192) {
		return false
	}
	r.seen[ip] = count + 1
	return true
}

func closedNormally(err error) bool {
	return err == nil || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed)
}
