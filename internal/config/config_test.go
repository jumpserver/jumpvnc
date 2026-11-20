package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSFTPHomeDefault(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	} else {
		t.Setenv("HOME", home)
	}
	for _, name := range []string{"host", "authorized"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	base := "ssh:\n  user: desktop\n  host_key_file: host\n  authorized_keys_file: authorized\nsftp:\n  mode: read_write\n"
	name := filepath.Join(dir, "config.yaml")
	for _, root := range []string{"", "  root: \"~\"\n", "  root: \"\"\n"} {
		if err := os.WriteFile(name, []byte(base+root), 0600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(name)
		if err != nil {
			t.Fatal(err)
		}
		resolved, err := filepath.EvalSymlinks(home)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.SFTP.Root != resolved || !cfg.SFTP.Home {
			t.Fatalf("wrong home: %+v", cfg.SFTP)
		}
	}
	// A HOME containing the Agent's own credentials must still be rejected.
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", dir)
	} else {
		t.Setenv("HOME", dir)
	}
	if _, err := Load(name); err == nil || !strings.Contains(err.Error(), "protected file") {
		t.Fatalf("exposed credentials in HOME: %v", err)
	}
}

func TestLibVNCConfiguration(t *testing.T) {
	dir := t.TempDir()
	for _, file := range []string{"host", "authorized", "desktop.exe", "password"} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "exchange"), 0700); err != nil {
		t.Fatal(err)
	}
	base := "ssh:\n  user: desktop\n  host_key_file: host\n  authorized_keys_file: authorized\n  allow_vnc: true\nvnc:\n  engine: libvncserver\n  executable: desktop.exe\n  password_file: password\n  view_only: true\nsftp:\n  mode: read_write\n  root: exchange\n"
	name := filepath.Join(dir, "config.yaml")
	write := func(text string) {
		t.Helper()
		if err := os.WriteFile(name, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(base)
	cfg, err := Load(name)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.VNC.Executable != filepath.Join(dir, "desktop.exe") || cfg.VNC.PasswordFile != filepath.Join(dir, "password") || !cfg.VNC.ViewOnly {
		t.Fatalf("unexpected backend configuration: %+v", cfg.VNC)
	}
	write(strings.Replace(base, "  password_file: password\n", "", 1))
	if _, err := Load(name); err == nil {
		t.Fatal("accepted backend without authentication configuration")
	}
	write(strings.Replace(base, "root: exchange", "root: .", 1))
	if _, err := Load(name); err == nil || !strings.Contains(err.Error(), "protected file") {
		t.Fatalf("protected files exposed through SFTP: %v", err)
	}
}

func TestVNCCompatibilityConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, sshListen, extra, wantListen string
		allowVNC, wantError                bool
	}{
		{name: "disabled_by_default", allowVNC: true},
		{name: "default_port", allowVNC: true, extra: "vnc:\n  compatibility:\n    enabled: true\n", wantListen: "0.0.0.0:5923"},
		{name: "associated_port", allowVNC: true, sshListen: "127.0.0.1:12222", extra: "vnc:\n  compatibility:\n    enabled: true\n", wantListen: "127.0.0.1:12223"},
		{name: "ipv6", allowVNC: true, sshListen: "[::1]:5922", extra: "vnc:\n  compatibility:\n    enabled: true\n", wantListen: "[::1]:5923"},
		{name: "explicit_port", allowVNC: true, sshListen: "127.0.0.1:65535", extra: "vnc:\n  compatibility:\n    enabled: true\n    listen: 127.0.0.1:15922\n", wantListen: "127.0.0.1:15922"},
		{name: "overflow", allowVNC: true, sshListen: "127.0.0.1:65535", extra: "vnc:\n  compatibility:\n    enabled: true\n", wantError: true},
		{name: "zero", allowVNC: true, extra: "vnc:\n  compatibility:\n    enabled: true\n    listen: 127.0.0.1:0\n", wantError: true},
		{name: "ssh_conflict", allowVNC: true, extra: "vnc:\n  compatibility:\n    enabled: true\n    listen: 127.0.0.1:5922\n", wantError: true},
		{name: "backend_conflict", allowVNC: true, extra: "vnc:\n  compatibility:\n    enabled: true\n    listen: 127.0.0.1:5900\n", wantError: true},
		{name: "requires_desktop_permission", extra: "sftp:\n  mode: read_only\nvnc:\n  compatibility:\n    enabled: true\n", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := "ssh:\n  user: desktop\n  host_key_file: host\n  authorized_keys_file: authorized\n"
			if tc.allowVNC {
				base += "  allow_vnc: true\n"
			}
			if tc.sshListen != "" {
				base += "  listen: \"" + tc.sshListen + "\"\n"
			}
			name := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(name, []byte(base+tc.extra), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(name)
			if tc.wantError {
				if err == nil {
					t.Fatal("accepted invalid compatibility configuration")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.VNC.Compatibility.Listen != tc.wantListen {
				t.Fatalf("compatibility listen = %q", cfg.VNC.Compatibility.Listen)
			}
			if tc.sshListen == "" && cfg.SSH.Listen != "0.0.0.0:5922" {
				t.Fatalf("SSH listen = %q", cfg.SSH.Listen)
			}
			if tc.wantListen == "" && cfg.VNC.Compatibility.Enabled {
				t.Fatal("compatibility enabled by default")
			}
		})
	}
}
