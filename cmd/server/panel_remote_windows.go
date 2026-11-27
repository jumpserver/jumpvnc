package main

import (
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"jumpvnc/internal/sshgateway"
)

const (
	idRemoteTitle = 1100 + iota
	idRemoteDetail
	idRemoteLabel
	idRemoteAll
	idRemoteList
	idRemoteSelected
	idRemoteDisconnect
	idRemoteBack
)
const remoteTimer = 3

type remotePage struct {
	controls, settings                               []uintptr
	title, detail, list, info, disconnect, all, font uintptr
	visible                                          bool
	rows                                             []sshgateway.RemoteConnection
}

func (t *windowsTray) showConnections(show bool) {
	p := &t.panel.remote
	if t.panel.window == 0 || p.visible == show {
		return
	}
	p.visible = show
	hide, display := p.settings, p.controls
	if !show {
		hide, display = display, hide
	}
	for _, h := range hide {
		showWindow.Call(h, swHide)
	}
	for _, h := range display {
		showWindow.Call(h, 5)
	}
	if show {
		t.panel.hidePassword()
		setControlText(t.panel.passwordInput, "")
		setControlText(t.panel.window, productName+" · 远程连接")
		t.refreshRemote(nil)
		setTimer.Call(t.panel.window, remoteTimer, 1000, 0)
	} else {
		killTimer.Call(t.panel.window, remoteTimer)
		setControlText(t.panel.window, productName)
		t.updatePanel()
	}
}

func elapsed(start time.Time) string {
	seconds := max(0, int(time.Since(start).Seconds()))
	return fmt.Sprintf("%02d:%02d:%02d", seconds/3600, seconds/60%60, seconds%60)
}

func remoteRow(row sshgateway.RemoteConnection) string {
	mode := "桌面通道"
	if row.Authenticated {
		mode = "远程控制"
		if row.ViewOnly {
			mode = "仅查看"
		}
	}
	peer := row.Address
	if row.User != "" {
		peer = row.User + " @ " + peer
	}
	return fmt.Sprintf("  %s     ·     %s     ·     %s 接入", peer, mode, row.Started.Local().Format("15:04:05"))
}

func (t *windowsTray) selectedRemote() int {
	p := &t.panel.remote
	sel, _, _ := sendMessage.Call(p.list, 0x0188, 0, 0) // LB_GETCURSEL
	if sel >= uintptr(len(p.rows)) {
		return -1
	}
	return int(sel)
}

func (t *windowsTray) updateRemoteSelection() {
	p := &t.panel.remote
	i := t.selectedRemote()
	setControlEnabled(p.disconnect, i >= 0)
	info := ""
	if i >= 0 {
		info = "连接时长   " + elapsed(p.rows[i].Started)
	}
	setControlText(p.info, info)
}

func (t *windowsTray) refreshRemote(ids []string) {
	p := &t.panel.remote
	rows, err := t.remoteConnections(ids)
	if err != nil {
		rows = nil
	}
	selection := ""
	if i := t.selectedRemote(); i >= 0 {
		selection = p.rows[i].ID
	}
	changed := len(rows) != len(p.rows)
	if !changed {
		for i, r := range rows {
			if r.ID != p.rows[i].ID || r.Authenticated != p.rows[i].Authenticated {
				changed = true
				break
			}
		}
	}
	if changed {
		sendMessage.Call(p.list, 0x0184, 0, 0) // LB_RESETCONTENT
		for i, r := range rows {
			value := windows.StringToUTF16Ptr(remoteRow(r))
			sendMessage.Call(p.list, 0x0180, 0, uintptr(unsafe.Pointer(value)))
			if r.ID == selection || (selection == "" && i == 0) {
				sendMessage.Call(p.list, 0x0186, uintptr(i), 0)
			}
		}
	}
	p.rows = rows
	title, detail := "暂无远程连接", "当前没有桌面连接"
	if err != nil {
		title = "连接状态不可用"
		detail = err.Error()
	} else if len(rows) > 0 {
		title = "本机已连接"
		detail = fmt.Sprintf("%d 个桌面连接     ·     最早连接时长 %s", len(rows), elapsed(rows[0].Started))
		for _, r := range rows {
			if r.Authenticated && !r.ViewOnly {
				title = "本机被控中"
				break
			}
		}
	}
	setControlText(p.title, "   "+title)
	setControlText(p.detail, "   "+detail)
	setControlEnabled(p.all, len(rows) > 0)
	t.updateRemoteSelection()
}

func (t *windowsTray) disconnectRemote(all bool) {
	p := &t.panel.remote
	var ids []string
	if all {
		for _, row := range p.rows {
			ids = append(ids, row.ID)
		}
	} else if i := t.selectedRemote(); i >= 0 {
		ids = []string{p.rows[i].ID}
	}
	if len(ids) > 0 {
		t.refreshRemote(ids)
	}
}
