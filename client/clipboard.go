package main

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
)

const clipboardTextLimit = 1024 * 1024

type ClipboardText struct {
	Focused bool   `json:"focused"`
	HasText bool   `json:"hasText"`
	Text    string `json:"text"`
}

// Keep native clipboard access scoped to the current connection and foreground
// viewer. Background desktops must not exchange another window's clipboard.
func (v *Viewer) clipboardCaller(ctx context.Context, id string) (*mygo.Window, error) {
	state, err := v.caller(ctx)
	if err != nil {
		return nil, err
	}
	s, err := state.desktop.active()
	if err != nil {
		return nil, err
	}
	if s.id != id {
		return nil, errors.New("剪贴板连接已过期")
	}
	return mygo.CallerWindow(ctx), nil
}

func validClipboardText(text string) error {
	if len(text) > clipboardTextLimit {
		return errors.New("剪贴板文本超过 1 MiB")
	}
	if !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return errors.New("剪贴板包含无效文本")
	}
	return nil
}

func (v *Viewer) ReadClipboard(ctx context.Context, id string) (ClipboardText, error) {
	var result ClipboardText
	var err error
	mygo.RunOnMain(func() {
		var win *mygo.Window
		win, err = v.clipboardCaller(ctx, id)
		if err != nil || !win.IsFocused() {
			return
		}
		result.Focused = true
		var data []byte
		data, err = mygo.Clipboard.ReadFormat(transfer.Text)
		if errors.Is(err, transfer.ErrFormat) {
			err = nil // Files/images are not an empty text clipboard.
			return
		}
		if err == nil {
			result.Text = string(data)
			err = validClipboardText(result.Text)
			result.HasText = err == nil
		}
	})
	return result, err
}

func (v *Viewer) WriteClipboard(ctx context.Context, id, text string) (bool, error) {
	if err := validClipboardText(text); err != nil {
		return false, err
	}
	var written bool
	var err error
	mygo.RunOnMain(func() {
		var win *mygo.Window
		win, err = v.clipboardCaller(ctx, id)
		if err != nil || !win.IsFocused() {
			return
		}
		err = mygo.Clipboard.Write(transfer.TextData(text))
		written = err == nil
	})
	return written, err
}
