package vnc

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	providerCLSID = "{D55005C6-847D-422B-95D8-C57A72D45D2B}"
	loginPipe     = `\\.\pipe\JumpVNC-ConsoleLogon-v1`
)

type windowsLogin struct {
	session uint32
	log     *slog.Logger
}

func (w windowsLogin) Available() bool {
	if windows.WTSGetActiveConsoleSessionId() != w.session {
		return false
	}
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Authentication\Credential Providers\`+providerCLSID, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer key.Close()
	disabled, _, err := key.GetIntegerValue("Disabled")
	return errors.Is(err, registry.ErrNotExist) || (err == nil && disabled == 0)
}

var queryWTS = windows.NewLazySystemDLL("wtsapi32.dll").NewProc("WTSQuerySessionInformationW")

// WTSINFOEXW's level-1 union is aligned to eight bytes on Windows x64.
func (w windowsLogin) lockState() (uint32, bool) {
	var info *byte
	var length uint32
	const wtsSessionInfoEx = 25
	result, _, _ := queryWTS.Call(0, uintptr(w.session), wtsSessionInfoEx, uintptr(unsafe.Pointer(&info)), uintptr(unsafe.Pointer(&length)))
	if result == 0 || info == nil {
		return 0, false
	}
	defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(info)))
	if length < 20 || windows.WTSGetActiveConsoleSessionId() != w.session {
		return 0, false
	}
	p := unsafe.Slice(info, 20)
	if binary.LittleEndian.Uint32(p) != 1 || binary.LittleEndian.Uint32(p[8:]) != w.session {
		return 0, false
	}
	return binary.LittleEndian.Uint32(p[16:]), true
}

func consoleUser(session uint32) (*windows.SID, error) {
	var token windows.Token
	err := windows.WTSQueryUserToken(session, &token)
	if errors.Is(err, windows.ERROR_NO_TOKEN) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid.Copy()
}

func localLoginSID(account string) *windows.SID {
	computer, err := windows.ComputerName()
	if err != nil {
		return nil
	}
	name := strings.TrimPrefix(account, `.\`)
	if name == "" || strings.ContainsAny(name, `\/@`) {
		return nil
	}
	requested, domain, kind, err := windows.LookupSID("", computer+`\`+name)
	if err != nil || kind != windows.SidTypeUser || !strings.EqualFold(domain, computer) {
		return nil
	}
	return requested
}

func (w windowsLogin) accountMatches(account string, allowEmpty bool) bool {
	requested := localLoginSID(account)
	if requested == nil {
		return false
	}
	owner, err := consoleUser(w.session)
	return err == nil && ((owner == nil && allowEmpty) || (owner != nil && requested.Equals(owner)))
}

func (w windowsLogin) Active(account string) bool {
	state, ok := w.lockState()
	return ok && state == 1 && w.accountMatches(account, false)
}

func (w windowsLogin) Ready(account string) bool {
	state, ok := w.lockState()
	if !ok {
		return false
	}
	// Before the first local sign-in Windows can report an unlocked Winlogon
	// desktop even though there is no interactive user.
	owner, err := consoleUser(w.session)
	if err != nil || state > 1 {
		return false
	}
	if account == "" {
		return owner == nil || state == 0
	}
	target := localLoginSID(account)
	if target == nil {
		return false
	}
	if owner == nil || owner.Equals(target) {
		return owner == nil || state == 0
	}
	return switchAllowed(owner, target)
}

func (w windowsLogin) canSubmit(account string) bool {
	return w.Available() && w.Ready(account) && w.accountMatches(account, true)
}

func (w windowsLogin) Submit(ctx context.Context, account string, password []byte) error {
	name := utf16.Encode([]rune(account))
	if len(name) == 0 || len(name) > 256 {
		return errors.New("invalid local account length")
	}
	secret, err := loginPasswordFrame(password)
	if err != nil {
		return err
	}
	defer clear(secret)
	owner, err := consoleUser(w.session)
	if err != nil {
		return err
	}
	target := localLoginSID(account)
	if target == nil {
		return errors.New("local sign-in account unavailable")
	}
	switching := owner != nil && !target.Equals(owner)
	canSubmit := func() bool {
		if !switching {
			return w.canSubmit(account)
		}
		state, ok := w.lockState()
		current, err := consoleUser(w.session)
		return ok && state == 0 && err == nil && current != nil && current.Equals(owner) &&
			w.Available() && switchAllowed(owner, target)
	}
	if switching {
		if err := w.prepareUserSwitch(ctx, owner, target, account, secret); err != nil {
			return err
		}
	}
	if !canSubmit() {
		return errors.New("console owner or lock state does not match this account")
	}

	// go-winio creates the first instance exclusively and rejects remote pipes.
	// LogonUI first peeks for a complete invitation. A zero-quota pipe can
	// leave the service's write pending until a read is posted.
	listener, err := winio.ListenPipe(loginPipe, &winio.PipeConfig{SecurityDescriptor: "D:P(A;;GA;;;SY)", InputBufferSize: 1024, OutputBufferSize: 1024})
	if err != nil {
		return err
	}
	defer listener.Close()
	cancelAccept := context.AfterFunc(ctx, func() { listener.Close() })
	defer cancelAccept()
	// Give an already-visible sign-in screen time to serialize the credential.
	// The standard service SAS follows Windows' software SAS policy.
	sasCtx, cancelSAS := context.WithCancel(ctx)
	defer cancelSAS()
	sasDone := make(chan struct{})
	defer func() { cancelSAS(); <-sasDone }()
	go func() {
		defer close(sasDone)
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		select {
		case <-sasCtx.Done():
		case <-timer.C:
			if sasCtx.Err() == nil && canSubmit() {
				if err := sendConsoleSAS(w.log); err != nil {
					w.log.Warn("windows_login.sas_unavailable", "error", err)
				}
			}
		}
	}()
	conn, err := listener.Accept()
	if err != nil {
		return fmt.Errorf("wait for Windows sign-in screen: %w", err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(30 * time.Second)
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}
	process, err := w.logonUIPeer(conn)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(process)
	header := make([]byte, 10+2*len(name))
	copy(header, "JVC1")
	if switching {
		copy(header, "JVS1")
	}
	binary.LittleEndian.PutUint32(header[4:], w.session)
	binary.LittleEndian.PutUint16(header[8:], uint16(len(name)))
	for i, u := range name {
		binary.LittleEndian.PutUint16(header[10+i*2:], u)
	}
	if _, err := conn.Write(header); err != nil {
		return fmt.Errorf("offer account to LogonUI: %w", err)
	}
	var ack [1]byte
	if _, err := io.ReadFull(conn, ack[:]); err != nil {
		return fmt.Errorf("wait for Windows to select the sign-in tile: %w", err)
	}
	cancelSAS()
	<-sasDone
	expected := byte('G')
	if switching {
		expected = 'L'
	}
	if ack[0] != expected || ctx.Err() != nil || !canSubmit() {
		return errors.New("credential provider cancelled the login")
	}
	if _, err := conn.Write(secret); err != nil {
		return fmt.Errorf("deliver credential to selected Windows tile: %w", err)
	}
	clear(secret)
	if _, err := io.ReadFull(conn, ack[:]); err != nil {
		return fmt.Errorf("wait for credential serialization: %w", err)
	}
	if ack[0] != 'S' {
		return errors.New("Windows did not receive the credential")
	}
	return nil
}

func (w windowsLogin) logonUIPeer(conn net.Conn) (windows.Handle, error) {
	fd, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		return 0, errors.New("cannot verify credential provider connection")
	}
	var pid uint32
	if err := windows.GetNamedPipeClientProcessId(windows.Handle(fd.Fd()), &pid); err != nil {
		return 0, err
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return 0, err
	}
	verified := false
	defer func() {
		if !verified {
			windows.CloseHandle(process)
		}
	}()
	system, err := windows.GetSystemDirectory()
	if err != nil {
		return 0, err
	}
	path := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(path))
	if err := windows.QueryFullProcessImageName(process, 0, &path[0], &size); err != nil {
		return 0, err
	}
	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return 0, err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return 0, err
	}
	var session, n uint32
	if err := windows.GetTokenInformation(token, windows.TokenSessionId, (*byte)(unsafe.Pointer(&session)), 4, &n); err != nil {
		return 0, err
	}
	if !strings.EqualFold(windows.UTF16ToString(path[:size]), filepath.Join(system, "LogonUI.exe")) ||
		!user.User.Sid.IsWellKnown(windows.WinLocalSystemSid) || session != w.session || windows.WTSGetActiveConsoleSessionId() != session {
		return 0, errors.New("credential provider must be the active console LogonUI")
	}
	verified = true
	return process, nil
}

// Convert directly into the bounded, mutable UTF-16LE pipe message.
func loginPasswordFrame(b []byte) ([]byte, error) {
	msg := make([]byte, 2, 514)
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		if r == 0 || (r == utf8.RuneError && size == 1) || len(msg)+utf16.RuneLen(r)*2 > cap(msg) {
			clear(msg)
			return nil, errors.New("invalid Windows password")
		}
		b = b[size:]
		if r > 0xffff {
			a, c := utf16.EncodeRune(r)
			msg = binary.LittleEndian.AppendUint16(msg, uint16(a))
			msg = binary.LittleEndian.AppendUint16(msg, uint16(c))
		} else {
			msg = binary.LittleEndian.AppendUint16(msg, uint16(r))
		}
	}
	if len(msg) == 2 {
		return nil, errors.New("empty Windows password")
	}
	binary.LittleEndian.PutUint16(msg, uint16((len(msg)-2)/2))
	return msg, nil
}
