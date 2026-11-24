package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"

	"sshvnc/internal/config"
)

var (
	kernel32            = windows.NewLazySystemDLL("kernel32.dll")
	ole32               = windows.NewLazySystemDLL("ole32.dll")
	getWindowText       = user32.NewProc("GetWindowTextW")
	getWindowTextLength = user32.NewProc("GetWindowTextLengthW")
	replaceFile         = kernel32.NewProc("ReplaceFileW")
	browseForFolder     = shell32.NewProc("SHBrowseForFolderW")
	getFolderPath       = shell32.NewProc("SHGetPathFromIDListEx")
	oleInitialize       = ole32.NewProc("OleInitialize")
	oleUninitialize     = ole32.NewProc("OleUninitialize")
	freeShellItem       = ole32.NewProc("CoTaskMemFree")
)

const rootPathHint = "相对路径以配置文件所在目录为准。保存后重启生效。"

func (t *windowsTray) refreshPanelSettings(keepDraft bool) {
	p := &t.panel
	current := controlText(p.rootInput)
	keepDraft = keepDraft && p.rootEnabled && current != p.savedRoot
	var cfg config.Config
	var err error
	if t.canConfigure() {
		cfg, err = config.ReadSettings(t.configPath)
	} else {
		err = errors.New("需要管理员权限")
	}
	if err != nil {
		t.setPanelPasswordFile("")
		p.rootEnabled = false
		p.compatibilityAllowed = false
		setControlText(p.ssh, "无法读取配置文件")
		setControlText(p.sshUser, "")
		setControlText(p.vnc, "")
		setControlText(p.compatibility, "")
		setControlText(p.rootLabel, "根目录")
		setControlText(p.rootDirectory, "")
		hint := "请打开 YAML 配置检查文件内容。"
		if !t.canConfigure() {
			hint = "以管理员身份管理后，可查看和修改配置。"
		}
		setControlText(p.rootHint, hint)
		setControlText(p.passwordHint, hint)
	} else {
		t.setPanelPasswordFile(desktopPasswordFile(t.configPath, cfg))
		setControlText(p.ssh, "SSH 监听："+cfg.SSH.Listen)
		setControlText(p.sshUser, "SSH 登录用户名："+cfg.SSH.User)
		backend := "关闭"
		if cfg.SSH.AllowVNC {
			backend = cfg.VNC.Listen + "  (" + cfg.VNC.Engine + ")"
		}
		setControlText(p.vnc, "VNC 后端："+backend)
		compatibility := "关闭"
		p.compatibilityAllowed = cfg.SSH.AllowVNC && t.registration == nil
		p.compatibilityEnabled = cfg.VNC.Compatibility.Enabled
		toggle := "开启兼容"
		if p.compatibilityEnabled {
			compatibility = cfg.VNC.Compatibility.Listen + "  (普通 VNC，未加密)"
			toggle = "关闭兼容"
		}
		setControlText(p.compatibility, "VNC 兼容入口："+compatibility)
		setControlText(p.compatibilityToggle, toggle)
		if t.registration != nil {
			setControlText(p.compatibility, "VNC 兼容入口：系统服务模式关闭此入口")
		}
		p.rootEnabled = cfg.SFTP.Mode != "disabled"
		setControlText(p.rootLabel, "根目录  ·  "+sftpModeLabel(cfg.SFTP.Mode))
		p.savedRoot = cfg.SFTP.Root
		if p.savedRoot == "" {
			p.savedRoot = "~"
		}
		if !keepDraft {
			setControlText(p.rootInput, p.savedRoot)
		}
		setControlText(p.rootDirectory, "")
		hint := rootPathHint
		if t.registration != nil {
			hint = "指定文件目录，不使用系统服务的 HOME。保存后重启生效。"
		}
		if p.rootEnabled {
			if root, err := cfg.SFTP.RootDirectory(t.configPath); err == nil {
				setControlText(p.rootDirectory, "解析后："+root)
			} else {
				setControlText(p.rootDirectory, "无法解析已配置的根目录。")
			}
		} else {
			hint = "SFTP 已关闭，可在 YAML 配置中启用。"
		}
		setControlText(p.rootHint, hint)
	}
	setControlEnabled(p.rootInput, p.rootEnabled)
	setControlEnabled(p.rootBrowse, p.rootEnabled)
	setControlEnabled(p.rootHome, p.rootEnabled && t.registration == nil)
	setControlEnabled(p.compatibilityToggle, p.compatibilityAllowed)
	p.updateRootSave()
}

func (t *windowsTray) togglePanelCompatibility() {
	p := &t.panel
	if !p.compatibilityAllowed {
		return
	}
	err := updateProtectedFile(t.configPath, func(original []byte) ([]byte, error) {
		return config.WithVNCCompatibility(original, !p.compatibilityEnabled)
	})
	if err != nil {
		showTrayError(p.window, "无法保存 VNC 兼容模式", err)
		return
	}
	t.refreshPanelSettings(true)
	setControlText(p.compatibility, controlText(p.compatibility)+" · 重启生效")
}

func sftpModeLabel(mode string) string {
	switch mode {
	case "read_write":
		return "读写"
	case "read_only":
		return "只读"
	case "write_only":
		return "只写"
	default:
		return "关闭"
	}
}

func setControlText(control uintptr, value string) {
	text := windows.StringToUTF16Ptr(strings.ReplaceAll(value, "\x00", `\0`))
	setWindowText.Call(control, uintptr(unsafe.Pointer(text)))
}

func controlText(control uintptr) string {
	length, _, _ := getWindowTextLength.Call(control)
	buffer := make([]uint16, length+1)
	getWindowText.Call(control, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
	return windows.UTF16ToString(buffer)
}

func (p *managementWindow) updateRootSave() {
	root := controlText(p.rootInput)
	setControlEnabled(p.rootSave, p.rootEnabled && strings.TrimSpace(root) != "" && root != p.savedRoot)
}

func (t *windowsTray) savePanelRoot() {
	p := &t.panel
	root := controlText(p.rootInput)
	if !p.rootEnabled || root == p.savedRoot || strings.TrimSpace(root) == "" {
		return
	}
	if t.registration != nil && !filepath.IsAbs(root) {
		showTrayError(p.window, "无法保存 SFTP 根目录", errors.New("系统服务模式请指定完整目录路径，不使用 HOME"))
		return
	}
	if err := writeSFTPRoot(t.configPath, root); err != nil {
		showTrayError(p.window, "无法保存 SFTP 根目录", err)
		return
	}
	t.refreshPanelSettings(false)
	setControlText(p.rootHint, "已保存。重启服务后使用新目录。")
}

func writeSFTPRoot(name, root string) error {
	return updateProtectedFile(name, func(original []byte) ([]byte, error) {
		return config.WithSFTPRoot(name, original, root)
	})
}

func updateProtectedFile(name string, edit func([]byte) ([]byte, error)) error {
	target, err := filepath.EvalSymlinks(name)
	if err != nil {
		return err
	}
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("必须使用普通文件")
	}
	original, err := os.ReadFile(target)
	if err != nil {
		return err
	}
	defer clear(original)
	updated, err := edit(original)
	if err != nil {
		return err
	}
	defer clear(updated)
	if bytes.Equal(original, updated) {
		return nil
	}
	security, err := windows.GetNamedSecurityInfo(target, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	acl, _, err := security.DACL()
	if err != nil {
		return err
	}
	stage, err := os.CreateTemp(filepath.Dir(target), ".jumpvnc-config-*")
	if err != nil {
		return err
	}
	defer os.Remove(stage.Name())
	defer stage.Close()
	// The original may have a more restrictive ACL than the containing directory.
	if err := windows.SetNamedSecurityInfo(stage.Name(), windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		return err
	}
	if _, err := stage.Write(updated); err != nil {
		return err
	}
	if err := stage.Sync(); err != nil {
		return err
	}
	if err := stage.Close(); err != nil {
		return err
	}
	currentTarget, err := filepath.EvalSymlinks(name)
	if err != nil {
		return err
	}
	current, err := os.ReadFile(currentTarget)
	if err != nil {
		return err
	}
	defer clear(current)
	if !strings.EqualFold(target, currentTarget) || !bytes.Equal(original, current) {
		return errors.New("文件已被其他程序修改，请重新打开管理窗口后再保存")
	}
	return replaceConfiguration(target, stage.Name())
}

func windowsRunUser() string {
	buffer := make([]uint16, 256)
	size := uint32(len(buffer))
	err := windows.GetUserNameEx(windows.NameSamCompatible, &buffer[0], &size)
	if errors.Is(err, windows.ERROR_MORE_DATA) && size > uint32(len(buffer)) {
		buffer = make([]uint16, size)
		err = windows.GetUserNameEx(windows.NameSamCompatible, &buffer[0], &size)
	}
	if err != nil {
		return "无法读取"
	}
	return windows.UTF16ToString(buffer)
}

func replaceConfiguration(target, staged string) error {
	backup := staged + ".bak"
	to, err := windows.UTF16PtrFromString(windowsFilePath(target))
	if err != nil {
		return err
	}
	from, err := windows.UTF16PtrFromString(windowsFilePath(staged))
	if err != nil {
		return err
	}
	previous, err := windows.UTF16PtrFromString(windowsFilePath(backup))
	if err != nil {
		return err
	}
	if ok, _, err := replaceFile.Call(uintptr(unsafe.Pointer(to)), uintptr(unsafe.Pointer(from)), uintptr(unsafe.Pointer(previous)), 0, 0, 0); ok == 0 {
		if errors.Is(err, windows.ERROR_UNABLE_TO_MOVE_REPLACEMENT_2) {
			if restoreErr := windows.MoveFile(previous, to); restoreErr != nil {
				return fmt.Errorf("保存失败，原文件备份位于 %s: %w", backup, err)
			}
		}
		return err
	}
	_ = os.Remove(backup)
	return nil
}

func windowsFilePath(path string) string {
	if strings.HasPrefix(path, `\\?\`) {
		return path
	}
	if strings.HasPrefix(path, `\\`) {
		return `\\?\UNC\` + path[2:]
	}
	return `\\?\` + path
}

func (t *windowsTray) browsePanelRoot() {
	if status, _, _ := oleInitialize.Call(0); int32(status) < 0 {
		showTrayError(t.panel.window, "无法打开目录选择窗口", fmt.Errorf("OleInitialize: 0x%08x", uint32(status)))
		return
	}
	defer oleUninitialize.Call()
	var name [windows.MAX_PATH]uint16
	info := struct {
		window, root       uintptr
		displayName, title *uint16
		flags              uint32
		callback, param    uintptr
		image              int32
	}{
		window: t.panel.window, displayName: &name[0],
		title: windows.StringToUTF16Ptr("选择 SFTP 根目录"), flags: 0x51, // BIF_RETURNONLYFSDIRS | BIF_USENEWUI
	}
	item, _, _ := browseForFolder.Call(uintptr(unsafe.Pointer(&info)))
	if item == 0 {
		return
	}
	defer freeShellItem.Call(item)
	var path [32768]uint16
	ok, _, _ := getFolderPath.Call(item, uintptr(unsafe.Pointer(&path[0])), uintptr(len(path)), 0)
	if ok == 0 {
		showTrayError(t.panel.window, "无法选择该目录", errors.New("请选择具体的文件系统目录"))
		return
	}
	setControlText(t.panel.rootInput, windows.UTF16ToString(path[:]))
}
