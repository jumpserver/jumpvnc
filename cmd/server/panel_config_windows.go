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
		setControlText(p.fingerprint, "无法读取配置文件")
		setControlText(p.sshListenInput, "")
		setControlText(p.sshUserInput, "")
		setControlText(p.authorizedKeys, "")
		setControlText(p.rootLabel, "根目录")
		hint := "请打开 YAML 配置检查文件内容。"
		if !t.canConfigure() {
			hint = "以管理员身份管理后，可查看和修改配置。"
		}
		setControlText(p.rootHint, hint)
		setControlText(p.passwordHint, hint)
		setControlText(p.keysHint, hint)
	} else {
		t.setPanelPasswordFile(desktopPasswordFile(t.configPath, cfg))
		setControlText(p.sshListenInput, cfg.SSH.Listen)
		setControlText(p.sshUserInput, cfg.SSH.User)
		t.refreshPanelAuthorization(cfg)
		p.rootEnabled = true
		p.savedMode = cfg.SFTP.Mode
		sendMessage.Call(p.sftpMode, cbSetCurSel, uintptr(sftpModeIndex(cfg.SFTP.Mode)), 0)
		p.savedRoot = cfg.SFTP.Root
		if p.savedRoot == "" && t.registration == nil {
			p.savedRoot = "~"
		}
		if !keepDraft {
			setControlText(p.rootInput, p.savedRoot)
		}
		setControlText(p.rootHint, "")
	}
	setControlEnabled(p.rootInput, p.rootEnabled)
	setControlEnabled(p.rootBrowse, p.rootEnabled)
	for _, control := range []uintptr{p.sshListenInput, p.sshUserInput, p.sshSave, p.authorizedKeys, p.keysSave, p.keyCopy, p.publicCopy, p.connectionCopy, p.sftpMode} {
		setControlEnabled(control, err == nil)
	}
	p.updateRootSave()
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
	mode := p.selectedSFTPMode()
	setControlEnabled(p.rootSave, p.rootEnabled && (mode == "disabled" || strings.TrimSpace(root) != "") && (root != p.savedRoot || mode != p.savedMode))
}

func (t *windowsTray) savePanelRoot() {
	p := &t.panel
	root := controlText(p.rootInput)
	mode := p.selectedSFTPMode()
	if !t.canApplyPanelSettings() || !p.rootEnabled || (root == p.savedRoot && mode == p.savedMode) {
		return
	}
	if mode != "disabled" && t.registration != nil && !filepath.IsAbs(root) {
		showTrayError(p.window, "无法保存 SFTP 根目录", errors.New("请输入完整的文件夹路径"))
		return
	}
	if err := updateProtectedFile(t.configPath, func(original []byte) ([]byte, error) {
		return config.WithSFTPSettings(t.configPath, original, mode, root)
	}); err != nil {
		showTrayError(p.window, "无法保存 SFTP 根目录", err)
		return
	}
	t.refreshPanelSettings(false)
	t.applyPanelSettings()
	setControlText(p.rootHint, "已保存，正在应用文件传输设置。")
}

var sftpModes = []string{"disabled", "read_write", "read_only", "write_only"}

func sftpModeIndex(mode string) int {
	for i, value := range sftpModes {
		if mode == value {
			return i
		}
	}
	return 0
}

func (p *managementWindow) selectedSFTPMode() string {
	index, _, _ := sendMessage.Call(p.sftpMode, cbGetCurSel, 0, 0)
	if index >= uintptr(len(sftpModes)) {
		return "disabled"
	}
	return sftpModes[index]
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
