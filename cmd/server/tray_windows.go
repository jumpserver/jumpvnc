package main

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	wmClose           = 0x0010
	wmEndSession      = 0x0016
	wmContextMenu     = 0x007b
	wmTimer           = 0x0113
	wmLeftDoubleClick = 0x0203
	wmRightButtonUp   = 0x0205
	wmTrayIcon        = 0x8001
	wmServiceUpdate   = 0x8002
	ninKeySelect      = 0x0401
	uoiFlags          = 1
	wsfVisible        = 1
	msgfltAllow       = 1
	nimAdd            = 0
	nimModify         = 1
	nimDelete         = 2
	nimSetFocus       = 3
	nimSetVersion     = 4
	nifMessage        = 0x01
	nifIcon           = 0x02
	nifTip            = 0x04
	nifShowTip        = 0x80
	notifyVersion4    = 4
	idiApplication    = 32512
	siidDesktopPC     = 94
	shgsiIcon         = 0x100
	shgsiSmallIcon    = 0x001
	mfGrayed          = 0x0001
	mfSeparator       = 0x0800
	tpmRightButton    = 0x0002
	tpmReturnCommand  = 0x0100
	trayIconID        = 1
	iconRetryTimer    = 1
)

const (
	menuOpen = iota + 100
	menuStart
	menuStop
	menuRestart
	menuConfig
	menuError
	menuQuit
)

var (
	user32  = windows.NewLazySystemDLL("user32.dll")
	shell32 = windows.NewLazySystemDLL("shell32.dll")

	getProcessWindowStation   = user32.NewProc("GetProcessWindowStation")
	getUserObjectInformation  = user32.NewProc("GetUserObjectInformationW")
	registerWindowMessage     = user32.NewProc("RegisterWindowMessageW")
	registerClass             = user32.NewProc("RegisterClassExW")
	createWindow              = user32.NewProc("CreateWindowExW")
	destroyWindow             = user32.NewProc("DestroyWindow")
	defWindowProc             = user32.NewProc("DefWindowProcW")
	changeWindowMessageFilter = user32.NewProc("ChangeWindowMessageFilterEx")
	getMessage                = user32.NewProc("GetMessageW")
	dispatchMessage           = user32.NewProc("DispatchMessageW")
	postMessage               = user32.NewProc("PostMessageW")
	postQuitMessage           = user32.NewProc("PostQuitMessage")
	setTimer                  = user32.NewProc("SetTimer")
	killTimer                 = user32.NewProc("KillTimer")
	loadIcon                  = user32.NewProc("LoadIconW")
	destroyIcon               = user32.NewProc("DestroyIcon")
	getCursorPos              = user32.NewProc("GetCursorPos")
	createPopupMenu           = user32.NewProc("CreatePopupMenu")
	appendMenu                = user32.NewProc("AppendMenuW")
	destroyMenu               = user32.NewProc("DestroyMenu")
	endMenu                   = user32.NewProc("EndMenu")
	setForegroundWindow       = user32.NewProc("SetForegroundWindow")
	trackPopupMenu            = user32.NewProc("TrackPopupMenu")
	notifyIcon                = shell32.NewProc("Shell_NotifyIconW")
	getStockIcon              = shell32.NewProc("SHGetStockIconInfo")
)

// These structures mirror the Win32 ABI, including unused fields.
type windowClass struct {
	size, style                   uint32
	callback                      uintptr
	classExtra, windowExtra       int32
	instance, icon, cursor, brush uintptr
	menuName, className           *uint16
	smallIcon                     uintptr
}

type windowMessage struct {
	window         uintptr
	message        uint32
	wParam, lParam uintptr
	time           uint32
	x, y           int32
	private        uint32
}

type notificationIcon struct {
	size               uint32
	window             uintptr
	id, flags, message uint32
	icon               uintptr
	tip                [128]uint16
	state, stateMask   uint32
	info               [256]uint16
	version            uint32
	infoTitle          [64]uint16
	infoFlags          uint32
	guid               windows.GUID
	balloonIcon        uintptr
}

type stockIconInfo struct {
	size              uint32
	icon              uintptr
	imageIndex, index int32
	path              [windows.MAX_PATH]uint16
}

type windowsTray struct {
	configPath     string
	service        *trayService
	panel          managementWindow
	instance       uintptr
	window         uintptr
	icon           uintptr
	taskbarCreated uint32
	version4       bool
	menuOpen       bool
	closing        bool
}

func runAgent(ctx context.Context, name string, noTray bool, log *slog.Logger) error {
	if noTray || !interactiveDesktop() {
		return run(ctx, name, log, nil)
	}
	name, err := filepath.Abs(name)
	if err != nil {
		return fmt.Errorf("resolve configuration path: %w", err)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	tray := windowsTray{configPath: name}
	if err := tray.createWindow(); err != nil {
		log.Warn("tray.unavailable", "error", err)
		return run(ctx, name, log, nil)
	}
	defer destroyWindow.Call(tray.window)
	var stock stockIconInfo
	stock.size = uint32(unsafe.Sizeof(stock))
	if hr, _, _ := getStockIcon.Call(siidDesktopPC, shgsiIcon|shgsiSmallIcon, uintptr(unsafe.Pointer(&stock))); hr == 0 && stock.icon != 0 {
		tray.icon = stock.icon
		defer destroyIcon.Call(stock.icon)
	} else {
		tray.icon, _, _ = loadIcon.Call(0, idiApplication)
	}
	defer tray.notify(nimDelete, tray.iconData())
	defer func() {
		if tray.panel.window != 0 {
			destroyWindow.Call(tray.panel.window)
		}
	}()

	tray.service = newTrayService(ctx, func(ctx context.Context, ready func()) error {
		err := run(ctx, name, log, ready)
		if err != nil && ctx.Err() == nil {
			log.Error("agent.error", "error", err)
		}
		return err
	}, func() { postMessage.Call(tray.window, wmServiceUpdate, 0, 0) })
	defer tray.service.close()
	posted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		postMessage.Call(tray.window, wmClose, 0, 0)
		close(posted)
	})
	defer func() {
		if !stop() {
			<-posted
		}
	}()

	tray.service.start()
	tray.refreshIcon()
	var msg windowMessage
	for {
		result, _, err := getMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		switch int32(result) {
		case -1:
			return fmt.Errorf("read tray message: %w", err)
		case 0:
			return nil
		}
		if tray.panel.window != 0 {
			processed, _, _ := isDialogMessage.Call(tray.panel.window, uintptr(unsafe.Pointer(&msg)))
			if processed != 0 {
				continue
			}
		}
		translateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		dispatchMessage.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

func interactiveDesktop() bool {
	var session uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &session); err != nil || session == 0 {
		return false
	}
	station, _, _ := getProcessWindowStation.Call()
	var info struct{ inherit, reserved, flags uint32 }
	ok, _, _ := getUserObjectInformation.Call(station, uoiFlags, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info), 0)
	return ok != 0 && info.flags&wsfVisible != 0
}

func (t *windowsTray) createWindow() error {
	var instance windows.Handle
	if err := windows.GetModuleHandleEx(windows.GET_MODULE_HANDLE_EX_FLAG_UNCHANGED_REFCOUNT, nil, &instance); err != nil {
		return err
	}
	t.instance = uintptr(instance)
	class := windowClass{
		instance: t.instance, className: windows.StringToUTF16Ptr("SuperVNC.Server.Tray"),
		callback: windows.NewCallback(t.windowProc),
	}
	class.size = uint32(unsafe.Sizeof(class))
	if atom, _, err := registerClass.Call(uintptr(unsafe.Pointer(&class))); atom == 0 {
		return fmt.Errorf("register tray window class: %w", err)
	}
	// A hidden top-level window receives the TaskbarCreated broadcast from Explorer.
	hwnd, _, err := createWindow.Call(0, uintptr(unsafe.Pointer(class.className)), 0, 0, 0, 0, 0, 0, 0, 0, t.instance, 0)
	if hwnd == 0 {
		return fmt.Errorf("create tray window: %w", err)
	}
	t.window = hwnd
	name := windows.StringToUTF16Ptr("TaskbarCreated")
	message, _, _ := registerWindowMessage.Call(uintptr(unsafe.Pointer(name)))
	t.taskbarCreated = uint32(message)
	if message != 0 {
		changeWindowMessageFilter.Call(hwnd, message, msgfltAllow, 0)
	}
	changeWindowMessageFilter.Call(hwnd, wmTrayIcon, msgfltAllow, 0)
	return nil
}

func (t *windowsTray) windowProc(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	switch message {
	case wmClose:
		t.quit()
		return 0
	case wmEndSession:
		if wParam != 0 {
			t.quit()
		}
		return 0
	case wmServiceUpdate:
		if t.service != nil && !t.closing {
			t.service.update()
			t.refreshStatus()
		}
		return 0
	case wmTrayIcon:
		if t.service != nil && !t.menuOpen && !t.closing {
			event := uint16(lParam)
			switch {
			case event == wmContextMenu || (event == wmRightButtonUp && !t.version4):
				var point struct{ x, y int32 }
				getCursorPos.Call(uintptr(unsafe.Pointer(&point)))
				t.showMenu(point.x, point.y)
			case event == wmLeftDoubleClick || (event == ninKeySelect && t.version4):
				t.openPanel()
			}
		}
		return 0
	case wmTimer:
		if wParam == iconRetryTimer && !t.closing {
			t.refreshIcon()
		}
		return 0
	default:
		if message != 0 && message == t.taskbarCreated && t.service != nil && !t.closing {
			t.refreshIcon()
			return 0
		}
	}
	ret, _, _ := defWindowProc.Call(hwnd, uintptr(message), wParam, lParam)
	return ret
}

func (t *windowsTray) quit() {
	t.closing = true
	endMenu.Call()
	postQuitMessage.Call(0)
}

func (t *windowsTray) iconData() *notificationIcon {
	data := notificationIcon{window: t.window, id: trayIconID}
	data.size = uint32(unsafe.Sizeof(data))
	return &data
}

func (t *windowsTray) notify(operation uintptr, data *notificationIcon) bool {
	ok, _, _ := notifyIcon.Call(operation, uintptr(unsafe.Pointer(data)))
	return ok != 0
}

func (t *windowsTray) refreshStatus() {
	t.updatePanel()
	t.refreshIcon()
}

func (t *windowsTray) refreshIcon() {
	data := t.iconData()
	data.flags = nifMessage | nifIcon | nifTip | nifShowTip
	data.message = wmTrayIcon
	data.icon = t.icon
	copy(data.tip[:], windows.StringToUTF16(productName+" · "+t.status()))
	if !t.notify(nimModify, data) {
		if !t.notify(nimAdd, data) {
			setTimer.Call(t.window, iconRetryTimer, 2000, 0)
			return
		}
		data.version = notifyVersion4
		t.version4 = t.notify(nimSetVersion, data)
	}
	killTimer.Call(t.window, iconRetryTimer)
}

func (t *windowsTray) status() string {
	switch t.service.state {
	case serviceStarting:
		return "正在启动"
	case serviceRunning:
		return "运行中"
	case serviceStopping:
		return "正在停止"
	case serviceRestarting:
		return "正在重启"
	default:
		if t.service.lastErr != nil {
			return "已停止（服务出错）"
		}
		return "已停止"
	}
}

func (t *windowsTray) showMenu(x, y int32) {
	menu, _, _ := createPopupMenu.Call()
	if menu == 0 {
		return
	}
	defer destroyMenu.Call(menu)
	t.menuOpen = true
	defer func() { t.menuOpen = false }()

	add := func(id uintptr, label string, enabled bool) {
		var flags uintptr
		if !enabled {
			flags = mfGrayed
		}
		text := windows.StringToUTF16Ptr(label)
		appendMenu.Call(menu, flags, id, uintptr(unsafe.Pointer(text)))
	}
	add(0, "状态："+t.status(), false)
	appendMenu.Call(menu, mfSeparator, 0, 0)
	add(menuOpen, "打开管理窗口", true)
	appendMenu.Call(menu, mfSeparator, 0, 0)
	add(menuStart, "启动服务", t.service.canStart())
	add(menuStop, "停止服务", t.service.canStop())
	add(menuRestart, "重启服务", t.service.canStop())
	add(menuConfig, "编辑配置", true)
	if t.service.lastErr != nil {
		add(menuError, "查看错误", true)
	}
	appendMenu.Call(menu, mfSeparator, 0, 0)
	add(menuQuit, "退出 SuperVNC", true)
	setForegroundWindow.Call(t.window)
	command, _, _ := trackPopupMenu.Call(menu, tpmRightButton|tpmReturnCommand, uintptr(x), uintptr(y), 0, t.window, 0)
	// Explorer needs a queued message after the popup to release menu tracking.
	postMessage.Call(t.window, 0, 0, 0)
	if t.closing {
		return
	}
	if command == 0 {
		t.notify(nimSetFocus, t.iconData())
		return
	}
	t.handleCommand(command, t.window)
}

func (t *windowsTray) handleCommand(command, owner uintptr) {
	switch command {
	case menuOpen:
		t.openPanel()
		return
	case menuStart:
		t.service.start()
	case menuStop, menuRestart:
		t.service.stop(command == menuRestart)
	case menuConfig:
		if err := t.editConfig(owner); err != nil {
			showTrayError(owner, "无法打开配置文件", err)
		}
		return
	case menuError:
		if t.service.lastErr != nil {
			showTrayError(owner, "服务出错", t.service.lastErr)
		}
		return
	case menuQuit:
		t.quit()
		return
	default:
		return
	}
	t.refreshStatus()
}

func (t *windowsTray) editConfig(owner uintptr) error {
	systemDir, err := windows.GetSystemDirectory()
	if err != nil {
		return err
	}
	return windows.ShellExecute(windows.Handle(owner), windows.StringToUTF16Ptr("open"),
		windows.StringToUTF16Ptr(filepath.Join(systemDir, "notepad.exe")),
		windows.StringToUTF16Ptr(windows.ComposeCommandLine([]string{t.configPath})), nil, windows.SW_SHOWNORMAL)
}

func showTrayError(owner uintptr, title string, err error) {
	message := strings.ReplaceAll(err.Error(), "\x00", `\0`)
	windows.MessageBox(windows.HWND(owner), windows.StringToUTF16Ptr(message),
		windows.StringToUTF16Ptr(productName+" · "+title), windows.MB_ICONERROR)
}
