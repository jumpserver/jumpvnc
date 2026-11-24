package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"

	"jumpvnc/internal/config"
	"jumpvnc/internal/vnc"
)

// This is an explicit local credential-display action, never a startup log.
// The client private key is referenced by path, never printed.
func installationConnectionInfo(name string) (string, error) {
	name, err := filepath.Abs(name)
	if err != nil {
		return "", err
	}
	cfg, err := config.ReadSettings(name)
	if err != nil {
		return "", err
	}
	resolve := func(path string) string {
		if filepath.IsAbs(path) {
			return path
		}
		return filepath.Join(filepath.Dir(name), path)
	}
	private, err := os.ReadFile(resolve(cfg.SSH.HostKeyFile))
	if err != nil {
		return "", err
	}
	defer clear(private)
	host, err := ssh.ParsePrivateKey(private)
	if err != nil {
		return "", err
	}
	password := "使用原有桌面密码"
	if cfg.VNC.Engine == "libvncserver" && cfg.VNC.PasswordFile != "" {
		secret, err := vnc.ReadDesktopPassword(resolve(cfg.VNC.PasswordFile))
		if err != nil {
			return "", err
		}
		password = string(secret)
		clear(secret)
	}
	identity := "使用原有客户端私钥"
	clientPath := filepath.Join(filepath.Dir(name), connectionKeyName)
	if client, err := os.ReadFile(clientPath); err == nil {
		defer clear(client)
		key, err := ssh.ParsePrivateKey(client)
		if err == nil {
			if keys, err := os.ReadFile(resolve(cfg.SSH.AuthorizedKeysFile)); err == nil && authorizedContains(keys, key.PublicKey()) {
				identity = clientPath
			}
		}
	}
	listen, port, _ := net.SplitHostPort(cfg.SSH.Listen)
	var addresses []string
	if listen != "" && listen != "0.0.0.0" && listen != "::" {
		addresses = []string{listen}
	} else if interfaces, err := net.InterfaceAddrs(); err == nil {
		for _, address := range interfaces {
			ip, _, err := net.ParseCIDR(address.String())
			if err == nil && ip.To4() != nil && !ip.IsLoopback() {
				addresses = append(addresses, ip.String())
			}
		}
	}
	if len(addresses) == 0 {
		hostname, _ := os.Hostname()
		addresses = []string{hostname}
	}
	return fmt.Sprintf("协议: SVNC\n服务器: %s\n端口: %s\n用户名: %s\nVNC 密码: %s\n主机指纹: %s\n连接私钥文件: %s\n配置文件: %s\n", strings.Join(addresses, ", "), port, cfg.SSH.User, password, ssh.FingerprintSHA256(host.PublicKey()), identity, name), nil
}
