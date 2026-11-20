package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/egoist/mygo"
)

var importRequested = mygo.NewEvent[bool]("svnc-import-requested")

// Called by the native event on the UI thread. Files stay queued until the
// manager page is ready, including files received during a cold launch.
func (m *Manager) queueImport(filename string) {
	m.importsMu.Lock()
	m.pendingImports = append(m.pendingImports, filename)
	m.importsMu.Unlock()
	mygo.App.WhenReady(func() {
		m.activateWindow()
		importRequested.Emit(m.window, true)
	})
}

func (m *Manager) activateWindow() {
	if m.window == nil || m.window.IsDestroyed() {
		m.showWindow()
	}
	m.window.Show()
	m.window.Focus()
}

func (m *Manager) ImportPending() (ImportResult, error) {
	m.importsMu.Lock()
	paths := m.pendingImports
	m.pendingImports = nil
	m.importsMu.Unlock()
	return m.store.importConnectionFiles(paths)
}

func (m *Manager) ImportDialog(ctx context.Context) (ImportResult, error) {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent: mygo.CallerWindow(ctx), Title: "导入 SuperVNC 连接", Multiple: true,
		Filters: []mygo.FileFilter{{Name: "SuperVNC 连接", Extensions: []string{"svnc"}}},
	})
	if err != nil {
		return ImportResult{}, err
	}
	return m.store.importConnectionFiles(paths)
}

func (m *Manager) ExportServer(ctx context.Context, id string) (bool, error) {
	lib, err := m.store.read()
	if err != nil {
		return false, err
	}
	for _, server := range lib.Servers {
		if server.ID != id {
			continue
		}
		data, err := encodeConnectionFile(server)
		if err != nil {
			return false, err
		}
		name := strings.Map(func(r rune) rune {
			if unicode.IsControl(r) || strings.ContainsRune(`/\:*?"<>|`, r) {
				return '-'
			}
			return r
		}, server.Name)
		filename, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
			Parent: mygo.CallerWindow(ctx), Title: "导出 SuperVNC 连接", DefaultPath: name + ".svnc",
			Filters: []mygo.FileFilter{{Name: "SuperVNC 连接", Extensions: []string{"svnc"}}},
		})
		if err != nil || filename == "" {
			return false, err
		}
		if !strings.EqualFold(filepath.Ext(filename), ".svnc") {
			return false, errors.New("导出文件请使用 .svnc 扩展名")
		}
		err = writeConnectionFile(filename, data)
		return err == nil, err
	}
	return false, errors.New("连接已被删除，请刷新")
}
