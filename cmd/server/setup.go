package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
	"golang.org/x/crypto/ssh"
)

const connectionKeyName = "client_ed25519_key"

// Initialization is additive: upgrades never replace existing identities,
// passwords, access lists or configuration, including an intentionally empty list.
func initializeServiceFiles(name, fileRoot string) error {
	dir := filepath.Dir(name)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if _, err := ensureInstallationKey(filepath.Join(dir, "ssh_host_ed25519_key")); err != nil {
		return err
	}
	public, err := ensureInstallationKey(filepath.Join(dir, connectionKeyName))
	if err != nil {
		return err
	}
	if err := createInstallationFile(filepath.Join(dir, "authorized_keys"), public); err != nil {
		return err
	}
	var random [6]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	if err := createInstallationFile(filepath.Join(dir, "vnc-password.txt"), []byte(base64.RawURLEncoding.EncodeToString(random[:])+"\n")); err != nil {
		return err
	}
	// The file root is separate from configuration, binaries and credentials.
	if err := os.MkdirAll(fileRoot, 0755); err != nil {
		return err
	}
	root, err := yaml.Marshal(fileRoot)
	if err != nil {
		return err
	}
	settings := fmt.Sprintf(`ssh:
  listen: "0.0.0.0:5922"
  host_key_file: ssh_host_ed25519_key
  authorized_keys_file: authorized_keys
  user: vncuser
  allow_vnc: true
sftp:
  mode: read_write
  root: %s
vnc:
  engine: libvncserver
  listen: "127.0.0.1:5900"
  executable: backends/windows/svnc-desktop.exe
  password_file: vnc-password.txt
  view_only: false
  compatibility:
    enabled: false
audit: true
`, strings.TrimSpace(string(root)))
	return createInstallationFile(name, []byte(settings))
}

func ensureInstallationKey(name string) ([]byte, error) {
	if _, err := os.Lstat(name); errors.Is(err, os.ErrNotExist) {
		_, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		block, err := ssh.MarshalPrivateKey(private, "JumpVNC")
		if err != nil {
			return nil, err
		}
		if err := createInstallationFile(name, pem.EncodeToMemory(block)); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	private, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	defer clear(private)
	signer, err := ssh.ParsePrivateKey(private)
	if err != nil {
		return nil, fmt.Errorf("read installation key %s: %w", filepath.Base(name), err)
	}
	public := ssh.MarshalAuthorizedKey(signer.PublicKey())
	return public, createInstallationFile(name+".pub", public)
}

func createInstallationFile(name string, data []byte) error {
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		info, statErr := os.Lstat(name)
		if statErr != nil {
			return statErr
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("installation file %s must be a regular file", filepath.Base(name))
		}
		return nil
	}
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	err = errors.Join(writeErr, f.Sync(), f.Close())
	if err != nil {
		_ = os.Remove(name)
	}
	return err
}

func validatePublicKeys(data []byte) error {
	if len(data) > 1<<20 {
		return errors.New("公钥列表不能超过 1 MiB")
	}
	count := 0
	for i, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		key, _, options, _, err := ssh.ParseAuthorizedKey(line)
		if err != nil || len(options) != 0 {
			return fmt.Errorf("第 %d 行不是有效的 SSH 公钥（不接受 authorized_keys 选项）", i+1)
		}
		if _, cert := key.(*ssh.Certificate); cert {
			return errors.New("暂不支持 SSH 证书，请使用普通公钥")
		}
		count++
	}
	if count == 0 {
		return errors.New("请至少保留一个客户端公钥；停止访问可使用“停止服务”")
	}
	return nil
}
