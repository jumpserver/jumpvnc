package config

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestFilePanelCannotExposeConfigurationOrCredentials(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "config.yaml")
	original := []byte("ssh:\n  user: user\n  host_key_file: host\n  authorized_keys_file: keys\n  allow_vnc: true\nsftp:\n  mode: disabled\n")
	for _, file := range []string{"host", "keys"} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(name, original, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := WithSFTPSettings(name, original, "read_write", dir); err == nil {
		t.Fatal("enabled file access to the configuration and credential directory")
	}
	root := t.TempDir()
	for _, mode := range []string{"read_write", "read_only", "write_only", "disabled"} {
		updated, err := WithSFTPSettings(name, original, mode, root)
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := decodeSettings(bytes.NewReader(updated))
		if err != nil || cfg.SFTP.Mode != mode || cfg.SFTP.Root != root || cfg.SSH.User != "user" || !cfg.SSH.AllowVNC {
			t.Fatalf("unexpected file-access edit for %s", mode)
		}
	}
}
