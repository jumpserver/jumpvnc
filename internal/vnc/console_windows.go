package vnc

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"jumpvnc/internal/config"
)

// StartConsole launches the backend in the physical console session. The SCM
// runs the Agent as LocalSystem in Session 0, which has no interactive desktop.
func StartConsole(ctx context.Context, cfg config.VNC, log *slog.Logger, logPath string) (Backend, error) {
	cmd, err := command(ctx, cfg)
	if err != nil {
		return Backend{}, err
	}
	probe, err := net.Listen("tcp4", cfg.Listen)
	if err != nil {
		return Backend{}, fmt.Errorf("VNC backend port is unavailable: %w", err)
	}
	probe.Close()

	startup, cancel := context.WithTimeout(ctx, time.Duration(cfg.StartupTimeoutSeconds)*time.Second)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var session uint32
	for {
		session = windows.WTSGetActiveConsoleSessionId()
		if session != 0 && session != ^uint32(0) {
			break
		}
		select {
		case <-startup.Done():
			return Backend{}, fmt.Errorf("waiting for Windows console session: %w", startup.Err())
		case <-ticker.C:
		}
	}

	event, eventName, err := consoleSASEvent()
	if err != nil {
		return Backend{}, fmt.Errorf("create console SAS event: %w", err)
	}
	args := append(cmd.Args, "--console-session", fmt.Sprint(session), "--log-file", logPath, "--sas-event", eventName)
	child, err := launchConsoleProcess(session, cmd.Path, args)
	if err != nil {
		windows.CloseHandle(event)
		return Backend{}, fmt.Errorf("launch VNC backend in Windows console session %d: %w", session, err)
	}
	process := child.Process
	exited, waited := make(chan error, 1), make(chan struct{})
	go func() {
		defer close(waited)
		exited <- waitConsoleProcess(child.Process, event, session, log, logPath)
	}()
	stop := func() {
		_ = windows.TerminateProcess(process, 0)
		<-waited
		windows.CloseHandle(process)
		windows.CloseHandle(event)
	}
	dial := func(ctx context.Context) (*net.TCPConn, error) {
		return dialConsoleProcess(ctx, cfg.Listen, child.ProcessId)
	}
	if err := waitForRFB(startup, cfg, exited, func(ctx context.Context) (net.Conn, error) { return dial(ctx) }); err != nil {
		stop()
		return Backend{}, err
	}
	log.Info("vnc.ready", "engine", cfg.Engine, "listen", cfg.Listen, "console_session", session)
	backend := Backend{Exited: exited, Stop: stop, Dial: dial}
	if !cfg.ViewOnly {
		backend.Login = windowsLogin{session: session, log: log}
	}
	return backend, nil
}

func launchConsoleProcess(session uint32, executable string, args []string) (windows.ProcessInformation, error) {
	var process windows.ProcessInformation
	application, err := windows.UTF16PtrFromString(executable)
	if err != nil {
		return process, err
	}
	commandLine, err := windows.UTF16FromString(windows.ComposeCommandLine(args))
	if err != nil {
		return process, err
	}
	directory, err := windows.UTF16PtrFromString(filepath.Dir(executable))
	if err != nil {
		return process, err
	}
	var serviceToken windows.Token
	err = windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE|windows.TOKEN_ADJUST_PRIVILEGES, &serviceToken)
	if err != nil {
		return process, err
	}
	defer serviceToken.Close()
	var privilege windows.Tokenprivileges
	privilege.PrivilegeCount = 1
	name, _ := windows.UTF16PtrFromString("SeTcbPrivilege")
	if err := windows.LookupPrivilegeValue(nil, name, &privilege.Privileges[0].Luid); err != nil {
		return process, err
	}
	privilege.Privileges[0].Attributes = windows.SE_PRIVILEGE_ENABLED
	var previous windows.Tokenprivileges
	var previousSize uint32
	if err := windows.AdjustTokenPrivileges(serviceToken, false, &privilege, uint32(unsafe.Sizeof(previous)), &previous, &previousSize); err != nil {
		return process, err
	}
	defer windows.AdjustTokenPrivileges(serviceToken, false, &previous, 0, nil, nil)

	var token windows.Token
	if err := windows.DuplicateTokenEx(serviceToken, windows.MAXIMUM_ALLOWED, nil, windows.SecurityImpersonation, windows.TokenPrimary, &token); err != nil {
		return process, err
	}
	defer token.Close()
	if err := windows.SetTokenInformation(token, windows.TokenSessionId, (*byte)(unsafe.Pointer(&session)), uint32(unsafe.Sizeof(session))); err != nil {
		return process, err
	}
	desktop, _ := windows.UTF16PtrFromString(`winsta0\Winlogon`)
	startup := windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfo{})), Desktop: desktop}
	if err := windows.CreateProcessAsUser(token, application, &commandLine[0], nil, nil, false, windows.CREATE_NO_WINDOW, nil, directory, &startup, &process); err != nil {
		return process, err
	}
	windows.CloseHandle(process.Thread)
	return process, nil
}

func consoleSASEvent() (windows.Handle, string, error) {
	name := `Global\JumpVNC-SAS-` + rand.Text()
	name16, _ := windows.UTF16PtrFromString(name)
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;SY)")
	if err != nil {
		return 0, "", err
	}
	attributes := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
	event, err := windows.CreateEvent(&attributes, 0, 0, name16)
	if err != nil && event != 0 {
		windows.CloseHandle(event)
	}
	return event, name, err
}

func waitConsoleProcess(process, sasEvent windows.Handle, session uint32, log *slog.Logger, logPath string) error {
	handles := []windows.Handle{process, sasEvent}
	var previousSAS time.Time
	for {
		result, err := windows.WaitForMultipleObjects(handles, false, windows.INFINITE)
		if err != nil {
			return err
		}
		if result == windows.WAIT_OBJECT_0 {
			var code uint32
			if err := windows.GetExitCodeProcess(process, &code); err != nil {
				return err
			}
			if code != 0 {
				return fmt.Errorf("exit status %d; see %s", code, logPath)
			}
			return nil
		}
		active := windows.WTSGetActiveConsoleSessionId()
		requestLog := log.With("console_session", session, "active_session", active)
		requestLog.Info("vnc.console.sas_received")
		if active != session {
			requestLog.Info("vnc.console.sas_skipped", "reason", "console_changed")
			continue
		}
		if time.Since(previousSAS) < time.Second {
			requestLog.Info("vnc.console.sas_skipped", "reason", "rate_limit")
			continue
		}
		previousSAS = time.Now()
		if err := sendConsoleSAS(requestLog); err != nil {
			requestLog.Warn("vnc.console.sas_failed", "error", err)
		}
	}
}

var sendSAS = windows.NewLazySystemDLL("sas.dll").NewProc("SendSAS")

// This backend serves only the active physical console. Request SAS as the SCM service.
func sendConsoleSAS(log *slog.Logger) error {
	if err := sendSAS.Find(); err != nil {
		return err
	}
	log.Info("vnc.console.sas_call", "caller", "service")
	sendSAS.Call(0)
	log.Info("vnc.console.sas_api_returned", "caller", "service")
	return nil
}

var getExtendedTCPTable = windows.NewLazySystemDLL("iphlpapi.dll").NewProc("GetExtendedTcpTable")

func dialConsoleProcess(ctx context.Context, address string, pid uint32) (*net.TCPConn, error) {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp4", address)
	if err != nil {
		return nil, err
	}
	tcp := conn.(*net.TCPConn)
	if err := checkConsolePeer(tcp, pid); err != nil {
		tcp.Close()
		return nil, err
	}
	return tcp, nil
}

func checkConsolePeer(conn *net.TCPConn, pid uint32) error {
	remote := conn.RemoteAddr().(*net.TCPAddr).AddrPort()
	local := conn.LocalAddr().(*net.TCPAddr).AddrPort()
	remoteIP, localIP := remote.Addr().Unmap().As4(), local.Addr().Unmap().As4()
	var size uint32
	const tcpTableOwnerPIDConnections = 4
	const afInet = windows.AF_INET
	for range 4 {
		var data []byte
		var buffer *byte
		if size > 16*1024*1024 {
			break
		}
		if size != 0 {
			data = make([]byte, size)
			buffer = &data[0]
		}
		result, _, _ := getExtendedTCPTable.Call(uintptr(unsafe.Pointer(buffer)), uintptr(unsafe.Pointer(&size)), 0, afInet, tcpTableOwnerPIDConnections, 0)
		if result == uintptr(windows.ERROR_INSUFFICIENT_BUFFER) {
			continue
		}
		if result != 0 {
			return windows.Errno(result)
		}
		// MIB_TCPTABLE_OWNER_PID starts with a count followed by 24-byte rows.
		const rowSize = 24
		for offset := 4; offset+rowSize <= len(data) && uint32((offset-4)/rowSize) < binary.LittleEndian.Uint32(data[:4]); offset += rowSize {
			row := data[offset : offset+rowSize]
			if binary.LittleEndian.Uint32(row[20:24]) == pid && [4]byte(row[4:8]) == remoteIP &&
				binary.BigEndian.Uint16(row[8:10]) == remote.Port() && [4]byte(row[12:16]) == localIP &&
				binary.BigEndian.Uint16(row[16:18]) == local.Port() {
				return nil
			}
		}
		break
	}
	return errors.New("VNC loopback connection is not owned by the console backend")
}
