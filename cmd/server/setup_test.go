package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"

	"sshvnc/internal/config"
	"sshvnc/internal/vnc"
)

func TestInstallationGeneratesWorkingDistinctCredentialsAndPreservesUpgrades(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "service.yaml")
	root := filepath.Join(t.TempDir(), "files")
	if err := initializeServiceFiles(name, root); err != nil {
		t.Fatal(err)
	}
	read := func(file string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	host, err := ssh.ParsePrivateKey(read("ssh_host_ed25519_key"))
	if err != nil {
		t.Fatal(err)
	}
	client, err := ssh.ParsePrivateKey(read(connectionKeyName))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(host.PublicKey().Marshal(), client.PublicKey().Marshal()) {
		t.Fatal("the server and client must have separate identities")
	}
	if !bytes.Equal(read("authorized_keys"), ssh.MarshalAuthorizedKey(client.PublicKey())) {
		t.Fatal("the generated client is not authorized")
	}
	if err := validatePublicKeys(read("authorized_keys")); err != nil {
		t.Fatal(err)
	}
	if _, err := vnc.ReadDesktopPassword(filepath.Join(dir, "vnc-password.txt")); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.ReadSettings(name)
	if err != nil || cfg.SSH.User != "vncuser" || cfg.SFTP.Mode != "read_write" || cfg.SFTP.Root != root || cfg.VNC.Compatibility.Enabled {
		t.Fatalf("unexpected installation configuration: %+v, %v", cfg, err)
	}
	// An intentionally revoked key list must never be repopulated on upgrade.
	if err := os.WriteFile(filepath.Join(dir, "authorized_keys"), []byte("# revoked\n"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot := map[string][]byte{}
	for _, file := range []string{"service.yaml", "ssh_host_ed25519_key", connectionKeyName, "authorized_keys", "vnc-password.txt"} {
		snapshot[file] = read(file)
	}
	if err := initializeServiceFiles(name, root); err != nil {
		t.Fatal(err)
	}
	for file, original := range snapshot {
		if !bytes.Equal(original, read(file)) {
			t.Fatalf("upgrade replaced %s", file)
		}
	}
	if err := createInstallationFile(dir, []byte("not a file")); err == nil {
		t.Fatal("accepted a directory in place of a credential file")
	}
}

func TestPublicKeysRejectPrivateKeysAndUnusableAuthorization(t *testing.T) {
	for _, input := range []string{"", "# none\n", "ssh-ed25519 invalid", "-----BEGIN OPENSSH PRIVATE KEY-----", "command=\"sh\" invalid"} {
		if validatePublicKeys([]byte(input)) == nil {
			t.Fatalf("accepted %q", input)
		}
	}
}
