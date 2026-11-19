package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

const sshTimeout = 12 * time.Second

type Profile struct {
	Host           string `json:"host"`
	Port           int    `json:"port"`
	User           string `json:"user"`
	Jump           string `json:"jump"`
	Identity       string `json:"identity"`
	KnownHosts     string `json:"knownHosts"`
	HostKeyAlias   string `json:"hostKeyAlias"`
	VNCPort        int    `json:"vncPort"`
	VNCPassword    string `json:"vncPassword"`
	StoredPassword bool   `json:"storedPassword"`
}

func defaultProfile() Profile {
	p := Profile{Port: 2222, User: "vncuser", VNCPort: 5900, KnownHosts: "~/.ssh/known_hosts"}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	state := filepath.Join(home, ".ssh", "sshvnc-g15")
	for _, name := range []string{"client", "known_hosts", "vnc-password.txt"} {
		if info, err := os.Stat(filepath.Join(state, name)); err != nil || !info.Mode().IsRegular() {
			return p
		}
	}
	p.Host = "127.0.0.1"
	p.Jump = "g15"
	p.Identity = filepath.Join(state, "client")
	p.KnownHosts = filepath.Join(state, "known_hosts")
	p.HostKeyAlias = "sshvnc-g15"
	p.StoredPassword = true
	return p
}

func (p Profile) password() (string, error) {
	if !p.StoredPassword || p.VNCPassword != "" {
		return p.VNCPassword, nil
	}
	saved := defaultProfile()
	if !saved.StoredPassword || p.Host != saved.Host || p.Port != saved.Port || p.Jump != saved.Jump ||
		p.HostKeyAlias != saved.HostKeyAlias || p.KnownHosts != saved.KnownHosts || p.User != saved.User ||
		p.Identity != saved.Identity || p.VNCPort != saved.VNCPort {
		return "", errors.New("已保存的密码只用于原始 g15 配置，请输入 VNC 密码")
	}
	password, err := os.ReadFile(filepath.Join(filepath.Dir(saved.Identity), "vnc-password.txt"))
	if err != nil {
		return "", fmt.Errorf("读取 g15 VNC 密码: %w", err)
	}
	return strings.TrimRight(string(password), "\r\n"), nil
}

func homePath(name string) (string, error) {
	if name == "" {
		return "", errors.New("请填写文件路径")
	}
	if strings.HasPrefix(name, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		name = filepath.Join(home, name[2:])
	}
	return filepath.Abs(name)
}

func (p Profile) dial(ctx context.Context) (*ssh.Client, error) {
	host := strings.Trim(strings.TrimSpace(p.Host), "[]")
	if host == "" || strings.ContainsAny(host, " /\\\r\n\t\x00") || strings.HasPrefix(host, "-") || p.User == "" {
		return nil, errors.New("请填写有效的 Agent 主机与用户名")
	}
	if p.Port < 1 || p.Port > 65535 || p.VNCPort < 1 || p.VNCPort > 65535 {
		return nil, errors.New("SSH 和 VNC 端口应在 1–65535 之间")
	}
	if strings.HasPrefix(p.Jump, "-") || strings.ContainsAny(p.Jump, " \t\r\n\x00") {
		return nil, errors.New("跳板机应为 SSH 主机名或别名")
	}
	identity, err := homePath(p.Identity)
	if err != nil {
		return nil, fmt.Errorf("SSH 私钥: %w", err)
	}
	key, err := os.ReadFile(identity)
	if err != nil {
		return nil, fmt.Errorf("读取 SSH 私钥: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("解析 SSH 私钥: %w", err)
	}
	hosts, err := homePath(p.KnownHosts)
	if err != nil {
		return nil, fmt.Errorf("known_hosts: %w", err)
	}
	check, err := knownhosts.New(hosts)
	if err != nil {
		return nil, fmt.Errorf("加载 known_hosts: %w", err)
	}

	address := net.JoinHostPort(host, strconv.Itoa(p.Port))
	keyAddress := address
	// OpenSSH's HostKeyAlias matches the literal alias in known_hosts.
	if p.HostKeyAlias != "" {
		keyAddress = net.JoinHostPort(p.HostKeyAlias, "22")
	}
	sshConfig := &ssh.ClientConfig{
		User: p.User, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			return check(keyAddress, targetAddr(address), key)
		},
	}

	ctx, cancel := context.WithTimeout(ctx, sshTimeout)
	defer cancel()
	var raw net.Conn
	if p.Jump == "" {
		raw, err = (&net.Dialer{}).DialContext(ctx, "tcp", address)
	} else {
		raw, err = dialJump(p.Jump, address)
	}
	if err != nil {
		return nil, fmt.Errorf("连接 Agent: %w", err)
	}
	stop := context.AfterFunc(ctx, func() { raw.Close() })
	conn, channels, requests, err := ssh.NewClientConn(raw, address, sshConfig)
	if !stop() || err != nil {
		raw.Close()
		if ctx.Err() != nil {
			return nil, fmt.Errorf("连接 Agent: %w", ctx.Err())
		}
		return nil, fmt.Errorf("Agent SSH 握手: %w", err)
	}
	return ssh.NewClient(conn, channels, requests), nil
}

type targetAddr string

func (a targetAddr) Network() string { return "tcp" }
func (a targetAddr) String() string  { return string(a) }

// OpenSSH handles the jump host, including its existing ssh_config and keys.
// Closing this transport also ends the OpenSSH stdio forward.
func dialJump(jump, target string) (net.Conn, error) {
	local, relay := net.Pipe()
	cmd := exec.Command("/usr/bin/ssh", "-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10",
		"-o", "StrictHostKeyChecking=yes", "-o", "ClearAllForwardings=yes", "-W", target, jump)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		local.Close()
		relay.Close()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		local.Close()
		relay.Close()
		return nil, err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		local.Close()
		relay.Close()
		return nil, err
	}
	go func() {
		io.Copy(stdin, relay)
		stdin.Close()
		cmd.Process.Kill()
	}()
	go func() {
		io.Copy(relay, stdout)
		relay.Close()
		cmd.Wait()
	}()
	return local, nil
}
