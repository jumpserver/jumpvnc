package main

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	wmSetFont         = 0x0030
	wmActivate        = 0x0006
	wmCommand         = 0x0111
	wsCaption         = 0x00c00000
	wsSysMenu         = 0x00080000
	wsMinimizeBox     = 0x00020000
	wsClipChildren    = 0x02000000
	wsChild           = 0x40000000
	wsVisible         = 0x10000000
	wsTabStop         = 0x00010000
	wsGroup           = 0x00020000
	wsExClientEdge    = 0x00000200
	esAutoHScroll     = 0x0080
	esReadOnly        = 0x0800
	esPassword        = 0x0020
	ssNoPrefix        = 0x0080
	bsGroupBox        = 0x0007
	defaultGUIFont    = 17
	colorButtonFace   = 15
	idcArrow          = 32512
	idCancel          = 2
	idPanelStatus     = 1000
	idPanelConfig     = 1001
	idPanelSSH        = 1002
	idPanelVNC        = 1003
	idRootInput       = 1005
	idRootBrowse      = 1006
	idRootSave        = 1007
	idRootHint        = 1008
	idPanelSSHUser    = 1009
	idPanelRunUser    = 1010
	idPasswordCurrent = 1011
	idPasswordShow    = 1012
	idPasswordInput   = 1013
	idPasswordSave    = 1014
	idPasswordHint    = 1015
	enChange          = 0x0300
	swHide            = 0
	swRestore         = 9
	cwUseDefault      = 0x80000000
	managementWidth   = 570
	managementHeight  = 580
)

var (
	gdi32              = windows.NewLazySystemDLL("gdi32.dll")
	getStockObject     = gdi32.NewProc("GetStockObject")
	adjustWindowRectEx = user32.NewProc("AdjustWindowRectEx")
	loadCursor         = user32.NewProc("LoadCursorW")
	showWindow         = user32.NewProc("ShowWindow")
	isWindowVisible    = user32.NewProc("IsWindowVisible")
	enableWindow       = user32.NewProc("EnableWindow")
	setWindowText      = user32.NewProc("SetWindowTextW")
	sendMessage        = user32.NewProc("SendMessageW")
	translateMessage   = user32.NewProc("TranslateMessage")
	isDialogMessage    = user32.NewProc("IsDialogMessageW")
)

type managementWindow struct {
	window, status, start, stop, restart, lastError      uintptr
	admin, configEdit                                    uintptr
	ssh, vnc, sshUser                                    uintptr
	rootLabel, rootInput, rootBrowse, rootSave, rootHint uintptr
	passwordCurrent, passwordShow, passwordInput         uintptr
	passwordSave, passwordHint                           uintptr
	savedRoot                                            string
	passwordFile                                         string
	rootEnabled                                          bool
	passwordRevealed                                     bool
	classRegistered                                      bool
}

func (t *windowsTray) openPanel() {
	if t.panel.window == 0 {
		if err := t.createPanel(); err != nil {
			showTrayError(t.window, "无法打开管理窗口", err)
			return
		}
	}
	t.service.update()
	t.updatePanel()
	t.refreshPanelSettings(true)
	showWindow.Call(t.panel.window, swRestore)
	// The launcher can override the process's first ShowWindow call with SW_HIDE.
	if visible, _, _ := isWindowVisible.Call(t.panel.window); visible == 0 {
		showWindow.Call(t.panel.window, swRestore)
	}
	setForegroundWindow.Call(t.panel.window)
}

func (t *windowsTray) createPanel() error {
	className := windows.StringToUTF16Ptr("JumpVNC.Server.Management")
	if !t.panel.classRegistered {
		cursor, _, _ := loadCursor.Call(0, idcArrow)
		class := windowClass{
			instance: t.instance, className: className, callback: windows.NewCallback(t.panelProc),
			cursor: cursor, brush: colorButtonFace + 1, icon: t.icon, smallIcon: t.icon,
		}
		class.size = uint32(unsafe.Sizeof(class))
		if atom, _, err := registerClass.Call(uintptr(unsafe.Pointer(&class))); atom == 0 {
			return fmt.Errorf("register management window class: %w", err)
		}
		t.panel.classRegistered = true
	}
	const style = wsCaption | wsSysMenu | wsMinimizeBox | wsClipChildren
	rect := struct{ left, top, right, bottom int32 }{right: managementWidth, bottom: managementHeight}
	if ok, _, err := adjustWindowRectEx.Call(uintptr(unsafe.Pointer(&rect)), style, 0, 0); ok == 0 {
		return fmt.Errorf("size management window: %w", err)
	}
	title := windows.StringToUTF16Ptr(productName)
	hwnd, _, err := createWindow.Call(0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(title)), style,
		cwUseDefault, cwUseDefault, uintptr(rect.right-rect.left), uintptr(rect.bottom-rect.top), 0, 0, t.instance, 0)
	if hwnd == 0 {
		return fmt.Errorf("create management window: %w", err)
	}

	controls := []struct {
		class, text         string
		style, extraStyle   uintptr
		x, y, width, height uintptr
		id                  uintptr
		handle              *uintptr
	}{
		{"BUTTON", "服务", bsGroupBox | wsGroup, 0, 20, 14, 530, 96, 0, nil},
		{"STATIC", "", ssNoPrefix, 0, 36, 37, 498, 20, idPanelStatus, &t.panel.status},
		{"BUTTON", "启动服务", wsTabStop, 0, 36, 64, 100, 28, menuStart, &t.panel.start},
		{"BUTTON", "停止服务", wsTabStop, 0, 146, 64, 100, 28, menuStop, &t.panel.stop},
		{"BUTTON", "重启服务", wsTabStop, 0, 256, 64, 100, 28, menuRestart, &t.panel.restart},
		{"BUTTON", "以管理员身份管理", wsTabStop, 0, 366, 64, 168, 28, menuAdmin, &t.panel.admin},
		{"BUTTON", "连接配置", bsGroupBox | wsGroup, 0, 20, 121, 530, 115, 0, nil},
		{"STATIC", "", ssNoPrefix, 0, 36, 144, 498, 18, idPanelSSH, &t.panel.ssh},
		{"STATIC", "", ssNoPrefix, 0, 36, 164, 498, 18, idPanelVNC, &t.panel.vnc},
		{"STATIC", "", ssNoPrefix, 0, 36, 184, 498, 18, idPanelSSHUser, &t.panel.sshUser},
		{"STATIC", "Windows 运行用户：" + t.backendRunUser(), ssNoPrefix, 0, 36, 204, 498, 18, idPanelRunUser, nil},
		{"BUTTON", "SFTP", bsGroupBox | wsGroup, 0, 20, 247, 530, 100, 0, nil},
		{"STATIC", "根目录", ssNoPrefix, 0, 36, 268, 498, 18, 0, &t.panel.rootLabel},
		{"EDIT", "", wsTabStop | esAutoHScroll, wsExClientEdge, 36, 291, 320, 24, idRootInput, &t.panel.rootInput},
		{"BUTTON", "浏览…", wsTabStop, 0, 366, 289, 78, 28, idRootBrowse, &t.panel.rootBrowse},
		{"BUTTON", "保存目录", wsTabStop, 0, 454, 289, 80, 28, idRootSave, &t.panel.rootSave},
		{"STATIC", "", ssNoPrefix, 0, 36, 324, 498, 18, idRootHint, &t.panel.rootHint},
		{"BUTTON", "VNC 密码", bsGroupBox | wsGroup, 0, 20, 358, 530, 109, 0, nil},
		{"STATIC", "当前密码", 0, 0, 36, 382, 80, 18, 0, nil},
		{"EDIT", "", esReadOnly | esPassword, wsExClientEdge, 120, 378, 302, 24, idPasswordCurrent, &t.panel.passwordCurrent},
		{"BUTTON", "显示密码", wsTabStop, 0, 434, 376, 100, 28, idPasswordShow, &t.panel.passwordShow},
		{"STATIC", "新密码", 0, 0, 36, 414, 80, 18, 0, nil},
		{"EDIT", "", wsTabStop | esPassword | esAutoHScroll, wsExClientEdge, 120, 410, 302, 24, idPasswordInput, &t.panel.passwordInput},
		{"BUTTON", "保存密码", wsTabStop, 0, 434, 408, 100, 28, idPasswordSave, &t.panel.passwordSave},
		{"STATIC", "", ssNoPrefix, 0, 36, 442, 498, 18, idPasswordHint, &t.panel.passwordHint},
		{"BUTTON", "配置文件", bsGroupBox | wsGroup, 0, 20, 478, 530, 57, 0, nil},
		{"EDIT", t.configPath, wsTabStop | esAutoHScroll | esReadOnly, wsExClientEdge, 36, 499, 388, 24, idPanelConfig, nil},
		{"BUTTON", "编辑配置", wsTabStop, 0, 434, 497, 100, 28, menuConfig, &t.panel.configEdit},
		{"BUTTON", "查看错误", wsTabStop | wsGroup, 0, 20, 545, 100, 28, menuError, &t.panel.lastError},
		{"BUTTON", "关闭窗口", wsTabStop, 0, 450, 545, 100, 28, idCancel, nil},
	}
	font, _, _ := getStockObject.Call(defaultGUIFont)
	for _, control := range controls {
		class := windows.StringToUTF16Ptr(control.class)
		text := windows.StringToUTF16Ptr(control.text)
		child, _, err := createWindow.Call(control.extraStyle, uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(text)),
			wsChild|wsVisible|control.style, control.x, control.y, control.width, control.height, hwnd, control.id, t.instance, 0)
		if child == 0 {
			destroyWindow.Call(hwnd)
			return fmt.Errorf("create %s control: %w", control.class, err)
		}
		sendMessage.Call(child, wmSetFont, font, 0)
		if control.handle != nil {
			*control.handle = child
		}
	}
	t.panel.window = hwnd
	return nil
}

func (t *windowsTray) updatePanel() {
	if t.panel.window == 0 {
		return
	}
	setControlText(t.panel.status, "状态："+t.status())
	setControlEnabled(t.panel.start, t.canConfigure() && t.service.canStart())
	setControlEnabled(t.panel.stop, t.canConfigure() && t.service.canStop())
	setControlEnabled(t.panel.restart, t.canConfigure() && t.service.canStop())
	setControlEnabled(t.panel.lastError, t.service.currentError() != nil || (t.registration != nil && t.canConfigure()))
	setControlEnabled(t.panel.configEdit, t.canConfigure())
	if t.registration == nil || t.canConfigure() {
		showWindow.Call(t.panel.admin, swHide)
	}
	if t.registration != nil && t.service.currentError() == nil {
		setControlText(t.panel.lastError, "查看日志")
	} else {
		setControlText(t.panel.lastError, "查看错误")
	}
}

func setControlEnabled(control uintptr, enabled bool) {
	var value uintptr
	if enabled {
		value = 1
	}
	enableWindow.Call(control, value)
}

func (t *windowsTray) panelProc(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	switch message {
	case wmClose:
		t.closePanel()
		return 0
	case wmActivate:
		if t.panel.window == hwnd && uint16(wParam) == 0 && t.panel.passwordRevealed {
			t.panel.hidePassword()
		}
	case wmCommand:
		if t.panel.window != hwnd || t.closing {
			return 0
		}
		command, notification := uintptr(uint16(wParam)), uint16(wParam>>16)
		if command == idRootInput && notification == enChange {
			t.panel.updateRootSave()
			return 0
		}
		if command == idPasswordInput && notification == enChange {
			t.panel.updatePasswordSave()
			return 0
		}
		if notification != 0 {
			return 0
		}
		switch command {
		case idCancel:
			t.closePanel()
		case idRootBrowse:
			if t.panel.rootEnabled {
				t.browsePanelRoot()
			}
		case idRootSave:
			t.savePanelRoot()
		case idPasswordShow:
			t.togglePanelPassword()
		case idPasswordSave:
			t.savePanelPassword()
		case menuStart, menuStop, menuRestart, menuConfig, menuError, menuAdmin:
			if lParam != 0 {
				t.handleCommand(command, hwnd)
			}
		}
		return 0
	}
	result, _, _ := defWindowProc.Call(hwnd, uintptr(message), wParam, lParam)
	return result
}

func (t *windowsTray) backendRunUser() string {
	if t.registration != nil {
		return t.registration.runUser + "（系统服务）"
	}
	return windowsRunUser()
}
