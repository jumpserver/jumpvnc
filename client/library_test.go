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
		initial: Library{Version: 1, Folders: []Folder{}, Servers: []Server{}}}
}

func testServer() Server {
	return Server{Name: "工作站 2", Profile: Profile{Host: "127.0.0.1", Port: 2222, User: "desktop",
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
	info, _ := os.Stat(s.filename)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("config permissions: %v", info.Mode())
	}
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
