package main

import (
	"bytes"
	"errors"
	"os"
	"sync"
	"testing"
)

type memoryPasswordStore struct {
	mu        sync.Mutex
	values    map[string]savedPasswords
	failSet   bool
	beforeSet func()
}

func (s *memoryPasswordStore) Get(ref string) (savedPasswords, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[ref]
	if !ok {
		return value, errPasswordNotFound
	}
	return value, nil
}
func (s *memoryPasswordStore) Set(ref string, value savedPasswords) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failSet {
		return errors.New("fixture keychain unavailable")
	}
	if s.beforeSet != nil {
		s.beforeSet()
	}
	s.values[ref] = value
	return nil
}
func (s *memoryPasswordStore) Delete(ref string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, ref)
	return nil
}

func TestSavedPasswordsLifecycle(t *testing.T) {
	s := testLibrary(t)
	server := testServer()
	server.Profile.Transport = "forward"
	server.Profile.StoredPassword = true
	server.Profile.SSHPassword = "ssh-fixture"
	server.Profile.StoredSSHPassword = true
	lib, err := s.saveServer(server)
	if err != nil {
		t.Fatal(err)
	}
	server = lib.Servers[0]
	p := server.Profile
	if p.VNCPassword != "" || p.SSHPassword != "" || p.PasswordRef == "" || !p.StoredPassword || !p.StoredSSHPassword {
		t.Fatal("returned secrets or lost stored credential metadata")
	}
	data, _ := os.ReadFile(s.filename)
	if bytes.Contains(data, []byte("never-persist-this")) || bytes.Contains(data, []byte("ssh-fixture")) {
		t.Fatal("plaintext password written into connection config")
	}
	reopened := &libraryStore{filename: s.filename, passwords: s.passwords}
	resolved, err := reopened.resolvePasswords(server.ID, p)
	if err != nil || resolved.VNCPassword != "never-persist-this" || resolved.SSHPassword != "ssh-fixture" {
		t.Fatalf("saved credentials did not survive restart: %v", err)
	}
	first := p.PasswordRef
	server.Name = "renamed"
	lib, err = s.saveServer(server)
	if err != nil {
		t.Fatal(err)
	}
	server = lib.Servers[0]
	if server.Profile.PasswordRef != first {
		t.Fatal("blank edit did not retain existing password")
	}
	server.Profile.VNCPassword = "replacement"
	lib, err = s.saveServer(server)
	if err != nil {
		t.Fatal(err)
	}
	server = lib.Servers[0]
	if _, err := s.passwords.Get(first); !errors.Is(err, errPasswordNotFound) {
		t.Fatal("replaced password revision retained")
	}
	resolved, err = s.resolvePasswords(server.ID, server.Profile)
	if err != nil || resolved.VNCPassword != "replacement" || resolved.SSHPassword != "ssh-fixture" {
		t.Fatal("password edit discarded another credential")
	}
	server.Profile.StoredPassword = false
	lib, err = s.saveServer(server)
	if err != nil {
		t.Fatal(err)
	}
	server = lib.Servers[0]
	resolved, err = s.resolvePasswords(server.ID, server.Profile)
	if err != nil || resolved.VNCPassword != "" || resolved.SSHPassword != "ssh-fixture" || server.Profile.StoredPassword {
		t.Fatal("unchecking saved VNC password did not clear it independently")
	}
	changed := server.Profile
	changed.Host = "other.example"
	resolved, err = s.resolvePasswords(server.ID, changed)
	if err != nil || resolved.SSHPassword != "" || resolved.StoredSSHPassword {
		t.Fatal("credential released to an edited endpoint")
	}
	server.Profile.Host = "other.example"
	lib, err = s.saveServer(server)
	if err != nil {
		t.Fatal(err)
	}
	server = lib.Servers[0]
	if server.Profile.PasswordRef != "" || server.Profile.StoredSSHPassword {
		t.Fatal("endpoint change reused old credentials")
	}
	server.Profile.VNCPassword = "delete-fixture"
	server.Profile.StoredPassword = true
	lib, err = s.saveServer(server)
	if err != nil {
		t.Fatal(err)
	}
	ref := lib.Servers[0].Profile.PasswordRef
	if _, err := s.deleteServer(server.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.passwords.Get(ref); !errors.Is(err, errPasswordNotFound) {
		t.Fatal("deleted connection retained password")
	}
}

func TestPasswordSaveFailuresPreservePreviousCredentials(t *testing.T) {
	s := testLibrary(t)
	mem := s.passwords.(*memoryPasswordStore)
	server := testServer()
	server.Profile.StoredPassword = true
	lib, err := s.saveServer(server)
	if err != nil {
		t.Fatal(err)
	}
	server = lib.Servers[0]
	oldRef := server.Profile.PasswordRef
	before, _ := os.ReadFile(s.filename)
	server.Profile.VNCPassword = "replacement"
	mem.failSet = true
	if _, err := s.saveServer(server); err == nil {
		t.Fatal("keychain failure reported as saved")
	}
	after, _ := os.ReadFile(s.filename)
	if !bytes.Equal(before, after) {
		t.Fatal("keychain failure changed config")
	}
	mem.failSet = false
	mem.beforeSet = func() {
		if err := os.Rename(s.filename, s.filename+".original"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(s.filename, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.saveServer(server); err == nil {
		t.Fatal("config failure reported as saved")
	}
	if err := os.Remove(s.filename); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(s.filename+".original", s.filename); err != nil {
		t.Fatal(err)
	}
	if len(mem.values) != 1 {
		t.Fatal("failed config write leaked a new credential revision")
	}
	if old, err := mem.Get(oldRef); err != nil || old.VNC != "never-persist-this" {
		t.Fatal("failed config write removed previous credentials")
	}
}

func TestMissingAccountAndDeletedPasswordCanBePrompted(t *testing.T) {
	s := testLibrary(t)
	server := testServer()
	server.Profile.User = ""
	server.Profile.StoredPassword = true
	lib, err := s.saveServer(server)
	if err != nil {
		t.Fatal(err)
	}
	server = lib.Servers[0]
	if server.Profile.User != "" {
		t.Fatal("blank account replaced instead of being requested at connect time")
	}
	p := server.Profile
	p.User = "entered-at-connect"
	resolved, err := s.resolvePasswords(server.ID, p)
	if err != nil || resolved.VNCPassword == "" {
		t.Fatal("missing account prevented using saved desktop password")
	}
	s.passwords.Delete(server.Profile.PasswordRef)
	resolved, err = s.resolvePasswords(server.ID, p)
	if err != nil || resolved.StoredPassword || resolved.VNCPassword != "" {
		t.Fatal("deleted keychain item did not become a credential request")
	}
}
