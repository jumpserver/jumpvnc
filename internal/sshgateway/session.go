package sshgateway

import (
	"log/slog"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

func (s *Server) session(incoming ssh.NewChannel, log *slog.Logger) {
	if s.files == nil || len(incoming.ExtraData()) != 0 {
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
			if req.Type != "subsystem" || ssh.Unmarshal(req.Payload, &subsystem) != nil || subsystem.Name != "sftp" {
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
