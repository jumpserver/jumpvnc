package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Folder struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Server struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	FolderID   string  `json:"folderId"`
	Favorite   bool    `json:"favorite"`
	Profile    Profile `json:"profile"`
	CreatedAt  int64   `json:"createdAt"`
	LastOpened int64   `json:"lastOpened"`
}

type Library struct {
	Version int      `json:"version"`
	Folders []Folder `json:"folders"`
	Servers []Server `json:"servers"`
}

// Each mutation reloads and atomically replaces the file. A failed write leaves
// the previous library intact and is reported to the caller.
type libraryStore struct {
	mu        sync.Mutex
	filename  string
	initial   Library
	passwords passwordStore
}

func newLibraryStore(dir string) *libraryStore {
	initial := Library{Version: 1, Folders: []Folder{}, Servers: []Server{}}
	return &libraryStore{filename: filepath.Join(dir, "connections.json"), initial: initial, passwords: newPasswordStore()}
}

func (s *libraryStore) read() (Library, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

func (s *libraryStore) load() (Library, error) {
	data, err := os.ReadFile(s.filename)
	if errors.Is(err, os.ErrNotExist) {
		lib := s.initial
		lib.Folders = append([]Folder{}, lib.Folders...)
		lib.Servers = append([]Server{}, lib.Servers...)
		return lib, nil
	}
	if err != nil {
		return Library{}, fmt.Errorf("读取连接配置: %w", err)
	}
	var lib Library
	if err := json.Unmarshal(data, &lib); err != nil {
		return Library{}, fmt.Errorf("连接配置损坏，未覆盖原文件: %w", err)
	}
	if lib.Version != 1 {
		return Library{}, errors.New("不支持此版本的连接配置")
	}
	if lib.Folders == nil {
		lib.Folders = []Folder{}
	}
	if lib.Servers == nil {
		lib.Servers = []Server{}
	}
	ids := map[string]bool{}
	for _, folder := range lib.Folders {
		if folder.ID == "" || ids[folder.ID] || strings.TrimSpace(folder.Name) == "" {
			return Library{}, errors.New("连接配置包含无效或重复的分类")
		}
		ids[folder.ID] = true
	}
	serverIDs := map[string]bool{}
	for i := range lib.Servers {
		server := &lib.Servers[i]
		if server.ID == "" || serverIDs[server.ID] || (server.FolderID != "" && !ids[server.FolderID]) {
			return Library{}, errors.New("连接配置包含无效的服务器或分类")
		}
		serverIDs[server.ID] = true
		server.Profile.VNCPassword = ""
		server.Profile.SSHPassword = ""
		server.Profile.TrustHostKey = ""
		if server.Profile.Transport == "" {
			server.Profile.Transport = "forward"
		}
	}
	return lib, nil
}

func (s *libraryStore) update(change func(*Library) error) (Library, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lib, err := s.load()
	if err != nil {
		return Library{}, err
	}
	if err := change(&lib); err != nil {
		return Library{}, err
	}
	for i := range lib.Servers {
		lib.Servers[i].Profile.VNCPassword = ""
		lib.Servers[i].Profile.SSHPassword = ""
		lib.Servers[i].Profile.TrustHostKey = ""
	}
	data, err := json.MarshalIndent(lib, "", "  ")
	if err != nil {
		return Library{}, err
	}
	if err := os.MkdirAll(filepath.Dir(s.filename), 0700); err != nil {
		return Library{}, err
	}
	file, err := os.CreateTemp(filepath.Dir(s.filename), ".connections-*")
	if err != nil {
		return Library{}, err
	}
	defer os.Remove(file.Name())
	_, err = file.Write(append(data, '\n'))
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(file.Name(), s.filename)
	}
	if err != nil {
		return Library{}, fmt.Errorf("保存连接配置: %w", err)
	}
	return lib, nil
}

func (s *libraryStore) saveServer(server Server) (Library, error) {
	return s.saveServerRecord(server, false)
}

func (s *libraryStore) saveConnectionProfile(id string, profile Profile) (Server, error) {
	if id == "" {
		return Server{}, errors.New("连接不存在，请刷新")
	}
	lib, err := s.saveServerRecord(Server{ID: id, Profile: profile}, true)
	if err != nil {
		return Server{}, err
	}
	for _, server := range lib.Servers {
		if server.ID == id {
			return server, nil
		}
	}
	return Server{}, errors.New("连接不存在，请刷新")
}

func (s *libraryStore) saveServerRecord(server Server, profileOnly bool) (Library, error) {
	server.Name = strings.TrimSpace(server.Name)
	p := &server.Profile
	p.Host, p.User, p.Jump = strings.TrimSpace(p.Host), strings.TrimSpace(p.User), strings.TrimSpace(p.Jump)
	p.Identity, p.KnownHosts, p.HostKeyAlias = strings.TrimSpace(p.Identity), strings.TrimSpace(p.KnownHosts), strings.TrimSpace(p.HostKeyAlias)
	p.VNCHost = strings.TrimSpace(p.VNCHost)
	if !profileOnly && (server.Name == "" || len([]rune(server.Name)) > 100) {
		return Library{}, errors.New("连接名称应为 1–100 个字符")
	}
	user := p.User
	*p = p.withDefaults()
	p.User = user // An omitted account is requested when connecting.
	if p.Host == "" || p.validateTransport() != nil {
		return Library{}, errors.New("请填写服务器地址和有效的连接设置")
	}
	var newRef, oldRef string
	lib, err := s.update(func(lib *Library) error {
		if server.FolderID != "" && !lib.hasFolder(server.FolderID) {
			return errors.New("分类已被删除，请重新选择")
		}
		for i, existing := range lib.Servers {
			if existing.ID == server.ID && server.ID != "" {
				if profileOnly {
					profile := server.Profile
					server = existing
					server.Profile = profile
				}
				var err error
				newRef, err = s.preparePasswords(&server.Profile, existing.Profile)
				if err != nil {
					return err
				}
				oldRef = existing.Profile.PasswordRef
				server.CreatedAt, server.LastOpened = existing.CreatedAt, existing.LastOpened
				lib.Servers[i] = server
				return nil
			}
		}
		if server.ID != "" {
			return errors.New("连接已被删除，请刷新")
		}
		server.ID, server.CreatedAt = rand.Text(), time.Now().UnixMilli()
		server.LastOpened = 0
		var err error
		newRef, err = s.preparePasswords(&server.Profile, Profile{})
		if err != nil {
			return err
		}
		lib.Servers = append(lib.Servers, server)
		return nil
	})
	if err != nil {
		s.cleanupPassword(newRef)
	} else if oldRef != "" && oldRef != server.Profile.PasswordRef {
		s.cleanupPassword(oldRef)
	}
	return lib, err
}

func (lib *Library) hasFolder(id string) bool {
	for _, f := range lib.Folders {
		if f.ID == id {
			return true
		}
	}
	return false
}

func (s *libraryStore) saveFolder(folder Folder) (Library, error) {
	folder.Name = strings.TrimSpace(folder.Name)
	if folder.Name == "" || len([]rune(folder.Name)) > 50 {
		return Library{}, errors.New("分类名称应为 1–50 个字符")
	}
	return s.update(func(lib *Library) error {
		for _, f := range lib.Folders {
			if f.ID != folder.ID && strings.EqualFold(f.Name, folder.Name) {
				return errors.New("已存在同名分类")
			}
		}
		for i, f := range lib.Folders {
			if f.ID == folder.ID && folder.ID != "" {
				lib.Folders[i] = folder
				return nil
			}
		}
		if folder.ID != "" {
			return errors.New("分类已被删除，请刷新")
		}
		folder.ID = rand.Text()
		lib.Folders = append(lib.Folders, folder)
		return nil
	})
}

func (s *libraryStore) deleteFolder(id string) (Library, error) {
	return s.update(func(lib *Library) error {
		for i, f := range lib.Folders {
			if f.ID == id {
				lib.Folders = append(lib.Folders[:i], lib.Folders[i+1:]...)
				for j := range lib.Servers {
					if lib.Servers[j].FolderID == id {
						lib.Servers[j].FolderID = ""
					}
				}
				return nil
			}
		}
		return errors.New("分类不存在，请刷新")
	})
}

func (s *libraryStore) deleteServer(id string) (Library, error) {
	var ref string
	lib, err := s.update(func(lib *Library) error {
		for i, server := range lib.Servers {
			if server.ID == id {
				ref = server.Profile.PasswordRef
				lib.Servers = append(lib.Servers[:i], lib.Servers[i+1:]...)
				return nil
			}
		}
		return errors.New("连接不存在，请刷新")
	})
	if err == nil {
		s.cleanupPassword(ref)
	}
	return lib, err
}

func (s *libraryStore) favorite(id string, favorite bool) (Library, error) {
	return s.update(func(lib *Library) error {
		for i := range lib.Servers {
			if lib.Servers[i].ID == id {
				lib.Servers[i].Favorite = favorite
				return nil
			}
		}
		return errors.New("连接不存在，请刷新")
	})
}
