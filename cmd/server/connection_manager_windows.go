package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"runtime"
	"time"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"

	"jumpvnc/internal/sshgateway"
)

const managerPipe = `\\.\pipe\JumpVNC-RemoteConnections-v1`

var pipeClientSession = kernel32.NewProc("GetNamedPipeClientSessionId")

// This pipe admits interactive local clients without granting pipe-instance creation.
func listenConnectionManager(ctx context.Context, connections *sshgateway.Connections) (net.Listener, error) {
	listener, err := winio.ListenPipe(managerPipe, &winio.PipeConfig{SecurityDescriptor: "D:P(A;;GA;;;SY)(A;;0x12019b;;;IU)"})
	if err != nil {
		return nil, fmt.Errorf("open connection manager: %w", err)
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			func() {
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(time.Second))
				var ids []string
				if json.NewDecoder(io.LimitReader(conn, 4<<20)).Decode(&ids) != nil {
					return
				}
				var session uint32
				ok, _, _ := pipeClientSession.Call(conn.(interface{ Fd() uintptr }).Fd(), uintptr(unsafe.Pointer(&session)))
				if ok == 0 || session == 0 || session == 0xffffffff || (session != windows.WTSGetActiveConsoleSessionId() && !adminPipeClient(conn)) || ctx.Err() != nil {
					json.NewEncoder(conn).Encode(managerReply{Error: "请在当前控制台桌面打开，或以管理员身份管理。"})
					return
				}
				connections.Disconnect(ids)
				json.NewEncoder(conn).Encode(managerReply{Connections: connections.List()})
			}()
		}
	}()
	return listener, nil
}

type managerReply struct {
	Connections []sshgateway.RemoteConnection
	Error       string
}

func (t *windowsTray) remoteConnections(ids []string) ([]sshgateway.RemoteConnection, error) {
	if t.registration == nil {
		t.connections.Disconnect(ids)
		return t.connections.List(), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 450*time.Millisecond)
	defer cancel()
	conn, err := winio.DialPipeAccessImpLevel(ctx, managerPipe, windows.FILE_READ_DATA|windows.FILE_WRITE_DATA|windows.SYNCHRONIZE, winio.PipeImpLevelIdentification)
	if err != nil {
		return nil, errors.New("无法连接服务，请检查 JumpVNC 服务状态和版本。")
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	var pid uint32
	service, err := openManagedWindowsService(windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return nil, err
	}
	defer service.Close()
	status, err := service.Query()
	if err != nil || windows.GetNamedPipeServerProcessId(windows.Handle(conn.(interface{ Fd() uintptr }).Fd()), &pid) != nil || status.ProcessId == 0 || status.ProcessId != pid {
		return nil, errors.New("无法验证 JumpVNC 服务进程。")
	}
	if err := json.NewEncoder(conn).Encode(ids); err != nil {
		return nil, err
	}
	var reply managerReply
	if err := json.NewDecoder(io.LimitReader(conn, 32<<20)).Decode(&reply); err != nil {
		return nil, err
	}
	if reply.Error != "" {
		return nil, errors.New(reply.Error)
	}
	return reply.Connections, nil
}

// Windows verifies the identity attached to the last pipe message. A filtered
// UAC token outside the physical console does not grant management access.
func adminPipeClient(conn net.Conn) bool {
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return false
	}
	runtime.LockOSThread()
	impersonate := windows.NewLazySystemDLL("advapi32.dll").NewProc("ImpersonateNamedPipeClient")
	ok, _, _ := impersonate.Call(conn.(interface{ Fd() uintptr }).Fd())
	if ok == 0 {
		runtime.UnlockOSThread()
		return false
	}
	member, err := windows.GetCurrentThreadToken().IsMember(admins)
	if revert := windows.RevertToSelf(); revert != nil {
		panic("cannot revert pipe client identity")
	}
	runtime.UnlockOSThread()
	return err == nil && member
}
