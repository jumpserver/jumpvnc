package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWithSFTPRoot(t *testing.T) {
	dir := t.TempDir()
	for _, file := range []string{"host", "authorized", "password", "desktop.exe"} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	root := "共享 #2"
	if err := os.Mkdir(filepath.Join(dir, root), 0700); err != nil {
		t.Fatal(err)
	}
	const base = `# Keep this header
ssh:
  listen: '127.0.0.1:15922'
  user: desktop
  host_key_file: host
  authorized_keys_file: authorized
  allow_vnc: true
sftp:
  mode: read_only
  root: missing-old-root # Keep this comment
  max_file_size_mib: 8
vnc:
  engine: libvncserver
  executable: desktop.exe
  password_file: password
  compatibility:
    enabled: true
`
	name := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(name, []byte(base), 0600); err != nil {
		t.Fatal(err)
	}
	if current, err := ReadSettings(name); err != nil || current.SFTP.Root != "missing-old-root" {
		t.Fatalf("settings must expose a broken root for editing: %+v, %v", current.SFTP, err)
	}
	for _, invalid := range []string{"", "absent", ".", "desktop.exe"} {
		if _, err := WithSFTPRoot(name, []byte(base), invalid); err == nil {
			t.Fatalf("accepted invalid root %q", invalid)
		}
	}
	updated, err := WithSFTPRoot(name, []byte(base), root)
	if err != nil {
		t.Fatal(err)
	}
	for _, comment := range []string{"# Keep this header", "# Keep this comment"} {
		if !strings.Contains(string(updated), comment) {
			t.Errorf("lost comment %q", comment)
		}
	}
	if err := os.WriteFile(name, updated, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(name)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(filepath.Join(dir, root))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SFTP.Root != want || cfg.SFTP.Mode != "read_only" || cfg.SFTP.MaxFileSizeMiB != 8 ||
		cfg.SSH.Listen != "127.0.0.1:15922" || cfg.VNC.Compatibility.Listen != "127.0.0.1:15923" {
		t.Fatalf("unexpected saved settings: %+v", cfg)
	}
}
