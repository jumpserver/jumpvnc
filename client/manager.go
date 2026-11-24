package main

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/egoist/mygo"
)

type Manager struct {
	store          *libraryStore
	viewer         *Viewer
	window         *mygo.Window // Only accessed on the UI thread.
	importsMu      sync.Mutex
	pendingImports []string
}

func (m *Manager) showWindow() {
	m.window = mygo.NewWindow(mygo.WindowOptions{
		Title: "JumpVNC · 连接管理", URL: "/", Width: 1180, Height: 780,
		MinWidth: 880, MinHeight: 580,
		TitleBarStyle: mygo.TitleBarHidden, TitleBarHeight: 48,
		TrafficLightPosition: &mygo.Point{X: 18, Y: 17},
		Transparent:          true, Vibrancy: mygo.VibrancySidebar,
		StateKey: "connection-manager",
	})
}

func (m *Manager) Library() (Library, error)               { return m.store.read() }
func (*Manager) Defaults() Profile                         { return defaultProfile() }
func (m *Manager) SaveServer(s Server) (Library, error)    { return m.store.saveServer(s) }
func (m *Manager) DeleteServer(id string) (Library, error) { return m.store.deleteServer(id) }
func (m *Manager) SaveFolder(f Folder) (Library, error)    { return m.store.saveFolder(f) }
func (m *Manager) DeleteFolder(id string) (Library, error) { return m.store.deleteFolder(id) }
func (m *Manager) Favorite(id string, favorite bool) (Library, error) {
	return m.store.favorite(id, favorite)
}

func (m *Manager) OpenServer(id string) error {
	var server Server
	_, err := m.store.update(func(lib *Library) error {
		for i := range lib.Servers {
			if lib.Servers[i].ID == id {
				lib.Servers[i].LastOpened = time.Now().UnixMilli()
				server = lib.Servers[i]
				return nil
			}
		}
		return errors.New("连接已被删除，请刷新")
	})
	if err != nil {
		return err
	}
	// Register before loading the page: its first bridge call must find its own
	// Desktop. Do not parent the window, so closing the library keeps it alive.
	win := mygo.NewWindow(mygo.WindowOptions{
		Title: server.Name, Width: 1240, Height: 790, MinWidth: 900, MinHeight: 580,
		BackgroundColor: "#e5e7eb", Hidden: true,
		TitleBarStyle: mygo.TitleBarHidden, TitleBarHeight: 36,
		TrafficLightPosition: &mygo.Point{X: 14, Y: 11},
	})
	state := &viewerWindow{desktop: &Desktop{}, server: server, store: m.store}
	m.viewer.mu.Lock()
	m.viewer.windows[win.ID()] = state
	m.viewer.mu.Unlock()
	win.OnClosed(func() {
		m.viewer.mu.Lock()
		delete(m.viewer.windows, win.ID())
		m.viewer.mu.Unlock()
		// A connect may still be dialing. Remove routing first and close the
		// session off the UI thread; a late connect is also closed below.
		go state.desktop.Disconnect()
	})
	win.OnReadyToShow(func() { win.Show() })
	if err := win.Page().LoadURL("/viewer.html"); err != nil {
		win.Close()
		return err
	}
	return nil
}

type viewerWindow struct {
	desktop *Desktop
	server  Server
	store   *libraryStore
}

// Viewer routes every desktop operation by the calling native window. Each
// window owns its SSH, noVNC bridge, files, and performance state independently.
type Viewer struct {
	mu      sync.Mutex
	windows map[int]*viewerWindow
}

func (v *Viewer) caller(ctx context.Context) (*viewerWindow, error) {
	win := mygo.CallerWindow(ctx)
	if win == nil || win.IsDestroyed() {
		return nil, errors.New("桌面窗口已关闭")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	state := v.windows[win.ID()]
	if state == nil {
		return nil, errors.New("当前窗口不是桌面连接")
	}
	return state, nil
}

func (v *Viewer) Defaults(ctx context.Context) (Server, error) {
	state, err := v.caller(ctx)
	if err != nil {
		return Server{}, err
	}
	lib, err := state.store.read()
	if err != nil {
		return Server{}, err
	}
	for _, server := range lib.Servers {
		if server.ID == state.server.ID {
			return server, nil
		}
	}
	return Server{}, errors.New("连接已被删除，请刷新")
}

func (v *Viewer) SaveProfile(ctx context.Context, p Profile) (Server, error) {
	state, err := v.caller(ctx)
	if err != nil {
		return Server{}, err
	}
	return state.store.saveConnectionProfile(state.server.ID, p)
}

func (v *Viewer) Connect(ctx context.Context, p Profile) (Connection, error) {
	state, err := v.caller(ctx)
	if err != nil {
		return Connection{}, err
	}
	wantedPassword, wantedSSH := p.StoredPassword, p.StoredSSHPassword
	p, err = state.store.resolvePasswords(state.server.ID, p)
	if err != nil {
		return Connection{}, err
	}
	var missing []string
	if wantedPassword && !p.StoredPassword && p.VNCPassword == "" {
		missing = append(missing, "vncPassword")
	}
	if wantedSSH && !p.StoredSSHPassword && p.SSHPassword == "" {
		missing = append(missing, "sshPassword")
	}
	if len(missing) != 0 {
		return Connection{Auth: &AuthChallenge{Fields: missing, Message: "未找到保存的密码，请重新填写"}}, nil
	}
	connection, err := state.desktop.Connect(p)
	if _, closed := v.caller(ctx); closed != nil {
		state.desktop.Disconnect()
		return Connection{}, closed
	}
	return connection, err
}

func (v *Viewer) Disconnect(ctx context.Context) error {
	state, err := v.caller(ctx)
	if err != nil {
		return err
	}
	state.desktop.Disconnect()
	return nil
}
func (v *Viewer) Performance(ctx context.Context, id string) (Performance, error) {
	state, err := v.caller(ctx)
	if err != nil {
		return Performance{}, err
	}
	return state.desktop.Performance(id)
}
func (v *Viewer) List(ctx context.Context, dir string) ([]FileEntry, error) {
	state, err := v.caller(ctx)
	if err != nil {
		return nil, err
	}
	return state.desktop.List(dir)
}
func (v *Viewer) Mkdir(ctx context.Context, dir, name string) error {
	state, err := v.caller(ctx)
	if err != nil {
		return err
	}
	return state.desktop.Mkdir(dir, name)
}
func (v *Viewer) UploadDialog(ctx context.Context, dir string) (int, error) {
	state, err := v.caller(ctx)
	if err != nil {
		return 0, err
	}
	return state.desktop.UploadDialog(ctx, dir)
}
func (v *Viewer) UploadDrop(ctx context.Context, paths []string, dir string) (int, error) {
	state, err := v.caller(ctx)
	if err != nil {
		return 0, err
	}
	return state.desktop.UploadDrop(ctx, paths, dir)
}
func (v *Viewer) Download(ctx context.Context, path string) (bool, error) {
	state, err := v.caller(ctx)
	if err != nil {
		return false, err
	}
	return state.desktop.Download(ctx, path)
}
func (v *Viewer) closeAll() {
	v.mu.Lock()
	states := make([]*viewerWindow, 0, len(v.windows))
	for _, state := range v.windows {
		states = append(states, state)
	}
	v.windows = make(map[int]*viewerWindow)
	v.mu.Unlock()
	for _, state := range states {
		state.desktop.Disconnect()
	}
}
