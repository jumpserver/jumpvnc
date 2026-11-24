package sshgateway

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"

	"golang.org/x/crypto/ssh"

	"jumpvnc/internal/config"
)

func loadAuth(cfg config.SSH) (ssh.Signer, map[string]struct{}, error) {
	hostKey, err := openCredential(cfg.HostKeyFile, 0077)
	if err != nil {
		return nil, nil, fmt.Errorf("open SSH host key: %w", err)
	}
	defer hostKey.Close()
	key, err := io.ReadAll(hostKey)
	if err != nil {
		return nil, nil, fmt.Errorf("read SSH host key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("parse SSH host key: %w", err)
	}

	f, err := openCredential(cfg.AuthorizedKeysFile, 0022)
	if err != nil {
		return nil, nil, fmt.Errorf("read authorized keys: %w", err)
	}
	defer f.Close()
	keys := make(map[string]struct{})
	scanner := bufio.NewScanner(f)
	for line := 1; scanner.Scan(); line++ {
		data := bytes.TrimSpace(scanner.Bytes())
		if len(data) == 0 || data[0] == '#' {
			continue
		}
		pub, _, options, _, err := ssh.ParseAuthorizedKey(data)
		if err != nil || len(options) != 0 {
			return nil, nil, fmt.Errorf("authorized keys line %d: expected an unqualified public key", line)
		}
		if _, isCert := pub.(*ssh.Certificate); isCert {
			return nil, nil, fmt.Errorf("authorized keys line %d: certificates are unsupported", line)
		}
		keys[string(pub.Marshal())] = struct{}{}
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, err
	}
	if len(keys) == 0 {
		return nil, nil, errors.New("authorized keys file is empty")
	}
	return signer, keys, nil
}

func openCredential(name string, forbidden os.FileMode) (*os.File, error) {
	info, err := os.Stat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 ||
		(runtime.GOOS != "windows" && info.Mode().Perm()&forbidden != 0) {
		return nil, fmt.Errorf("%q must be a regular file of at most 1 MiB with restricted permissions", name)
	}
	return os.Open(name)
}

func (s *Server) authConfig(log *slog.Logger) *ssh.ServerConfig {
	cfg := &ssh.ServerConfig{
		MaxAuthTries: 3,
		PublicKeyCallback: func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if _, ok := s.keys[string(key.Marshal())]; ok && conn.User() == s.cfg.SSH.User {
				return nil, nil
			}
			return nil, errors.New("unauthorized public key")
		},
		AuthLogCallback: func(conn ssh.ConnMetadata, method string, err error) {
			// OpenSSH probes with "none" to discover the available methods.
			if method == "none" {
				return
			}
			result := "success"
			if err != nil {
				result = "denied"
			}
			log.Info("ssh.auth", "user", conn.User(), "method", method, "result", result)
		},
	}
	cfg.AddHostKey(s.signer)
	return cfg
}
