package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"

	"sshvnc/internal/config"
	"sshvnc/internal/vnc"
)

func TestWriteSFTPRootPreservesConfigACL(t *testing.T) {
	dir := t.TempDir()
	for _, file := range []string{"host", "authorized"} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "exchange"), 0700); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(dir, "config.yaml")
	original := []byte("ssh:\n  user: desktop\n  host_key_file: host\n  authorized_keys_file: authorized\nsftp:\n  mode: read_only\n  root: old\n")
	if err := os.WriteFile(name, original, 0600); err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")(A;;FA;;;SY)")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(name, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	readACL := func() string {
		t.Helper()
		sd, err := windows.GetNamedSecurityInfo(name, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		return sd.String()
	}
	before := readACL()
	if err := writeSFTPRoot(name, "exchange"); err != nil {
		t.Fatal(err)
	}
	if after := readACL(); after != before {
		t.Fatalf("config ACL changed from %q to %q", before, after)
	}
	cfg, err := config.ReadSettings(name)
	if err != nil || cfg.SFTP.Root != "exchange" || cfg.SFTP.Mode != "read_only" {
		t.Fatalf("unexpected saved settings: %+v, %v", cfg.SFTP, err)
	}
}

func TestWriteDesktopPassword(t *testing.T) {
	name := filepath.Join(t.TempDir(), "vnc-password.txt")
	original := []byte("oldpass\r\n")
	if err := os.WriteFile(name, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeDesktopPassword(name, "too-long-password"); err == nil {
		t.Fatal("accepted an invalid password")
	}
	if current, err := os.ReadFile(name); err != nil || !bytes.Equal(current, original) {
		t.Fatalf("invalid edit changed the file, read error: %v", err)
	}
	if err := writeDesktopPassword(name, "new pass"); err != nil {
		t.Fatal(err)
	}
	password, err := vnc.ReadDesktopPassword(name)
	if err != nil || string(password) != "new pass" {
		t.Fatalf("backend cannot read the saved password, read error: %v", err)
	}
}
