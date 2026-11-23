package main

import (
	"bytes"
	"errors"
	"path/filepath"

	"sshvnc/internal/config"
	"sshvnc/internal/vnc"
)

const (
	emSetPasswordChar = 0x00cc
	passwordHint      = "新密码须为 1–8 个可打印 ASCII 字符。保存后重启生效。"
)

func desktopPasswordFile(name string, cfg config.Config) string {
	if !cfg.SSH.AllowVNC || cfg.VNC.Engine != "libvncserver" || cfg.VNC.PasswordFile == "" {
		return ""
	}
	if filepath.IsAbs(cfg.VNC.PasswordFile) {
		return cfg.VNC.PasswordFile
	}
	return filepath.Join(filepath.Dir(name), cfg.VNC.PasswordFile)
}

func (t *windowsTray) setPanelPasswordFile(name string) {
	p := &t.panel
	changed := p.passwordFile != name
	p.passwordFile = name
	p.hidePassword()
	if changed {
		setControlText(p.passwordInput, "")
	}
	available := name != ""
	setControlEnabled(p.passwordCurrent, available)
	setControlEnabled(p.passwordShow, available)
	setControlEnabled(p.passwordInput, available)
	p.updatePasswordSave()
	if available {
		setControlText(p.passwordHint, passwordHint)
	} else {
		setControlText(p.passwordHint, "仅托管的 LibVNCServer 后端支持在此管理密码。")
	}
}

func (p *managementWindow) hidePassword() {
	p.passwordRevealed = false
	sendMessage.Call(p.passwordCurrent, emSetPasswordChar, '*', 0)
	masked := ""
	if p.passwordFile != "" {
		masked = "********"
	}
	setControlText(p.passwordCurrent, masked)
	setControlText(p.passwordShow, "显示密码")
}

func (p *managementWindow) updatePasswordSave() {
	length, _, _ := getWindowTextLength.Call(p.passwordInput)
	setControlEnabled(p.passwordSave, p.passwordFile != "" && length != 0)
}

func (t *windowsTray) closePanel() {
	t.panel.hidePassword()
	setControlText(t.panel.passwordInput, "")
	showWindow.Call(t.panel.window, swHide)
}

func (t *windowsTray) currentPanelPasswordFile() (string, error) {
	cfg, err := config.ReadSettings(t.configPath)
	if err != nil {
		return "", err
	}
	name := desktopPasswordFile(t.configPath, cfg)
	if name == "" || name != t.panel.passwordFile {
		return "", errors.New("VNC 后端或密码文件已更改，请重新打开管理窗口")
	}
	return name, nil
}

func (t *windowsTray) togglePanelPassword() {
	p := &t.panel
	if p.passwordFile == "" {
		return
	}
	if p.passwordRevealed {
		p.hidePassword()
		return
	}
	name, err := t.currentPanelPasswordFile()
	if err != nil {
		showTrayError(p.window, "无法读取 VNC 密码", err)
		return
	}
	password, err := vnc.ReadDesktopPassword(name)
	if err != nil {
		showTrayError(p.window, "无法读取 VNC 密码", err)
		return
	}
	defer clear(password)
	setControlText(p.passwordCurrent, string(password))
	sendMessage.Call(p.passwordCurrent, emSetPasswordChar, 0, 0)
	p.passwordRevealed = true
	setControlText(p.passwordShow, "隐藏密码")
}

func (t *windowsTray) savePanelPassword() {
	p := &t.panel
	password := controlText(p.passwordInput)
	if p.passwordFile == "" || password == "" {
		return
	}
	name, err := t.currentPanelPasswordFile()
	if err == nil {
		err = writeDesktopPassword(name, password)
	}
	if err != nil {
		showTrayError(p.window, "无法保存 VNC 密码", err)
		return
	}
	p.hidePassword()
	setControlText(p.passwordInput, "")
	setControlText(p.passwordHint, "密码已保存。重启服务后使用新密码。")
}

func writeDesktopPassword(name, password string) error {
	if err := vnc.ValidateDesktopPassword(password); err != nil {
		return err
	}
	return updateProtectedFile(name, func(original []byte) ([]byte, error) {
		if bytes.HasSuffix(original, []byte("\r\n")) {
			return []byte(password + "\r\n"), nil
		}
		return []byte(password + "\n"), nil
	})
}
