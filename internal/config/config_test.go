package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
