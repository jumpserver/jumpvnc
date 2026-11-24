package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	sshconfig "github.com/kevinburke/ssh_config"
	"golang.org/x/crypto/ssh"
)

// Jump hosts use their own identities and trust records, independently of the
// destination. No config directive is allowed to launch a local command.
func jumpProfile(spec string, config *sshconfig.Config) (Profile, string, error) {
	u, err := url.Parse("ssh://" + spec)
	if err != nil || u.Hostname() == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(spec, " \t\r\n\x00") {
		return Profile{}, "", fmt.Errorf("无效的跳板机: %s", spec)
	}
	if u.User != nil {
		if _, password := u.User.Password(); password {
			return Profile{}, "", errors.New("跳板机地址不能包含密码")
		}
	}
	alias := u.Hostname()
	values := make(map[string]string)
	for _, key := range []string{"HostName", "User", "Port", "IdentityFile", "UserKnownHostsFile", "HostKeyAlias", "ProxyJump", "ProxyCommand"} {
		values[key], err = config.Get(alias, key)
		if err != nil {
			return Profile{}, "", fmt.Errorf("读取跳板机 %s 的 %s: %w", alias, key, err)
		}
	}
	if command := values["ProxyCommand"]; command != "" && !strings.EqualFold(command, "none") {
		return Profile{}, "", errors.New("Go SSH 不执行 ProxyCommand，请将跳板机配置改为 ProxyJump")
	}
	p := Profile{Transport: "subsystem", Host: alias, Port: 22, User: values["User"],
		Identity: values["IdentityFile"], KnownHosts: values["UserKnownHostsFile"], HostKeyAlias: values["HostKeyAlias"]}
	if host := values["HostName"]; host != "" {
		p.Host = strings.ReplaceAll(host, "%h", alias)
	}
	port := values["Port"]
	if u.Port() != "" {
		port = u.Port()
	}
	if port != "" {
		p.Port, err = strconv.Atoi(port)
		if err != nil || p.Port < 1 || p.Port > 65535 {
			return Profile{}, "", errors.New("跳板机端口应在 1–65535 之间")
		}
	}
	if u.User != nil {
		p.User = u.User.Username()
	}
	if p.User == "" {
		current, err := user.Current()
		if err != nil {
			return Profile{}, "", fmt.Errorf("读取本机用户名: %w", err)
		}
		p.User = current.Username
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Profile{}, "", err
	}
	expand := strings.NewReplacer("%%", "%", "%d", home, "%h", p.Host, "%n", alias, "%p", strconv.Itoa(p.Port), "%r", p.User)
	p.Identity = expand.Replace(p.Identity)
	p.KnownHosts = expand.Replace(p.KnownHosts)
	if strings.EqualFold(p.Identity, "none") {
		return Profile{}, "", errors.New("跳板机需要私钥或 SSH agent 身份")
	}
	if strings.EqualFold(p.KnownHosts, "none") {
		return Profile{}, "", errors.New("跳板机必须保存并校验主机身份")
	}
	if err := p.validateTransport(); err != nil {
		return Profile{}, "", err
	}
	return p, values["ProxyJump"], nil
}

func jumpRoute(spec string, config *sshconfig.Config, visiting map[string]bool, depth int) ([]Profile, error) {
	if depth >= 8 {
		return nil, errors.New("跳板机配置嵌套过深")
	}
	var route []Profile
	for _, hop := range strings.Split(spec, ",") {
		if visiting[hop] {
			return nil, errors.New("跳板机配置存在循环")
		}
		p, proxy, err := jumpProfile(hop, config)
		if err != nil {
			return nil, err
		}
		if proxy != "" && !strings.EqualFold(proxy, "none") {
			visiting[hop] = true
			parents, err := jumpRoute(proxy, config, visiting, depth+1)
			delete(visiting, hop)
			if err != nil {
				return nil, err
			}
			route = append(route, parents...)
		}
		route = append(route, p)
		if len(route) > 8 {
			return nil, errors.New("最多支持 8 层跳板机")
		}
	}
	return route, nil
}

type jumpConn struct {
	net.Conn
	clients []*ssh.Client
	once    sync.Once
}

func (c *jumpConn) Close() (err error) {
	c.once.Do(func() {
		if c.Conn != nil {
			err = c.Conn.Close()
		}
		for i := len(c.clients) - 1; i >= 0; i-- {
			c.clients[i].Close()
		}
	})
	return err
}

func dialJump(ctx context.Context, destination Profile, target string) (_ net.Conn, err error) {
	filename, err := homePath("~/.ssh/config")
	if err != nil {
		return nil, err
	}
	config := &sshconfig.Config{}
	file, err := os.Open(filepath.Clean(filename))
	if err == nil {
		defer file.Close()
		config, err = sshconfig.Decode(file)
	} else if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取 SSH 配置: %w", err)
	}
	route, err := jumpRoute(destination.Jump, config, make(map[string]bool), 0)
	if err != nil {
		return nil, err
	}
	transport := &jumpConn{}
	defer func() {
		if err != nil {
			transport.Close()
		}
	}()
	for _, p := range route {
		address := net.JoinHostPort(p.Host, strconv.Itoa(p.Port))
		// Confirmation is scoped to one endpoint, so accepting a jump host
		// never implicitly trusts the destination, even if they share a key.
		if destination.TrustHost == address {
			p.TrustHost, p.TrustHostKey = destination.TrustHost, destination.TrustHostKey
		}
		auth, cleanup, err := p.authMethods()
		if err != nil {
			return nil, fmt.Errorf("跳板机 %s: %w", address, err)
		}
		client, err := func() (*ssh.Client, error) {
			defer cleanup()
			var raw net.Conn
			var err error
			if len(transport.clients) == 0 {
				raw, err = (&net.Dialer{}).DialContext(ctx, "tcp", address)
			} else {
				raw, err = transport.clients[len(transport.clients)-1].DialContext(ctx, "tcp", address)
			}
			if err != nil {
				return nil, err
			}
			stop := context.AfterFunc(ctx, func() { raw.Close() })
			conn, channels, requests, err := ssh.NewClientConn(raw, address, &ssh.ClientConfig{
				User: p.User, Auth: auth, HostKeyCallback: p.hostKeyCallback(p.hostKeyAddress(address), address),
			})
			if !stop() || err != nil {
				raw.Close()
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				return nil, err
			}
			return ssh.NewClient(conn, channels, requests), nil
		}()
		if err != nil {
			return nil, fmt.Errorf("连接跳板机 %s: %w", address, err)
		}
		transport.clients = append(transport.clients, client)
	}
	transport.Conn, err = transport.clients[len(transport.clients)-1].DialContext(ctx, "tcp", target)
	if err != nil {
		return nil, fmt.Errorf("跳板机转发: %w", err)
	}
	return transport, nil
}
