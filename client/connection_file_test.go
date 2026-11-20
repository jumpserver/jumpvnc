package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConnectionFileRoundTripExcludesLocalCredentials(t *testing.T) {
	server := testServer()
	server.Profile.Transport = "subsystem"
	server.Profile.SSHPassword = "ssh-secret"
	server.Profile.TrustHostKey = "trusted-key"
	server.Profile.PasswordRef = "keychain-ref"
	server.Profile.StoredPassword, server.Profile.StoredSSHPassword = true, true
	data, err := encodeConnectionFile(server)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"secret", "never-persist-this", "trusted-key", "keychain-ref", "identity", "knownHosts", "storedPassword", "passwordRef", "vncPort"} {
		if bytes.Contains(data, []byte(forbidden)) {
			t.Fatalf("export contains local state: %s", forbidden)
		}
	}
	servers, err := decodeConnectionFile(data)
	if err != nil || len(servers) != 1 {
		t.Fatalf("roundtrip: %v", err)
	}
	p := servers[0].Profile
	if p.Host != server.Profile.Host || p.Transport != "subsystem" || p.Identity != "" || p.StoredPassword || p.PasswordRef != "" || servers[0].ID != "" {
		t.Fatalf("unexpected portable profile: %+v", servers[0])
	}
}

func TestConnectionFileValidationAndDefaults(t *testing.T) {
	if servers, err := readConnectionFile("../configs/example.svnc"); err != nil || len(servers) != 1 {
		t.Fatalf("documented example: %+v, %v", servers, err)
	}
	for _, transport := range []string{"subsystem", "vnc", "forward"} {
		connection := PortableConnection{Name: "测试", Profile: PortableProfile{Transport: transport, Host: "example.com"}}
		if transport == "forward" {
			connection.Profile.VNCPort = 5900
		}
		data, _ := json.Marshal(ConnectionFile{Format: "svnc", Version: 1, Connections: []PortableConnection{connection}})
		servers, err := decodeConnectionFile(data)
		wantPort := map[string]int{"subsystem": 5922, "vnc": 5900, "forward": 22}[transport]
		if err != nil || len(servers) != 1 || servers[0].Profile.Port != wantPort || servers[0].Profile.User != "" {
			t.Fatalf("%s defaults: %+v, %v", transport, servers, err)
		}
	}
	valid := `{"format":"svnc","version":1,"connections":[{"name":"Test","profile":{"transport":"subsystem","host":"example.com"}}]}`
	for name, data := range map[string]string{
		"version":   strings.Replace(valid, `"version":1`, `"version":2`, 1),
		"password":  strings.Replace(valid, `"host":"example.com"`, `"host":"example.com","vncPassword":"secret"`, 1),
		"trust":     strings.Replace(valid, `"host":"example.com"`, `"host":"example.com","trustHostKey":"key"`, 1),
		"transport": strings.Replace(valid, "subsystem", "unknown", 1),
		"port":      strings.Replace(valid, `"host":"example.com"`, `"host":"example.com","port":65536`, 1),
		"host":      strings.Replace(valid, "example.com", "-option", 1),
		"jump":      strings.Replace(valid, `"host":"example.com"`, `"host":"example.com","jump":"-Fbad"`, 1),
		"trailing":  valid + `{}`,
		"oversize":  valid + strings.Repeat(" ", maxConnectionFileSize),
		"utf8":      strings.Replace(valid, "Test", string([]byte{0xff}), 1),
		"empty":     `{"format":"svnc","version":1,"connections":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeConnectionFile([]byte(data)); err == nil {
				t.Fatal("accepted invalid file")
			}
		})
	}
}

func TestConnectionFileImportIsAtomicAndDoesNotOverwrite(t *testing.T) {
	store := testLibrary(t)
	server := testServer()
	server.Profile.Transport = "subsystem"
	server.Profile.StoredPassword = true
	lib, err := store.saveServer(server)
	if err != nil {
		t.Fatal(err)
	}
	existing := lib.Servers[0]
	data, err := encodeConnectionFile(existing)
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(t.TempDir(), "test.SVNC")
	if err := writeConnectionFile(filename, data); err != nil {
		t.Fatal(err)
	}
	result, err := store.importConnectionFiles([]string{filename, filename})
	if err != nil || result.Added != 0 || result.Skipped != 2 || len(result.Errors) != 0 || len(result.Library.Servers) != 1 {
		t.Fatalf("duplicate import: %+v, %v", result, err)
	}
	if result.Library.Servers[0] != existing {
		t.Fatal("import changed the existing connection or credential references")
	}
	var file ConnectionFile
	json.Unmarshal(data, &file)
	file.Connections[0].Profile.Host = "other.example.com" // Same name, different endpoint is a new connection.
	data, _ = json.Marshal(file)
	os.WriteFile(filename, data, 0600)
	manager := &Manager{store: store, pendingImports: []string{filename}}
	result, err = manager.ImportPending()
	if err != nil || result.Added != 1 || len(result.Library.Servers) != 2 {
		t.Fatalf("queued file import: %+v, %v", result, err)
	}
	newServer := result.Library.Servers[1]
	if newServer.ID == "" || newServer.ID == existing.ID || newServer.Profile.StoredPassword || newServer.Profile.PasswordRef != "" || newServer.FolderID != "" || newServer.LastOpened != 0 {
		t.Fatal("import reused local connection state")
	}
	result, err = manager.ImportPending()
	if err != nil || result.Added != 0 || result.Skipped != 0 {
		t.Fatal("queued import was processed twice")
	}
	before, _ := os.ReadFile(store.filename)
	file.Connections = append(file.Connections, PortableConnection{Name: "invalid"})
	data, _ = json.Marshal(file)
	os.WriteFile(filename, data, 0600)
	result, err = store.importConnectionFiles([]string{filename})
	after, _ := os.ReadFile(store.filename)
	if err != nil || len(result.Errors) != 1 || result.Added != 0 || !bytes.Equal(before, after) {
		t.Fatal("an invalid connection partially modified the library")
	}
}
