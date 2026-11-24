package main

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func testLibrary(t *testing.T) *libraryStore {
	t.Helper()
	return &libraryStore{filename: filepath.Join(t.TempDir(), "connections.json"),
		initial: Library{Version: 1, Folders: []Folder{}, Servers: []Server{}}, passwords: &memoryPasswordStore{values: map[string]savedPasswords{}}}
}

func testServer() Server {
	return Server{Name: "工作站 2", Profile: Profile{Host: "127.0.0.1", Port: 5922, User: "desktop",
		VNCPort: 5900, Identity: "~/.ssh/id_ed25519", KnownHosts: "~/.ssh/known_hosts", VNCPassword: "never-persist-this"}}
}

func TestLibraryRoundTripAndFolderRemoval(t *testing.T) {
	s := testLibrary(t)
	lib, err := s.saveFolder(Folder{Name: " 工作 "})
	if err != nil {
		t.Fatal(err)
	}
	folder := lib.Folders[0]
	if folder.Name != "工作" {
		t.Fatal("name not trimmed")
	}
	server := testServer()
	server.FolderID = folder.ID
	lib, err = s.saveServer(server)
	if err != nil {
		t.Fatal(err)
	}
	server = lib.Servers[0]
	if server.ID == "" || server.CreatedAt == 0 || server.Profile.VNCPassword != "" {
		t.Fatalf("unexpected saved server: %+v", server)
	}
	data, err := os.ReadFile(s.filename)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("never-persist-this")) {
		t.Fatal("password persisted")
	}
	assertPrivateFile(t, s.filename)
	// Reload through a separate store, as after restarting the app.
	reopened := &libraryStore{filename: s.filename}
	lib, err = reopened.read()
	if err != nil || len(lib.Servers) != 1 || lib.Servers[0].ID != server.ID {
		t.Fatalf("reload failed: %v", err)
	}
	if _, err := reopened.favorite(server.ID, true); err != nil {
		t.Fatal(err)
	}
	server.Name = "重命名"
	server.Favorite = true
	server.CreatedAt = 1
	lib, err = reopened.saveServer(server)
	if err != nil {
		t.Fatal(err)
	}
	if lib.Servers[0].CreatedAt == 1 {
		t.Fatal("edit changed creation time")
	}
	lib, err = reopened.deleteFolder(folder.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(lib.Folders) != 0 || len(lib.Servers) != 1 || lib.Servers[0].FolderID != "" || !lib.Servers[0].Favorite {
		t.Fatal("folder deletion must preserve connections and favorites")
	}
	lib, err = reopened.deleteServer(server.ID)
	if err != nil || len(lib.Servers) != 0 {
		t.Fatalf("delete failed: %v", err)
	}
}

func TestLibraryRejectsInvalidAndStaleEdits(t *testing.T) {
	s := testLibrary(t)
	lib, err := s.saveFolder(Folder{Name: "工作"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.saveFolder(Folder{Name: "工作"}); err == nil {
		t.Fatal("duplicate folder accepted")
	}
	server := testServer()
	server.FolderID = "deleted"
	if _, err := s.saveServer(server); err == nil {
		t.Fatal("missing folder accepted")
	}
	server.FolderID = lib.Folders[0].ID
	server.Profile.VNCPort = 70000
	if _, err := s.saveServer(server); err == nil {
		t.Fatal("invalid VNC port accepted")
	}
	server.Profile.VNCPort = 5900
	server.ID = "deleted"
	if _, err := s.saveServer(server); err == nil {
		t.Fatal("stale server edit recreated a connection")
	}
	if _, err := s.saveFolder(Folder{ID: "deleted", Name: "恢复"}); err == nil {
		t.Fatal("stale folder edit recreated a folder")
	}
}

func TestLibraryPreservesCorruptAndFutureFiles(t *testing.T) {
	for _, original := range []string{`{"version":`, `{"version":2}`, `{"version":1,"folders":[{"id":"same","name":"one"},{"id":"same","name":"two"}]}`} {
		t.Run(original, func(t *testing.T) {
			s := testLibrary(t)
			if err := os.WriteFile(s.filename, []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.saveServer(testServer()); err == nil {
				t.Fatal("invalid library overwritten")
			}
			data, err := os.ReadFile(s.filename)
			if err != nil || string(data) != original {
				t.Fatal("original file changed")
			}
		})
	}
}

func TestLibraryConcurrentMutationsDoNotLoseConnections(t *testing.T) {
	s := testLibrary(t)
	var workers sync.WaitGroup
	for range 12 {
		workers.Go(func() {
			if _, err := s.saveServer(testServer()); err != nil {
				t.Error(err)
			}
		})
	}
	workers.Wait()
	lib, err := s.read()
	if err != nil {
		t.Fatal(err)
	}
	if len(lib.Servers) != 12 {
		t.Fatalf("lost updates: saved %d connections", len(lib.Servers))
	}
}

func TestSubsystemProfileAndLegacyLibrary(t *testing.T) {
	setTestHome(t, t.TempDir())
	profile := defaultProfile()
	if profile.Transport != "subsystem" || profile.Port != 5922 {
		t.Fatalf("unexpected default: %+v", profile)
	}
	s := testLibrary(t)
	server := testServer()
	server.Profile.Transport, server.Profile.VNCPort = "subsystem", 0
	lib, err := s.saveServer(server)
	if err != nil {
		t.Fatal(err)
	}
	if lib.Servers[0].Profile.Transport != "subsystem" {
		t.Fatal("subsystem setting not saved")
	}
	server.Profile.Transport = "unsupported"
	if _, err := s.saveServer(server); err == nil {
		t.Fatal("unknown transport accepted")
	}

	legacy := `{"version":1,"folders":[],"servers":[{"id":"legacy","name":"old Agent","profile":{"host":"127.0.0.1","port":2225,"vncPort":5905}}]}`
	if err := os.WriteFile(s.filename, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	lib, err = s.read()
	if err != nil {
		t.Fatal(err)
	}
	p := lib.Servers[0].Profile
	if p.Transport != "forward" || p.Port != 2225 || p.VNCPort != 5905 {
		t.Fatalf("changed legacy connection: %+v", p)
	}
	data, err := os.ReadFile(s.filename)
	if err != nil || string(data) != legacy {
		t.Fatal("reading library rewrote legacy file")
	}
}

func TestMinimalSVNCProfile(t *testing.T) {
	s := testLibrary(t)
	lib, err := s.saveServer(Server{Name: "desktop", Profile: Profile{Host: "desktop.example", TrustHostKey: "temporary-confirmation", TrustHost: "temporary-endpoint"}})
	if err != nil {
		t.Fatal(err)
	}
	p := lib.Servers[0].Profile
	if p.Port != 5922 || p.User != "" || p.Transport != "subsystem" || p.TrustHostKey != "" || p.TrustHost != "" {
		t.Fatalf("unexpected minimal profile defaults: %+v", p)
	}
	if p.Identity != "" || p.KnownHosts != "" {
		t.Fatal("minimal profile requires manual identity paths")
	}
	data, err := os.ReadFile(s.filename)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("temporary-confirmation")) || bytes.Contains(data, []byte("temporary-endpoint")) {
		t.Fatal("persisted a one-time host identity approval")
	}
}

func TestViewerProfileSavePreservesConnectionMetadata(t *testing.T) {
	s := testLibrary(t)
	server := testServer()
	server.Profile.User = ""
	server.Profile.StoredPassword = true
	lib, err := s.saveServer(server)
	if err != nil {
		t.Fatal(err)
	}
	server = lib.Servers[0]
	if _, err := s.favorite(server.ID, true); err != nil {
		t.Fatal(err)
	}
	server.Name = "renamed elsewhere"
	server.Favorite = true
	lib, err = s.saveServer(server)
	if err != nil {
		t.Fatal(err)
	}
	previous := lib.Servers[0]
	profile := previous.Profile
	profile.User = "entered-account"
	saved, err := s.saveConnectionProfile(previous.ID, profile)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Name != "renamed elsewhere" || !saved.Favorite || saved.FolderID != previous.FolderID || saved.CreatedAt != previous.CreatedAt || saved.LastOpened != previous.LastOpened {
		t.Fatal("viewer save overwrote manager metadata")
	}
	if !saved.Profile.StoredPassword || saved.Profile.PasswordRef != previous.Profile.PasswordRef {
		t.Fatal("saving a previously missing account discarded the saved password")
	}
	resolved, err := s.resolvePasswords(saved.ID, saved.Profile)
	if err != nil || resolved.VNCPassword != "never-persist-this" {
		t.Fatal("viewer credential save could not be reused")
	}
	profile.VNCPassword = "new-vnc-password"
	profile.StoredPassword = true
	saved, err = s.saveConnectionProfile(saved.ID, profile)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Profile.VNCPassword != "" {
		t.Fatal("viewer save returned a plaintext password")
	}
	resolved, err = s.resolvePasswords(saved.ID, saved.Profile)
	if err != nil || resolved.VNCPassword != "new-vnc-password" {
		t.Fatal("viewer save did not replace password")
	}
	if _, err := s.saveConnectionProfile("deleted", profile); err == nil {
		t.Fatal("viewer save recreated a deleted connection")
	}
	if _, err := s.saveConnectionProfile("", profile); err == nil {
		t.Fatal("viewer save created an unnamed connection")
	}
}
