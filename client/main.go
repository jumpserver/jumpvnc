package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/egoist/mygo"
)

var transferProgress = mygo.NewEvent[Progress]("transfer-progress")

//go:embed resources/icon.png
var appIcon []byte

type Desktop struct {
	mu      sync.Mutex
	current *session
}

type Connection struct {
	Auth         *AuthChallenge    `json:"auth,omitempty"`
	HostKey      *HostKeyChallenge `json:"hostKey,omitempty"`
	ID           string            `json:"id"`
	URL          string            `json:"url"`
	Password     string            `json:"password,omitempty"`
	Files        bool              `json:"files"`
	FileLocation *FileLocation     `json:"fileLocation,omitempty"`
}

func (*Desktop) Defaults() Profile { return defaultProfile() }

func (d *Desktop) Connect(p Profile) (Connection, error) {
	p = p.withDefaults()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.current != nil && d.current.ctx.Err() == nil {
		return Connection{}, errors.New("已有连接，请先断开")
	}
	password, err := p.password()
	if err != nil {
		return Connection{}, err
	}
	s, url, err := openSession(p)
	if err != nil {
		var auth *AuthChallenge
		if errors.As(err, &auth) {
			return Connection{Auth: auth}, nil
		}
		if p.Transport == "forward" && strings.Contains(err.Error(), "unable to authenticate") {
			return Connection{Auth: &AuthChallenge{Fields: []string{"user", "sshPassword"}, Message: "SSH 认证失败，请填写账号和密码"}}, nil
		}
		var challenge *HostKeyChallenge
		if errors.As(err, &challenge) {
			return Connection{HostKey: challenge}, nil
		}
		return Connection{}, err
	}
	d.current = s
	return Connection{ID: s.id, URL: url, Password: password, Files: s.files != nil, FileLocation: s.fileLocation}, nil
}

func (d *Desktop) Disconnect() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.current != nil {
		d.current.close()
		d.current = nil
	}
}

func (d *Desktop) active() (*session, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.current == nil || d.current.ctx.Err() != nil {
		return nil, errors.New("Agent 已断开")
	}
	return d.current, nil
}

func (d *Desktop) List(directory string) ([]FileEntry, error) {
	s, err := d.fileSession()
	if err != nil {
		return nil, err
	}
	return s.list(directory)
}

func (d *Desktop) Mkdir(directory, name string) error {
	s, err := d.fileSession()
	if err != nil {
		return err
	}
	if _, err := remotePath(directory); err != nil {
		return err
	}
	if err := fileName(name); err != nil {
		return err
	}
	return s.files.Mkdir(path.Join(directory, name))
}

func (d *Desktop) UploadDialog(ctx context.Context, directory string) (int, error) {
	s, err := d.fileSession()
	if err != nil {
		return 0, err
	}
	files, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent: mygo.CallerWindow(ctx), Title: "选择上传文件", Multiple: true,
	})
	if err != nil || len(files) == 0 {
		return 0, err
	}
	return uploadFiles(s, mygo.CallerWindow(ctx), files, directory)
}

func (d *Desktop) UploadDrop(ctx context.Context, files []string, directory string) (int, error) {
	s, err := d.fileSession()
	if err != nil {
		return 0, err
	}
	return uploadFiles(s, mygo.CallerWindow(ctx), files, directory)
}

func uploadFiles(s *session, win *mygo.Window, files []string, directory string) (int, error) {
	if _, err := remotePath(directory); err != nil {
		return 0, err
	}
	if !s.busy.CompareAndSwap(false, true) {
		return 0, errors.New("另一个传输正在进行")
	}
	defer s.busy.Store(false)
	for i, file := range files {
		err := s.upload(file, directory, func(p Progress) { transferProgress.Emit(win, p) })
		if err != nil {
			return i, fmt.Errorf("已上传 %d 项，%s: %w", i, path.Base(file), err)
		}
	}
	return len(files), nil
}

func (d *Desktop) Download(ctx context.Context, remote string) (bool, error) {
	s, err := d.fileSession()
	if err != nil {
		return false, err
	}
	if _, err := remotePath(remote); err != nil || remote == "/" {
		return false, errors.New("请选择要下载的文件")
	}
	win := mygo.CallerWindow(ctx)
	local, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
		Parent: win, Title: "保存远程文件", DefaultPath: path.Base(remote), CreateDirectories: true,
	})
	if err != nil || local == "" {
		return false, err
	}
	if !s.busy.CompareAndSwap(false, true) {
		return false, errors.New("另一个传输正在进行")
	}
	defer s.busy.Store(false)
	err = s.download(remote, local, func(p Progress) { transferProgress.Emit(win, p) })
	return err == nil, err
}

func (d *Desktop) fileSession() (*session, error) {
	s, err := d.active()
	if err != nil {
		return nil, err
	}
	if s.files == nil {
		return nil, errors.New("文件传输不可用，桌面连接不受影响")
	}
	return s, nil
}

func main() {
	mygo.Theme.SetSource(mygo.ThemeLight)
	// Keep connection profiles and window state across the JumpVNC display rename.
	dataDir, err := mygo.App.Path(mygo.PathAppData)
	if err != nil {
		log.Fatal(err)
	}
	mygo.App.SetPath(mygo.PathUserData, filepath.Join(dataDir, "SSH VNC"))
	if !mygo.App.RequestSingleInstanceLock() {
		return
	}
	viewer := &Viewer{windows: make(map[int]*viewerWindow)}
	manager := &Manager{viewer: viewer}
	mygo.Bind(manager)
	mygo.Bind(viewer)
	mygo.App.OnOpenFile(manager.queueImport)
	mygo.App.OnSecondInstance(func(_ []string, _ string) { mygo.App.WhenReady(manager.activateWindow) })
	mygo.App.OnActivate(func(_ bool) {
		if manager.store != nil && (manager.window == nil || manager.window.IsDestroyed()) {
			manager.showWindow()
		}
	})
	mygo.App.WhenReady(func() {
		// Also set the running icon so an older Launch Services cache cannot
		// leave the Dock showing the placeholder after an in-place rebuild.
		if err := mygo.App.Dock.SetIcon(appIcon); err != nil {
			log.Printf("设置 Dock 图标: %v", err)
		}
		dir, err := mygo.App.Path(mygo.PathUserData)
		if err != nil {
			log.Fatal(err)
		}
		manager.store = newLibraryStore(dir)
		manager.showWindow()
	})
	err = mygo.App.Run()
	viewer.closeAll()
	if err != nil {
		log.Fatal(err)
	}
}
