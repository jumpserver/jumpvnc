package main

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/windows"

	"sshvnc/internal/config"
	"sshvnc/internal/vnc"
)

func settingPath(name, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(filepath.Dir(name), path)
}

func (t *windowsTray) refreshPanelAuthorization(cfg config.Config) {
	p := &t.panel
	p.authorizedFile = settingPath(t.configPath, cfg.SSH.AuthorizedKeysFile)
	keys, err := os.ReadFile(p.authorizedFile)
	if err == nil && len(keys) <= 1<<20 {
		setControlText(p.authorizedKeys, strings.ReplaceAll(strings.ReplaceAll(string(keys), "\r\n", "\n"), "\n", "\r\n"))
		setControlText(p.keysHint, "已自动生成连接密钥。也可粘贴自己的公钥，每行一个。")
	} else {
		setControlText(p.keysHint, "无法读取授权公钥，请检查高级配置。")
	}
	key, err := os.ReadFile(settingPath(t.configPath, cfg.SSH.HostKeyFile))
	if err == nil {
		defer clear(key)
		signer, err := ssh.ParsePrivateKey(key)
		if err == nil {
			setControlText(p.fingerprint, ssh.FingerprintSHA256(signer.PublicKey()))
			return
		}
	}
	setControlText(p.fingerprint, "无法读取主机密钥指纹")
}

func (t *windowsTray) savePanelSSH() {
	if !t.canApplyPanelSettings() {
		return
	}
	err := updateProtectedFile(t.configPath, func(original []byte) ([]byte, error) {
		return config.WithSSHSettings(original, strings.TrimSpace(controlText(t.panel.sshListenInput)), strings.TrimSpace(controlText(t.panel.sshUserInput)))
	})
	if err != nil {
		showTrayError(t.panel.window, "无法保存连接设置", err)
		return
	}
	t.refreshPanelSettings(false)
	t.applyPanelSettings()
}

func (t *windowsTray) savePanelKeys() {
	p := &t.panel
	if !t.canApplyPanelSettings() || p.authorizedFile == "" {
		return
	}
	keys := []byte(strings.TrimSpace(controlText(p.authorizedKeys)) + "\n")
	if err := validatePublicKeys(keys); err != nil {
		showTrayError(p.window, "无法保存授权公钥", err)
		return
	}
	cfg, err := config.ReadSettings(t.configPath)
	if err == nil && settingPath(t.configPath, cfg.SSH.AuthorizedKeysFile) != p.authorizedFile {
		err = errors.New("授权文件已更改，请重新打开窗口")
	}
	if err == nil {
		err = updateProtectedFile(p.authorizedFile, func([]byte) ([]byte, error) { return keys, nil })
	}
	if err != nil {
		showTrayError(p.window, "无法保存授权公钥", err)
		return
	}
	t.applyPanelSettings()
	setControlText(p.keysHint, "公钥已保存，正在应用；已移除的公钥将无法重新连接。")
}

func (t *windowsTray) applyPanelSettings() {
	// Do not save another change halfway through a restart: all save controls
	// are disabled while the controller is busy (see updatePanel).
	t.service.update()
	if t.service.canStop() {
		t.service.stop(true)
	} else if t.service.canStart() {
		t.service.start()
	}
	t.refreshStatus()
}

func (t *windowsTray) canApplyPanelSettings() bool {
	return t.canConfigure() && (t.service.canStart() || t.service.canStop())
}

func (t *windowsTray) copyPanelSettings(command uintptr) {
	if !t.canConfigure() {
		return
	}
	cfg, err := config.ReadSettings(t.configPath)
	var text string
	if err == nil {
		switch command {
		case idKeyCopy:
			var private []byte
			private, err = os.ReadFile(filepath.Join(filepath.Dir(t.configPath), connectionKeyName))
			if err == nil {
				defer clear(private)
				var signer ssh.Signer
				signer, err = ssh.ParsePrivateKey(private)
				if err == nil {
					var authorized []byte
					authorized, err = os.ReadFile(settingPath(t.configPath, cfg.SSH.AuthorizedKeysFile))
					if err == nil && !authorizedContains(authorized, signer.PublicKey()) {
						err = errors.New("自动生成的连接密钥已不在授权列表中，请使用你配置的客户端私钥")
					}
				}
				text = string(private)
			}
		case idPublicCopy:
			text = controlText(t.panel.authorizedKeys)
		case idConnectionCopy:
			var password []byte
			password, err = vnc.ReadDesktopPassword(desktopPasswordFile(t.configPath, cfg))
			if err == nil {
				defer clear(password)
				_, port, _ := net.SplitHostPort(cfg.SSH.Listen)
				var addresses []string
				if interfaces, addressErr := net.InterfaceAddrs(); addressErr == nil {
					for _, address := range interfaces {
						ip, _, parseErr := net.ParseCIDR(address.String())
						if parseErr == nil && ip.To4() != nil && !ip.IsLoopback() {
							addresses = append(addresses, ip.String())
						}
					}
				}
				if len(addresses) == 0 {
					host, _ := os.Hostname()
					addresses = []string{host}
				}
				text = fmt.Sprintf("协议: SVNC\r\n服务器: %s\r\n端口: %s\r\n用户名: %s\r\nVNC 密码: %s\r\n主机指纹: %s\r\n", strings.Join(addresses, ", "), port, cfg.SSH.User, password, controlText(t.panel.fingerprint))
			}
		}
	}
	if err == nil {
		err = setWindowsClipboard(t.panel.window, text)
	}
	if err != nil {
		showTrayError(t.panel.window, "无法复制", err)
		return
	}
	setControlText(t.panel.keysHint, "已复制到剪贴板。")
}

func authorizedContains(data []byte, key ssh.PublicKey) bool {
	for _, line := range bytes.Split(data, []byte("\n")) {
		public, _, options, _, err := ssh.ParseAuthorizedKey(line)
		if err == nil && len(options) == 0 && bytes.Equal(public.Marshal(), key.Marshal()) {
			return true
		}
	}
	return false
}

func setWindowsClipboard(owner uintptr, text string) error {
	content, err := windows.UTF16FromString(text)
	if err != nil {
		return err
	}
	defer clear(content)
	alloc := kernel32.NewProc("GlobalAlloc")
	free := kernel32.NewProc("GlobalFree")
	lock := kernel32.NewProc("GlobalLock")
	unlock := kernel32.NewProc("GlobalUnlock")
	handle, _, err := alloc.Call(2, uintptr(len(content)*2)) // GMEM_MOVEABLE
	if handle == 0 {
		return err
	}
	defer func() {
		if handle != 0 {
			free.Call(handle)
		}
	}()
	pointer, _, err := lock.Call(handle)
	if pointer == 0 {
		return err
	}
	writeErr := windows.WriteProcessMemory(windows.CurrentProcess(), pointer, (*byte)(unsafe.Pointer(&content[0])), uintptr(len(content)*2), nil)
	unlock.Call(handle)
	if writeErr != nil {
		return writeErr
	}
	if ok, _, err := user32.NewProc("OpenClipboard").Call(owner); ok == 0 {
		return err
	}
	defer user32.NewProc("CloseClipboard").Call()
	if ok, _, err := user32.NewProc("EmptyClipboard").Call(); ok == 0 {
		return err
	}
	if ok, _, err := user32.NewProc("SetClipboardData").Call(13, handle); ok == 0 {
		return err
	}
	handle = 0 // Windows owns the buffer after SetClipboardData succeeds.
	return nil
}
