package vnc

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const switchKey = "Software\\JumpVNC"

func allowedSwitchSID(root registry.Key, path string) string {
	key, err := registry.OpenKey(root, path, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer key.Close()
	value, _, err := key.GetStringValue("AllowConsoleSwitchToSID")
	if err != nil {
		return ""
	}
	return value
}

func switchAllowed(owner, target *windows.SID) bool {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE,
		"SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\Policies\\System", registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer key.Close()
	hidden, _, err := key.GetIntegerValue("HideFastUserSwitching")
	if !errors.Is(err, registry.ErrNotExist) && (err != nil || hidden != 0) {
		return false
	}
	return allowedSwitchSID(registry.USERS, owner.String()+"\\"+switchKey) == target.String()
}

func switchEventName(session uint32, owner, target string) *uint16 {
	return windows.StringToUTF16Ptr(fmt.Sprintf("Global\\JumpVNC-Lock-%d-%s-%s", session, owner, target))
}

var logonUser = windows.NewLazySystemDLL("advapi32.dll").NewProc("LogonUserW")
var lockWorkStation = windows.NewLazySystemDLL("user32.dll").NewProc("LockWorkStation")

// Check the interactive logon right and password before asking the incumbent
// to lock. The returned token is never used to act as either desktop user.
func validateSwitchCredential(account string, target *windows.SID, frame []byte) error {
	name, err := windows.UTF16PtrFromString(strings.TrimPrefix(account, ".\\"))
	if err != nil {
		return err
	}
	secret := make([]uint16, len(frame)/2)
	defer clear(secret)
	for i := range len(secret) - 1 {
		secret[i] = binary.LittleEndian.Uint16(frame[2+i*2:])
	}
	var token windows.Token
	ok, _, err := logonUser.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("."))),
		uintptr(unsafe.Pointer(&secret[0])), 2 /* LOGON32_LOGON_INTERACTIVE */, 0, uintptr(unsafe.Pointer(&token)))
	if ok == 0 {
		return fmt.Errorf("Windows interactive authentication: %w", err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil || !target.Equals(user.User.Sid) {
		return errors.New("Windows verified a different account")
	}
	return nil
}

func (w windowsLogin) prepareUserSwitch(ctx context.Context, owner, target *windows.SID, account string, frame []byte) error {
	if !w.Available() || !switchAllowed(owner, target) {
		return errors.New("the console owner has not enabled switching to this account, or Windows policy prevents it")
	}
	if err := validateSwitchCredential(account, target, frame); err != nil {
		return err
	}
	state, ok := w.lockState()
	current, err := consoleUser(w.session)
	if !ok || state > 1 || err != nil || current == nil || !owner.Equals(current) || ctx.Err() != nil {
		return errors.New("console changed during authentication")
	}
	if state == 0 {
		return nil
	}
	descriptor, err := windows.SecurityDescriptorFromString("O:SYD:P(A;;GA;;;SY)(A;;0x00120000;;;" + owner.String() + ")")
	if err != nil {
		return err
	}
	attributes := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
	request, err := windows.CreateEvent(&attributes, 0, 1, switchEventName(w.session, owner.String(), target.String()))
	if request != 0 {
		defer windows.CloseHandle(request)
	}
	if err != nil {
		return fmt.Errorf("create session lock request: %w", err)
	}
	w.log.Info("windows_login.switch_lock_requested", "console_session", w.session)
	deadline, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	poll := time.NewTicker(150 * time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case <-deadline.Done():
			return errors.New("the signed-in user's JumpVNC tray did not confirm a locked console")
		case <-poll.C:
			state, ok := w.lockState()
			if !ok || state > 1 {
				return errors.New("console changed while waiting for Windows to lock")
			}
			if state == 0 {
				return nil
			}
		}
	}
}

// Called on the ordinary tray's interactive desktop. Only its own local
// preference and a request created by LocalSystem can trigger a self-lock.
func PollConsoleSwitch() error {
	target := allowedSwitchSID(registry.CURRENT_USER, switchKey)
	if target == "" {
		return nil
	}
	targetSID, err := windows.StringToSid(target)
	if err != nil || targetSID.String() != target {
		return nil
	}
	var session uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &session); err != nil ||
		session == 0 || session != windows.WTSGetActiveConsoleSessionId() {
		return nil
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	name := switchEventName(session, user.User.Sid.String(), target)
	event, err := windows.OpenEvent(windows.SYNCHRONIZE|windows.READ_CONTROL, false, name)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return nil
	}
	if err != nil {
		return err
	}
	defer windows.CloseHandle(event)
	desc, err := windows.GetSecurityInfo(event, windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := desc.Owner()
	if err != nil || owner == nil || !owner.IsWellKnown(windows.WinLocalSystemSid) {
		return errors.New("ignoring a lock event not owned by LocalSystem")
	}
	signaled, err := windows.WaitForSingleObject(event, 0)
	if err != nil || signaled != windows.WAIT_OBJECT_0 {
		return err
	}
	if session == windows.WTSGetActiveConsoleSessionId() && switchAllowed(user.User.Sid, targetSID) {
		if ok, _, err := lockWorkStation.Call(); ok == 0 {
			return err
		}
	}
	return nil
}
