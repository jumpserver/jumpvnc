package sshgateway

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"
)

// ServeVNC exposes the existing backend's RFB authentication unchanged. It is
// an explicitly enabled compatibility endpoint, without SSH authentication.
func (s *Server) ServeVNC(ctx context.Context, listener net.Listener) error {
	if !s.cfg.VNC.Compatibility.Enabled || !s.cfg.SSH.AllowVNC {
		listener.Close()
		return errors.New("VNC compatibility is disabled")
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(ctx, func() { listener.Close() })
	var active sync.WaitGroup
	defer func() {
		cancel()
		stop()
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
		if err != nil || len(slots) == cap(slots) || !rate.allow(ip, time.Now()) {
			conn.Close()
			continue
		}
		log := s.log.With("transport", "vnc_compatibility", "connection_id", nextID, "source_ip", ip)
		nextID++
		slots <- struct{}{}
		active.Go(func() {
			defer func() { <-slots }()
			defer conn.Close()
			dialer := net.Dialer{Timeout: 3 * time.Second}
			backend, err := dialer.DialContext(ctx, "tcp4", s.cfg.VNC.Listen)
			if err != nil {
				log.Info("vnc.compatibility", "result", "failed", "reason", "backend_unavailable")
				return
			}
			defer backend.Close()
			viewer := &idleConn{Conn: conn, timeout: time.Duration(s.cfg.SSH.Limits.IdleTimeoutSeconds) * time.Second}
			viewer.enable()
			s.relayVNC(ctx, viewer, backend.(*net.TCPConn), log)
		})
	}
}

func (c *idleConn) CloseWrite() error {
	return c.Conn.(*net.TCPConn).CloseWrite()
}
