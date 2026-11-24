package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

const sshTimeout = 12 * time.Second

type Profile struct {
	TrustHostKey      string `json:"trustHostKey,omitempty"`
	TrustHost         string `json:"trustHost,omitempty"`
	Transport         string `json:"transport"`
	Host              string `json:"host"`
	Port              int    `json:"port"`
	User              string `json:"user"`
	Jump              string `json:"jump"`
	Identity          string `json:"identity"`
	KnownHosts        string `json:"knownHosts"`
	HostKeyAlias      string `json:"hostKeyAlias"`
	VNCPort           int    `json:"vncPort"`
	VNCHost           string `json:"vncHost,omitempty"`
	VNCPassword       string `json:"vncPassword"`
	VNCUsername       string `json:"vncUsername,omitempty"`
	StoredPassword    bool   `json:"storedPassword"`
	SSHPassword       string `json:"sshPassword,omitempty"`
	StoredSSHPassword bool   `json:"storedSSHPassword,omitempty"`
	PasswordRef       string `json:"passwordRef,omitempty"`
}

func defaultProfile() Profile {
	return Profile{Transport: "subsystem", Port: 5922, User: "vncuser"}
}

func preconfiguredProfile() Profile {
	p := defaultProfile()
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
	// The managed g15 deployment uses the standard SVNC subsystem endpoint.
	p.VNCPort = 0
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
	saved := preconfiguredProfile()
	// The pinned server identity survives a move from a jump route to direct access.
	// Connect releases the password only after that server's host key is verified.
	if !saved.StoredPassword || p.Port != saved.Port ||
		p.HostKeyAlias != saved.HostKeyAlias || p.KnownHosts != saved.KnownHosts || p.User != saved.User ||
		p.Identity != saved.Identity || p.VNCPort != saved.VNCPort || p.Transport != saved.Transport {
		return "", errors.New("已保存的密码只用于预配置的 g15 连接，请输入 VNC 密码")
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
	p = p.withDefaults()
	host := strings.Trim(strings.TrimSpace(p.Host), "[]")
	if host == "" || strings.ContainsAny(host, " /\\\r\n\t\x00") || strings.HasPrefix(host, "-") || p.User == "" {
		return nil, errors.New("请填写有效的 Agent 主机与用户名")
	}
	if err := p.validateTransport(); err != nil {
		return nil, err
	}
	if strings.HasPrefix(p.Jump, "-") || strings.ContainsAny(p.Jump, " \t\r\n\x00") {
		return nil, errors.New("跳板机应为 SSH 主机名或别名")
	}
	auth, cleanup, err := p.authMethods()
	if err != nil {
		return nil, err
	}
	defer cleanup()

	address := net.JoinHostPort(host, strconv.Itoa(p.Port))
	keyAddress := p.hostKeyAddress(address)
	sshConfig := &ssh.ClientConfig{
		User: p.User, Auth: auth,
		HostKeyCallback: p.hostKeyCallback(keyAddress, address),
	}

	ctx, cancel := context.WithTimeout(ctx, sshTimeout)
	defer cancel()
	var raw net.Conn
	if p.Jump == "" {
		raw, err = (&net.Dialer{}).DialContext(ctx, "tcp", address)
	} else {
		raw, err = dialJump(ctx, p, address)
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

// Profiles saved before subsystems were introduced keep their original tunnel.
func (p Profile) validateTransport() error {
	if p.Transport != "" && p.Transport != "subsystem" && p.Transport != "forward" && p.Transport != "vnc" {
		return errors.New("不支持的桌面连接方式")
	}
	if p.Port < 1 || p.Port > 65535 || p.VNCPort < 0 || p.VNCPort > 65535 || ((p.Transport == "" || p.Transport == "forward") && p.VNCPort == 0) {
		return errors.New("端口应在 1–65535 之间；SSH 隧道需要有效的 VNC 端口")
	}
	if !validHost(p.Host) || (p.VNCHost != "" && !validHost(p.VNCHost)) {
		return errors.New("请填写有效的服务器地址")
	}
	return nil
}

func (p Profile) withDefaults() Profile {
	if p.Port == 0 {
		if p.Transport == "vnc" {
			p.Port = 5900
		} else {
			p.Port = 5922
		}
	}
	if p.User == "" && p.Transport != "vnc" {
		p.User = "vncuser"
	}
	if p.Transport == "" && p.VNCPort == 0 {
		p.Transport = "subsystem"
	}
	return p
}

func validHost(host string) bool {
	host = strings.Trim(host, "[]")
	return host != "" && !strings.ContainsAny(host, " /\\\r\n\t\x00") && !strings.HasPrefix(host, "-")
}
