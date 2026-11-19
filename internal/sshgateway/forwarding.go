package sshgateway

import (
	"context"
	"io"
	"log/slog"
	"net"
	"strconv"
	"time"

	"golang.org/x/crypto/ssh"
)

type forwardRequest struct {
	Host       string
	Port       uint32
	OriginHost string
	OriginPort uint32
}

func (s *Server) forward(ctx context.Context, incoming ssh.NewChannel, log *slog.Logger) {
	var req forwardRequest
	if err := ssh.Unmarshal(incoming.ExtraData(), &req); err != nil || len(req.Host) > 253 || req.Port == 0 || req.Port > 65535 {
		incoming.Reject(ssh.Prohibited, "invalid forwarding request")
		log.Info("ssh.forward", "result", "denied", "reason", "invalid_payload")
		return
	}
	target := net.JoinHostPort(req.Host, strconv.FormatUint(uint64(req.Port), 10))
	log = log.With("target", target)
	if !s.cfg.SSH.AllowVNC || target != s.cfg.VNC.Listen {
		incoming.Reject(ssh.Prohibited, "forwarding target not permitted")
		log.Info("ssh.forward", "result", "denied", "reason", "target_policy")
		return
	}

	dialer := net.Dialer{Timeout: 3 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp4", s.cfg.VNC.Listen)
	if err != nil {
		incoming.Reject(ssh.ConnectionFailed, "VNC backend unavailable")
		log.Info("ssh.forward", "result", "failed", "reason", "backend_unavailable")
		return
	}
	backend := conn.(*net.TCPConn)
	defer backend.Close()
	stop := context.AfterFunc(ctx, func() { backend.Close() })
	defer stop()

	channel, requests, err := incoming.Accept()
	if err != nil {
		log.Info("ssh.forward", "result", "failed", "reason", "channel_closed")
		return
	}
	defer channel.Close()
	done := make(chan struct{})
	defer close(done)
	go denyRequests(requests, done, log)

	log.Info("ssh.forward", "result", "allowed")
	start := time.Now()
	log.Info("vnc.session", "event", "start")
	type transfer struct {
		bytes int64
		err   error
	}
	toVNC := make(chan transfer, 1)
	go func() {
		n, err := io.Copy(backend, channel)
		backend.CloseWrite()
		if !closedNormally(err) {
			backend.Close()
			channel.Close()
		}
		toVNC <- transfer{n, err}
	}()
	downloaded, copyErr := io.Copy(channel, backend)
	channel.CloseWrite()
	if !closedNormally(copyErr) {
		backend.Close()
		channel.Close()
	}
	upload := <-toVNC
	reason := "eof"
	switch {
	case ctx.Err() != nil:
		reason = "shutdown"
	case !closedNormally(copyErr) || !closedNormally(upload.err):
		reason = "io_error"
	}
	log.Info("vnc.session", "event", "end", "reason", reason, "duration_ms", time.Since(start).Milliseconds(),
		"bytes_to_vnc", upload.bytes, "bytes_from_vnc", downloaded)
}
