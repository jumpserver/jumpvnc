package main

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The installer job runs this on its disposable Windows runner after installing
// the real setup executable. Normal unit tests never touch an installed service.
func TestInstalledManagementWindow(t *testing.T) {
	if os.Getenv("JUMPVNC_TEST_INSTALLED_PANEL") != "1" {
		t.Skip("requires the installer acceptance environment")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	root := filepath.Join(os.Getenv("ProgramFiles"), "SuperVNC")
	command := exec.Command(filepath.Join(root, "bin", "sshvnc.exe"), "-manage-service", "-open-panel")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer command.Wait()
	defer command.Process.Kill()
	find := user32.NewProc("FindWindowW")
	class := windows.StringToUTF16Ptr("JumpVNC.Server.Management")
	var hwnd uintptr
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		hwnd, _, _ = find.Call(uintptr(unsafe.Pointer(class)), 0)
		if hwnd != 0 {
			keys := installedControl(hwnd, idAuthorizedKeys)
			if keys != 0 && strings.Contains(installedControlText(keys), "ssh-ed25519") {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if hwnd == 0 || !strings.Contains(installedControlText(installedControl(hwnd, idAuthorizedKeys)), "ssh-ed25519") {
		t.Fatal("the installed management window did not display generated authorization")
	}
	if got := installedControlText(installedControl(hwnd, idSSHListenInput)); got != "0.0.0.0:5922" {
		t.Fatalf("unexpected default listener %q", got)
	}
	if got := installedControlText(installedControl(hwnd, idSSHUserInput)); got != "vncuser" {
		t.Fatalf("unexpected default user %q", got)
	}
	for _, id := range []uintptr{idKeyCopy, idConnectionCopy, idPublicCopy} {
		button := installedControl(hwnd, id)
		if button == 0 {
			t.Fatalf("missing copy action %d", id)
		}
		if ok, _, err := user32.NewProc("SendMessageTimeoutW").Call(hwnd, wmCommand, id, button, 2, 5000, 0); ok == 0 {
			t.Fatal(err)
		}
		copied := readInstalledClipboard(t)
		switch id {
		case idKeyCopy:
			private, err := os.ReadFile(filepath.Join(root, connectionKeyName))
			if err != nil || !bytes.Equal([]byte(copied), private) {
				t.Fatal("copy action did not return the generated connection private key")
			}
			clear(private)
		case idConnectionCopy:
			if !strings.Contains(copied, "SVNC") || !strings.Contains(copied, "5922") || !strings.Contains(copied, "vncuser") || !strings.Contains(copied, "SHA256:") {
				t.Fatal("copied connection information is incomplete")
			}
		case idPublicCopy:
			if validatePublicKeys([]byte(copied)) != nil {
				t.Fatal("copied authorization is not a public key")
			}
		}
	}
	// Capture only the management window; credentials remain masked in this image.
	if name := os.Getenv("JUMPVNC_PANEL_SCREENSHOT"); name != "" {
		writeInstalledPanelScreenshot(t, hwnd, name)
	}
	if err := setWindowsClipboard(hwnd, ""); err != nil {
		t.Fatal(err)
	}
}

func installedControl(hwnd, id uintptr) uintptr {
	control, _, _ := user32.NewProc("GetDlgItem").Call(hwnd, id)
	return control
}

func readInstalledClipboard(t *testing.T) string {
	t.Helper()
	if ok, _, err := user32.NewProc("OpenClipboard").Call(0); ok == 0 {
		t.Fatal(err)
	}
	defer user32.NewProc("CloseClipboard").Call()
	handle, _, err := user32.NewProc("GetClipboardData").Call(13)
	if handle == 0 {
		t.Fatal(err)
	}
	pointer, _, err := kernel32.NewProc("GlobalLock").Call(handle)
	if pointer == 0 {
		t.Fatal(err)
	}
	defer kernel32.NewProc("GlobalUnlock").Call(handle)
	return windows.UTF16PtrToString((*uint16)(unsafe.Pointer(pointer)))
}

func writeInstalledPanelScreenshot(t *testing.T, hwnd uintptr, name string) {
	t.Helper()
	var rect struct{ left, top, right, bottom int32 }
	if ok, _, err := user32.NewProc("GetWindowRect").Call(hwnd, uintptr(unsafe.Pointer(&rect))); ok == 0 {
		t.Fatal(err)
	}
	width, height := int(rect.right-rect.left), int(rect.bottom-rect.top)
	dc, _, _ := user32.NewProc("GetDC").Call(hwnd)
	defer user32.NewProc("ReleaseDC").Call(hwnd, dc)
	memory, _, _ := gdi32.NewProc("CreateCompatibleDC").Call(dc)
	defer gdi32.NewProc("DeleteDC").Call(memory)
	info := struct {
		size                   uint32
		width, height          int32
		planes, depth          uint16
		compression, imageSize uint32
		x, y                   int32
		used, important        uint32
	}{size: 40, width: int32(width), height: -int32(height), planes: 1, depth: 32}
	var pixels uintptr
	bitmap, _, err := gdi32.NewProc("CreateDIBSection").Call(dc, uintptr(unsafe.Pointer(&info)), 0, uintptr(unsafe.Pointer(&pixels)), 0, 0)
	if bitmap == 0 || pixels == 0 {
		t.Fatal(err)
	}
	defer gdi32.NewProc("DeleteObject").Call(bitmap)
	previous, _, _ := gdi32.NewProc("SelectObject").Call(memory, bitmap)
	defer gdi32.NewProc("SelectObject").Call(memory, previous)
	if ok, _, err := user32.NewProc("PrintWindow").Call(hwnd, memory, 2); ok == 0 {
		t.Fatal(err)
	}
	data := unsafe.Slice((*byte)(unsafe.Pointer(pixels)), width*height*4)
	picture := image.NewNRGBA(image.Rect(0, 0, width, height))
	for offset := 0; offset < len(data); offset += 4 {
		picture.Pix[offset], picture.Pix[offset+1], picture.Pix[offset+2], picture.Pix[offset+3] = data[offset+2], data[offset+1], data[offset], 255
	}
	file, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := png.Encode(file, picture); err != nil {
		t.Fatal(err)
	}
}

func installedControlText(control uintptr) string {
	length, _, _ := sendMessage.Call(control, 0x000e, 0, 0)
	buffer := make([]uint16, length+1)
	sendMessage.Call(control, 0x000d, uintptr(len(buffer)), uintptr(unsafe.Pointer(&buffer[0])))
	return windows.UTF16ToString(buffer)
}
