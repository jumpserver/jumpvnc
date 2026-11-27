package main

import (
	"bytes"
	"crypto/des"
	"fmt"
	"io"
	"math/bits"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/windows"
	"jumpvnc/internal/config"
	"jumpvnc/internal/vnc"
)

// This opt-in acceptance runs in the interactive manager's Windows session.
// Only desktop channels created here may be selected for disconnection.
func TestInstalledRemoteConnections(t *testing.T) {
	output := os.Getenv("JUMPVNC_REMOTE_SCREENSHOTS")
	if output == "" {
		t.Skip("requires a running Windows installation and its management window")
	}
	root := filepath.Join(os.Getenv("ProgramFiles"), "SuperVNC")
	cfg, err := config.Load(filepath.Join(root, "service.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	private, err := os.ReadFile(filepath.Join(root, connectionKeyName))
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey(private)
	clear(private)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := os.ReadFile(cfg.SSH.HostKeyFile + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	host, _, _, _, err := ssh.ParseAuthorizedKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	password, err := vnc.ReadDesktopPassword(cfg.VNC.PasswordFile)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(password)
	var key [8]byte
	for i, b := range password {
		key[i] = bits.Reverse8(b)
	}
	cipher, err := des.NewCipher(key[:])
	clear(key[:])
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(cfg.SSH.Listen)
	if err != nil {
		t.Fatal(err)
	}
	peer := net.JoinHostPort("127.0.0.1", port)
	newViewer := func() (ssh.Channel, string) {
		t.Helper()
		client, err := ssh.Dial("tcp", peer, &ssh.ClientConfig{User: cfg.SSH.User, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: ssh.FixedHostKey(host), Timeout: 8 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { client.Close() })
		ch, req, err := client.OpenChannel("session", nil)
		if err != nil {
			t.Fatal(err)
		}
		go ssh.DiscardRequests(req)
		if ok, err := ch.SendRequest("subsystem", true, ssh.Marshal(struct{ Name string }{"vnc"})); !ok || err != nil {
			t.Fatal("desktop subsystem unavailable", err)
		}
		timer := time.AfterFunc(10*time.Second, func() { client.Close() })
		defer timer.Stop()
		read := func(n int) []byte {
			t.Helper()
			b := make([]byte, n)
			if _, err := io.ReadFull(ch, b); err != nil {
				t.Fatal(err)
			}
			return b
		}
		banner := read(12)
		if string(banner) != "RFB 003.008\n" {
			t.Fatal("unexpected RFB version")
		}
		ch.Write(banner)
		if !bytes.Equal(read(2), []byte{1, 2}) {
			t.Fatal("VNC authentication is required")
		}
		ch.Write([]byte{2})
		challenge := read(16)
		cipher.Encrypt(challenge[:8], challenge[:8])
		cipher.Encrypt(challenge[8:], challenge[8:])
		ch.Write(challenge)
		if !bytes.Equal(read(4), []byte{0, 0, 0, 0}) {
			t.Fatal("VNC authentication refused")
		}
		ch.Write([]byte{1})
		read(24) // shared ClientInit followed by ServerInit
		return ch, client.LocalAddr().String()
	}
	first, a := newViewer()
	second, b := newViewer()
	class := windows.StringToUTF16Ptr("JumpVNC.Server.Management")
	hwnd, _, _ := user32.NewProc("FindWindowW").Call(uintptr(unsafe.Pointer(class)), 0)
	if hwnd == 0 {
		t.Fatal("open the installed management window first")
	}
	command := func(id uintptr) {
		t.Helper()
		ok, _, err := user32.NewProc("SendMessageTimeoutW").Call(hwnd, wmCommand, id, installedControl(hwnd, id), 2, 3000, 0)
		if ok == 0 {
			t.Fatal(err)
		}
	}
	command(menuConnections)
	list := installedControl(hwnd, idRemoteList)
	// LB_GETTEXT is marshalled by Windows for list boxes with string items.
	rows := func() []string {
		count, _, _ := sendMessage.Call(list, 0x018b, 0, 0)
		if count > 65536 {
			t.Fatal("invalid list count")
		}
		out := make([]string, 0, count)
		for i := range count {
			n, _, _ := sendMessage.Call(list, 0x018a, i, 0)
			if n > 2048 {
				t.Fatal("invalid row")
			}
			buf := make([]uint16, 2049)
			sendMessage.Call(list, 0x0189, i, uintptr(unsafe.Pointer(&buf[0])))
			out = append(out, windows.UTF16ToString(buf))
		}
		return out
	}
	wait := func(check func([]string) bool) {
		t.Helper()
		until := time.Now().Add(8 * time.Second)
		for time.Now().Before(until) {
			if check(rows()) {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("connection list timeout: %s / %s", installedControlText(installedControl(hwnd, idRemoteDetail)), strings.Join(rows(), " / "))
	}
	index := func(lines []string, addr string) int {
		for i, line := range lines {
			if strings.Contains(line, " @ "+addr+" ") && strings.Contains(line, "远程控制") {
				return i
			}
		}
		return -1
	}
	wait(func(r []string) bool { return index(r, a) >= 0 && index(r, b) >= 0 })
	if err := os.MkdirAll(output, 0755); err != nil {
		t.Fatal(err)
	}
	writeInstalledPanelScreenshot(t, hwnd, filepath.Join(output, "remote-connected.png"))
	selected := index(rows(), a)
	if selected < 0 {
		t.Fatal("test connection disappeared")
	}
	sendMessage.Call(list, 0x0186, uintptr(selected), 0)
	command(idRemoteDisconnect)
	closed := func(ch ssh.Channel) {
		t.Helper()
		done := make(chan struct{})
		go func() { io.Copy(io.Discard, ch); close(done) }()
		select {
		case <-done:
		case <-time.After(4 * time.Second):
			t.Fatal("Viewer still connected")
		}
		ch.Close()
	}
	closed(first)
	wait(func(r []string) bool { return index(r, a) < 0 && index(r, b) >= 0 })
	writeInstalledPanelScreenshot(t, hwnd, filepath.Join(output, "remote-one-left.png"))
	left := rows()
	if len(left) != 1 || index(left, b) != 0 {
		t.Fatal("another connection is present; skipped disconnect-all")
	}
	command(idRemoteAll)
	closed(second)
	wait(func(r []string) bool { return len(r) == 0 })
	writeInstalledPanelScreenshot(t, hwnd, filepath.Join(output, "remote-disconnected.png"))
	if state, err := (windowsServiceBackend{}).query(); state != serviceRunning || err != nil {
		t.Fatal("service did not remain running", err)
	}
	t.Log(fmt.Sprintf("Two authenticated RFB connections closed via the management window, service running on %s", peer))
}
