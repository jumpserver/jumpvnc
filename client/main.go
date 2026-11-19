package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path"
	"sync"

	"github.com/egoist/mygo"
)

var transferProgress = mygo.NewEvent[Progress]("transfer-progress")

type Desktop struct {
	mu      sync.Mutex
	current *session
}

type Connection struct {
	URL      string `json:"url"`
	Password string `json:"password,omitempty"`
}

func (*Desktop) Defaults() Profile { return defaultProfile() }

func (d *Desktop) Connect(p Profile) (Connection, error) {
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
		return Connection{}, err
	}
	d.current = s
	return Connection{URL: url, Password: password}, nil
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
	s, err := d.active()
	if err != nil {
		return nil, err
	}
	return s.list(directory)
}

func (d *Desktop) Mkdir(directory, name string) error {
	s, err := d.active()
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
	s, err := d.active()
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
	s, err := d.active()
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
	s, err := d.active()
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

func main() {
	desktop := &Desktop{}
	mygo.Bind(desktop)
	mygo.App.WhenReady(func() {
		win := mygo.NewWindow(mygo.WindowOptions{
			Title: "SSH VNC", URL: "/", Width: 1240, Height: 790,
			MinWidth: 900, MinHeight: 580, BackgroundColor: "#111820",
		})
		win.OnClosed(func() { mygo.App.Quit() })
	})
	err := mygo.App.Run()
	desktop.Disconnect()
	if err != nil {
		log.Fatal(err)
	}
}
