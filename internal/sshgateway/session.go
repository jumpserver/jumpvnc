package sshgateway

import (
	"context"
	"io"
	"log/slog"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

func (s *Server) session(ctx context.Context, incoming ssh.NewChannel, log *slog.Logger) {
	if (s.files == nil && !s.cfg.SSH.AllowVNC) || len(incoming.ExtraData()) != 0 {
		incoming.Reject(ssh.Prohibited, "session not permitted")
		log.Info("policy.denied", "reason", "session_policy")
		return
	}
	channel, requests, err := incoming.Accept()
	if err != nil {
		return
	}
	defer channel.Close()
	timer := time.NewTimer(time.Duration(s.cfg.SSH.Limits.HandshakeTimeoutSeconds) * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			log.Info("policy.denied", "reason", "subsystem_timeout")
			return
		case req, ok := <-requests:
			if !ok {
				return
			}
			var subsystem struct{ Name string }
			if req.Type != "subsystem" || ssh.Unmarshal(req.Payload, &subsystem) != nil {
				rejectRequest(req, log)
				continue
			}
			if subsystem.Name == "vnc" && s.cfg.SSH.AllowVNC {
				timer.Stop()
				s.vncSubsystem(ctx, channel, requests, req, log.With("subsystem", "vnc"))
				return
			}
			if subsystem.Name != "sftp" || s.files == nil {
				rejectRequest(req, log)
				continue
			}
			timer.Stop()
			if req.WantReply && req.Reply(true, nil) != nil {
				return
			}
			done := make(chan struct{})
			defer close(done)
			go denyRequests(requests, done, log)
			server := sftp.NewRequestServer(channel, s.files.Handlers(log))
			defer server.Close()
			start := time.Now()
			log.Info("sftp.session", "event", "start")
			result := "success"
			if err := server.Serve(); !closedNormally(err) {
				result = "interrupted"
			}
			log.Info("sftp.session", "event", "end", "result", result, "duration_ms", time.Since(start).Milliseconds())
			return
		}
	}
}

func denyRequests(requests <-chan *ssh.Request, done <-chan struct{}, log *slog.Logger) {
	for {
		select {
		case <-done:
			return
		case req, ok := <-requests:
			if !ok {
				return
			}
			rejectRequest(req, log)
		}
	}
}

func rejectRequest(req *ssh.Request, log *slog.Logger) {
	if req.WantReply {
		req.Reply(false, nil)
	}
	log.Info("policy.denied", "request", req.Type)
}

func (s *Server) vncSubsystem(ctx context.Context, channel ssh.Channel, requests <-chan *ssh.Request, req *ssh.Request, log *slog.Logger) {
	if !s.desktops.enter() {
		rejectRequest(req, log)
		return
	}
	login := &desktopLogin{}
	defer func() { s.desktops.leave(login.exclusive) }()
	backend, err := s.dialVNC(ctx)
	if err != nil {
		rejectRequest(req, log)
		log.Info("vnc.subsystem", "result", "failed", "reason", "backend_unavailable")
		return
	}
	defer backend.Close()
	if req.WantReply && req.Reply(true, nil) != nil {
		return
	}
	if s.login == nil {
		done := make(chan struct{})
		defer close(done)
		go denyRequests(requests, done, log)
		s.relayVNC(ctx, channel, backend, log)
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case request, ok := <-requests:
				if !ok {
					return
				}
				login.handle(ctx, s, request, log)
			}
		}
	}()
	s.relayDesktop(ctx, channel, backend, io.TeeReader(backend, &login.proof), log)
	cancel()
	<-done
}
