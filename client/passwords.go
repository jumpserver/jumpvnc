package main

import (
	"crypto/rand"
	"errors"
	"log"
)

var errPasswordNotFound = errors.New("未保存密码")

type savedPasswords struct {
	VNC     string `json:"vnc,omitempty"`
	SSH     string `json:"ssh,omitempty"`
	Windows string `json:"windows,omitempty"`
}

type passwordStore interface {
	Get(string) (savedPasswords, error)
	Set(string, savedPasswords) error
	Delete(string) error
}

func sameCredentialTarget(a, b Profile) bool {
	transport := func(p Profile) string {
		if p.Transport == "" {
			return "forward"
		}
		return p.Transport
	}
	host := func(p Profile) string {
		if p.Transport != "subsystem" && p.Transport != "vnc" && p.VNCHost == "" {
			return "127.0.0.1"
		}
		return p.VNCHost
	}
	return transport(a) == transport(b) && a.Host == b.Host && a.Port == b.Port && a.User == b.User &&
		a.VNCUsername == b.VNCUsername &&
		a.Jump == b.Jump && a.Identity == b.Identity && a.KnownHosts == b.KnownHosts && a.HostKeyAlias == b.HostKeyAlias &&
		a.VNCPort == b.VNCPort && host(a) == host(b)
}

// Secret revisions are immutable: a failed config write can only orphan the new
// revision, never replace or remove the password used by the previous config.
func (s *libraryStore) preparePasswords(p *Profile, old Profile) (string, error) {
	p.PasswordRef = ""
	defer func() { p.VNCPassword, p.SSHPassword, p.WindowsPassword = "", "", "" }()
	if old.User == "" {
		old.User = p.User
	}
	if old.VNCUsername == "" {
		old.VNCUsername = p.VNCUsername
	}
	same := sameCredentialTarget(*p, old)
	sameWindows := same && p.WindowsAccount != "" && p.WindowsAccount == old.WindowsAccount
	if same && p.VNCPassword == "" && p.SSHPassword == "" && p.WindowsPassword == "" &&
		p.StoredPassword == old.StoredPassword && p.StoredSSHPassword == old.StoredSSHPassword &&
		p.StoredWindowsPassword == old.StoredWindowsPassword && (!p.StoredWindowsPassword || sameWindows) {
		p.PasswordRef = old.PasswordRef
		return "", nil
	}
	passwords := savedPasswords{}
	if same && old.PasswordRef != "" && ((p.StoredPassword && p.VNCPassword == "") || (p.StoredSSHPassword && p.SSHPassword == "") ||
		(sameWindows && p.StoredWindowsPassword && p.WindowsPassword == "")) {
		var err error
		passwords, err = s.passwords.Get(old.PasswordRef)
		if err != nil && !errors.Is(err, errPasswordNotFound) {
			return "", err
		}
	} else if same && old.StoredPassword && old.PasswordRef == "" && p.StoredPassword && p.VNCPassword == "" {
		// Keep the existing managed g15 credential available during migration.
		passwords.VNC, _ = old.password()
	}
	if p.StoredPassword {
		if p.VNCPassword != "" {
			passwords.VNC = p.VNCPassword
		}
	} else {
		passwords.VNC = ""
	}
	if p.StoredSSHPassword {
		if p.SSHPassword != "" {
			passwords.SSH = p.SSHPassword
		}
	} else {
		passwords.SSH = ""
	}
	if !p.StoredWindowsPassword {
		passwords.Windows = ""
	} else if p.WindowsPassword != "" || !sameWindows {
		passwords.Windows = p.WindowsPassword
	}
	p.StoredPassword, p.StoredSSHPassword, p.StoredWindowsPassword = passwords.VNC != "", passwords.SSH != "", passwords.Windows != ""
	if !p.StoredPassword && !p.StoredSSHPassword && !p.StoredWindowsPassword {
		return "", nil
	}
	ref := rand.Text()
	if err := s.passwords.Set(ref, passwords); err != nil {
		return "", err
	}
	p.PasswordRef = ref
	return ref, nil
}

func (s *libraryStore) cleanupPassword(ref string) {
	if ref == "" {
		return
	}
	if err := s.passwords.Delete(ref); err != nil {
		log.Printf("清理旧密码记录失败: %v", err)
	}
}

func (s *libraryStore) rememberWindowsPassword(id string, target Profile) error {
	var newRef, oldRef string
	_, err := s.update(func(lib *Library) error {
		for i := range lib.Servers {
			server := &lib.Servers[i]
			if server.ID != id {
				continue
			}
			old := server.Profile
			if !sameCredentialTarget(old.withDefaults(), target.withDefaults()) {
				return errors.New("连接目标已更改，请先保存连接设置以记住 Windows 密码")
			}
			next := old
			next.WindowsAccount, next.WindowsPassword, next.StoredWindowsPassword = target.WindowsAccount, target.WindowsPassword, true
			var err error
			newRef, err = s.preparePasswords(&next, old)
			if err != nil {
				return err
			}
			oldRef = old.PasswordRef
			server.Profile = next
			return nil
		}
		return errors.New("连接已被删除，请刷新")
	})
	if err != nil {
		s.cleanupPassword(newRef)
	} else if newRef != "" {
		s.cleanupPassword(oldRef)
	}
	return err
}

func (s *libraryStore) resolvePasswords(id string, p Profile) (Profile, error) {
	lib, err := s.read()
	if err != nil {
		return p, err
	}
	for _, server := range lib.Servers {
		if server.ID != id {
			continue
		}
		old := server.Profile
		p.StoredPassword = p.StoredPassword && old.StoredPassword
		p.StoredSSHPassword = p.StoredSSHPassword && old.StoredSSHPassword
		p.StoredWindowsPassword = p.StoredWindowsPassword && old.StoredWindowsPassword && old.WindowsAccount != "" && p.WindowsAccount == old.WindowsAccount
		if old.User == "" {
			old.User = p.User
		}
		if old.VNCUsername == "" {
			old.VNCUsername = p.VNCUsername
		}
		if !sameCredentialTarget(p, old) {
			p.StoredPassword, p.StoredSSHPassword, p.StoredWindowsPassword = false, false, false
			return p, nil
		}
		if old.PasswordRef == "" {
			return p, nil
		} // Legacy managed credential.
		if (!p.StoredPassword || p.VNCPassword != "") && (!p.StoredSSHPassword || p.SSHPassword != "") && (!p.StoredWindowsPassword || p.WindowsPassword != "") {
			return p, nil
		}
		passwords, err := s.passwords.Get(old.PasswordRef)
		if err != nil && !errors.Is(err, errPasswordNotFound) {
			return p, err
		}
		if p.StoredPassword && p.VNCPassword == "" {
			p.VNCPassword = passwords.VNC
			p.StoredPassword = passwords.VNC != ""
		}
		if p.StoredSSHPassword && p.SSHPassword == "" {
			p.SSHPassword = passwords.SSH
			p.StoredSSHPassword = passwords.SSH != ""
		}
		if p.StoredWindowsPassword && p.WindowsPassword == "" {
			p.WindowsPassword = passwords.Windows
			p.StoredWindowsPassword = passwords.Windows != ""
		}
		return p, nil
	}
	return p, errors.New("连接已被删除，请刷新")
}
