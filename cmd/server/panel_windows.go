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
	idSSHListenInput  = 1016
	idSSHUserInput    = 1017
	idSSHSave         = 1018
	idAuthorizedKeys  = 1019
	idKeysSave        = 1020
	idKeyCopy         = 1021
	idPublicCopy      = 1022
	idConnectionCopy  = 1023
	idSFTPMode        = 1024
	esMultiline       = 0x0004
	esWantReturn      = 0x1000
	wsVScroll         = 0x00200000
	cbsDropDownList   = 0x0003
	cbAddString       = 0x0143
	cbGetCurSel       = 0x0147
	cbSetCurSel       = 0x014e
	enChange          = 0x0300
	swHide            = 0
	swRestore         = 9
	cwUseDefault      = 0x80000000
	managementWidth   = 620
	managementHeight  = 710
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
	window, status, start, stop, restart, lastError         uintptr
	admin, configEdit                                       uintptr
	sshListenInput, sshUserInput, sshSave, fingerprint      uintptr
	authorizedKeys, keysSave, keyCopy, publicCopy, keysHint uintptr
	connectionCopy, sftpMode                                uintptr
	authorizedFile, savedMode                               string
	rootLabel, rootInput, rootBrowse, rootSave, rootHint    uintptr
	passwordCurrent, passwordShow, passwordInput            uintptr
	passwordSave, passwordHint                              uintptr
	savedRoot                                               string
	passwordFile                                            string
	rootEnabled                                             bool
	passwordRevealed                                        bool
	classRegistered                                         bool
	remote                                                  remotePage
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
		{"BUTTON", "服务", bsGroupBox | wsGroup, 0, 20, 14, 580, 96, 0, nil},
		{"STATIC", "", ssNoPrefix, 0, 36, 37, 548, 20, idPanelStatus, &t.panel.status},
		{"BUTTON", "启动服务", wsTabStop, 0, 36, 64, 100, 28, menuStart, &t.panel.start},
		{"BUTTON", "停止服务", wsTabStop, 0, 146, 64, 100, 28, menuStop, &t.panel.stop},
		{"BUTTON", "重启服务", wsTabStop, 0, 256, 64, 100, 28, menuRestart, &t.panel.restart},
		{"BUTTON", "以管理员身份管理", wsTabStop, 0, 406, 64, 178, 28, menuAdmin, &t.panel.admin},
		{"BUTTON", "连接设置", bsGroupBox | wsGroup, 0, 20, 120, 580, 108, 0, nil},
		{"STATIC", "SSH 监听", 0, 0, 36, 146, 70, 18, 0, nil},
		{"EDIT", "", wsTabStop | esAutoHScroll, wsExClientEdge, 108, 140, 228, 24, idSSHListenInput, &t.panel.sshListenInput},
		{"STATIC", "用户名", 0, 0, 350, 146, 60, 18, 0, nil},
		{"EDIT", "", wsTabStop | esAutoHScroll, wsExClientEdge, 410, 140, 174, 24, idSSHUserInput, &t.panel.sshUserInput},
		{"STATIC", "", ssNoPrefix, 0, 36, 179, 430, 20, 0, &t.panel.fingerprint},
		{"BUTTON", "保存并应用", wsTabStop, 0, 476, 173, 108, 28, idSSHSave, &t.panel.sshSave},
		{"STATIC", "运行账户：" + t.backendRunUser(), ssNoPrefix, 0, 36, 204, 548, 18, idPanelRunUser, nil},
		{"BUTTON", "SSH 授权公钥", bsGroupBox | wsGroup, 0, 20, 238, 580, 164, 0, nil},
		{"EDIT", "", wsTabStop | esMultiline | esWantReturn | wsVScroll, wsExClientEdge, 36, 262, 548, 72, idAuthorizedKeys, &t.panel.authorizedKeys},
		{"BUTTON", "复制连接私钥", wsTabStop, 0, 36, 344, 128, 28, idKeyCopy, &t.panel.keyCopy},
		{"BUTTON", "复制授权公钥", wsTabStop, 0, 174, 344, 128, 28, idPublicCopy, &t.panel.publicCopy},
		{"BUTTON", "保存并应用", wsTabStop, 0, 476, 344, 108, 28, idKeysSave, &t.panel.keysSave},
		{"STATIC", "", ssNoPrefix, 0, 36, 378, 548, 18, 0, &t.panel.keysHint},
		{"BUTTON", "文件传输", bsGroupBox | wsGroup, 0, 20, 412, 580, 106, 0, nil},
		{"STATIC", "访问模式", 0, 0, 36, 438, 70, 18, 0, &t.panel.rootLabel},
		{"COMBOBOX", "", wsTabStop | cbsDropDownList | wsVScroll, 0, 108, 432, 160, 180, idSFTPMode, &t.panel.sftpMode},
		{"EDIT", "", wsTabStop | esAutoHScroll, wsExClientEdge, 36, 464, 350, 24, idRootInput, &t.panel.rootInput},
		{"BUTTON", "浏览…", wsTabStop, 0, 396, 462, 70, 28, idRootBrowse, &t.panel.rootBrowse},
		{"BUTTON", "保存并应用", wsTabStop, 0, 476, 462, 108, 28, idRootSave, &t.panel.rootSave},
		{"STATIC", "", ssNoPrefix, 0, 36, 495, 548, 18, idRootHint, &t.panel.rootHint},
		{"BUTTON", "VNC 密码", bsGroupBox | wsGroup, 0, 20, 530, 580, 110, 0, nil},
		{"STATIC", "当前密码", 0, 0, 36, 554, 80, 18, 0, nil},
		{"EDIT", "", esReadOnly | esPassword, wsExClientEdge, 120, 550, 346, 24, idPasswordCurrent, &t.panel.passwordCurrent},
		{"BUTTON", "显示密码", wsTabStop, 0, 476, 548, 108, 28, idPasswordShow, &t.panel.passwordShow},
		{"STATIC", "新密码", 0, 0, 36, 586, 80, 18, 0, nil},
		{"EDIT", "", wsTabStop | esPassword | esAutoHScroll, wsExClientEdge, 120, 582, 346, 24, idPasswordInput, &t.panel.passwordInput},
		{"BUTTON", "保存并应用", wsTabStop, 0, 476, 580, 108, 28, idPasswordSave, &t.panel.passwordSave},
		{"STATIC", "", ssNoPrefix, 0, 36, 615, 548, 18, idPasswordHint, &t.panel.passwordHint},
		{"BUTTON", "高级配置", wsTabStop, 0, 20, 660, 100, 28, menuConfig, &t.panel.configEdit},
		{"BUTTON", "复制连接信息", wsTabStop, 0, 130, 660, 136, 28, idConnectionCopy, &t.panel.connectionCopy},
		{"BUTTON", "查看日志", wsTabStop, 0, 276, 660, 100, 28, menuError, &t.panel.lastError},
		{"BUTTON", "远程连接", wsTabStop, 0, 386, 660, 100, 28, menuConnections, nil},
		{"BUTTON", "关闭窗口", wsTabStop, 0, 500, 660, 100, 28, idCancel, nil},
		{"STATIC", "", ssNoPrefix, 0, 20, 24, 580, 52, idRemoteTitle, &t.panel.remote.title},
		{"STATIC", "", ssNoPrefix, 0, 20, 76, 580, 42, idRemoteDetail, &t.panel.remote.detail},
		{"STATIC", "桌面连接", ssNoPrefix, 0, 22, 149, 240, 24, idRemoteLabel, nil},
		{"BUTTON", "断开全部", wsTabStop, 0, 480, 140, 120, 32, idRemoteAll, &t.panel.remote.all},
		{"LISTBOX", "", wsTabStop | wsVScroll | 1 | 0x0100, wsExClientEdge, 20, 184, 580, 345, idRemoteList, &t.panel.remote.list},
		{"STATIC", "", ssNoPrefix, 0, 20, 545, 426, 52, idRemoteSelected, &t.panel.remote.info},
		{"BUTTON", "断开所选连接", wsTabStop, 0, 458, 539, 142, 32, idRemoteDisconnect, &t.panel.remote.disconnect},
		{"BUTTON", "返回设置", wsTabStop, 0, 20, 660, 110, 28, idRemoteBack, nil},
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
		if control.id >= idRemoteTitle && control.id <= idRemoteBack {
			t.panel.remote.controls = append(t.panel.remote.controls, child)
			showWindow.Call(child, swHide)
		} else {
			t.panel.remote.settings = append(t.panel.remote.settings, child)
		}
		if control.id == idRemoteTitle {
			family, _ := windows.UTF16PtrFromString("Segoe UI")
			titleFont, _, _ := gdi32.NewProc("CreateFontW").Call(28, 0, 0, 0, 600, 0, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(family)))
			if titleFont != 0 {
				t.panel.remote.font = titleFont
				sendMessage.Call(child, wmSetFont, titleFont, 0)
			}
		}
		if control.handle != nil {
			*control.handle = child
		}
	}
	for _, label := range []string{"关闭", "读写", "只读", "只写"} {
		text := windows.StringToUTF16Ptr(label)
		sendMessage.Call(t.panel.sftpMode, cbAddString, 0, uintptr(unsafe.Pointer(text)))
	}
	sendMessage.Call(t.panel.authorizedKeys, 0x00c5, 1<<20, 0) // EM_SETLIMITTEXT
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
	for _, control := range []uintptr{t.panel.sshSave, t.panel.keysSave, t.panel.rootSave, t.panel.passwordSave} {
		setControlEnabled(control, t.canApplyPanelSettings())
	}
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
	case wmTimer:
		if wParam == remoteTimer && t.panel.remote.visible {
			t.refreshRemote(nil)
			return 0
		}
	case 0x0138: // WM_CTLCOLORSTATIC
		if lParam != 0 && (lParam == t.panel.remote.title || lParam == t.panel.remote.detail) {
			const blue = 0xbb651a
			gdi32.NewProc("SetTextColor").Call(wParam, 0xffffff)
			gdi32.NewProc("SetBkColor").Call(wParam, blue)
			gdi32.NewProc("SetDCBrushColor").Call(wParam, blue)
			brush, _, _ := getStockObject.Call(18)
			return brush
		}
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
		if command == idSFTPMode && notification == 1 {
			t.panel.updateRootSave()
			return 0
		}
		if t.panel.remote.visible && command == idRemoteList && notification == 1 {
			t.updateRemoteSelection()
			return 0
		}
		if notification != 0 {
			return 0
		}
		switch command {
		case menuConnections:
			t.showConnections(true)
		case idRemoteBack:
			t.showConnections(false)
		case idRemoteDisconnect, idRemoteAll:
			if t.panel.remote.visible {
				t.disconnectRemote(command == idRemoteAll)
			}
		case idCancel:
			t.closePanel()
		case idRootBrowse:
			if t.panel.rootEnabled {
				t.browsePanelRoot()
			}
		case idSSHSave:
			t.savePanelSSH()
		case idKeysSave:
			t.savePanelKeys()
		case idKeyCopy, idPublicCopy, idConnectionCopy:
			t.copyPanelSettings(command)
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
