package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

func (p Profile) authMethods() ([]ssh.AuthMethod, func(), error) {
	cleanup := func() {}
	var passwordMethods []ssh.AuthMethod
	if p.SSHPassword != "" {
		passwordMethods = []ssh.AuthMethod{ssh.Password(p.SSHPassword)}
	}
	if p.Identity != "" {
		name, err := homePath(p.Identity)
		if err != nil {
			return nil, cleanup, err
		}
		key, err := os.ReadFile(name)
		if err != nil {
			if len(passwordMethods) != 0 {
				return passwordMethods, cleanup, nil
			}
			return nil, cleanup, fmt.Errorf("读取私钥: %w", err)
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			if len(passwordMethods) != 0 {
				return passwordMethods, cleanup, nil
			}
			return nil, cleanup, fmt.Errorf("读取私钥失败；如使用加密私钥，请加入本机 SSH agent 并留空私钥字段: %w", err)
		}
		return append([]ssh.AuthMethod{ssh.PublicKeys(signer)}, passwordMethods...), cleanup, nil
	}
	var agentClient agent.ExtendedAgent
	if socket := os.Getenv("SSH_AUTH_SOCK"); socket != "" {
		if conn, err := net.DialTimeout("unix", socket, time.Second); err == nil {
			conn.SetDeadline(time.Now().Add(sshTimeout))
			cleanup = func() { conn.Close() }
			agentClient = agent.NewClient(conn)
		}
	}
	var signers []ssh.Signer
	for _, name := range []string{"~/.ssh/id_ed25519", "~/.ssh/id_ecdsa", "~/.ssh/id_rsa"} {
		name, err := homePath(name)
		if err != nil {
			continue
		}
		key, err := os.ReadFile(name)
		if err != nil {
			continue
		}
		if signer, err := ssh.ParsePrivateKey(key); err == nil {
			signers = append(signers, signer)
		}
	}
	if agentClient == nil && len(signers) == 0 {
		if len(passwordMethods) != 0 {
			return passwordMethods, cleanup, nil
		}
		if p.Transport == "forward" {
			return nil, cleanup, &AuthChallenge{Fields: []string{"sshPassword"}, Message: "请填写 SSH 密码，或在连接设置中指定私钥"}
		}
		return nil, cleanup, errors.New("未找到本机身份，请选择私钥，或将私钥加入 SSH agent")
	}
	// SSH tries each authentication method once, so combine all public keys.
	method := ssh.PublicKeysCallback(func() ([]ssh.Signer, error) {
		keys := append([]ssh.Signer(nil), signers...)
		if agentClient != nil {
			agentKeys, err := agentClient.Signers()
			if err != nil && len(keys) == 0 {
				return nil, fmt.Errorf("读取本机 SSH agent: %w", err)
			}
			keys = append(agentKeys, keys...)
		}
		return keys, nil
	})
	return append([]ssh.AuthMethod{method}, passwordMethods...), cleanup, nil
}

type AuthChallenge struct {
	Fields  []string `json:"fields"`
	Message string   `json:"message"`
}

func (c *AuthChallenge) Error() string { return c.Message }

type HostKeyChallenge struct {
	Host        string `json:"host"`
	Algorithm   string `json:"algorithm"`
	Fingerprint string `json:"fingerprint"`
}

func (c *HostKeyChallenge) Error() string { return "请确认服务器身份：" + c.Fingerprint }

var knownHostsMu sync.Mutex

func (p Profile) hostKeyCallback(keyAddress, address string) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		knownHostsMu.Lock()
		defer knownHostsMu.Unlock()
		name := p.KnownHosts
		if name == "" {
			name = "~/.ssh/known_hosts"
		}
		name, err := homePath(name)
		if err != nil {
			return err
		}
		_, statErr := os.Stat(name)
		if statErr == nil {
			check, err := knownhosts.New(name)
			if err != nil {
				return fmt.Errorf("读取服务器身份记录: %w", err)
			}
			if err := check(keyAddress, targetAddr(address), key); err == nil {
				return nil
			} else {
				var unknown *knownhosts.KeyError
				if !errors.As(err, &unknown) || len(unknown.Want) != 0 {
					return fmt.Errorf("服务器身份与本机记录不一致，请核对服务器指纹: %w", err)
				}
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		fingerprint := ssh.FingerprintSHA256(key)
		label := address
		if p.Jump != "" {
			label = p.Jump + " → " + address
		}
		if p.TrustHostKey == "" || (p.TrustHost != "" && p.TrustHost != label) {
			return &HostKeyChallenge{Host: label, Algorithm: key.Type(), Fingerprint: fingerprint}
		}
		if p.TrustHostKey != fingerprint {
			return errors.New("服务器身份在确认后发生变化，请重新核对")
		}
		if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
			return err
		}
		file, err := os.OpenFile(name, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0600)
		if err != nil {
			return fmt.Errorf("保存服务器身份: %w", err)
		}
		_, err = fmt.Fprintln(file, "\n"+knownhosts.Line([]string{keyAddress}, key))
		return errors.Join(err, file.Close())
	}
}

// Loopback endpoints on different jump hosts are different server identities.
func (p Profile) hostKeyAddress(address string) string {
	if p.HostKeyAlias != "" {
		return net.JoinHostPort(p.HostKeyAlias, "22")
	}
	if p.Jump == "" {
		return address
	}
	sum := sha256.Sum256([]byte(p.Jump + "\x00" + address))
	return net.JoinHostPort(fmt.Sprintf("svnc-%x", sum[:12]), "22")
}
