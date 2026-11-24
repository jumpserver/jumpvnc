package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func initializePlatformService(name string) error {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return errors.New("service initialization requires administrator privileges")
	}
	programFiles, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, 0)
	if err != nil {
		return err
	}
	dir := filepath.Join(programFiles, "SuperVNC")
	name, err = filepath.Abs(name)
	if err != nil {
		return err
	}
	if !strings.EqualFold(name, filepath.Join(dir, "service.yaml")) {
		return errors.New("initialize the configuration in the protected installation directory")
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil || !strings.EqualFold(dir, resolved) {
		return errors.New("installation directory must not traverse a reparse point")
	}
	// Protect new credentials before creating them. The tray installer grants
	// ordinary users execution of only the manager and its launcher afterward.
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		return err
	}
	public, err := windows.KnownFolderPath(windows.FOLDERID_PublicDocuments, 0)
	if err != nil {
		return err
	}
	if err := initializeServiceFiles(name, filepath.Join(public, "JumpVNC Files")); err != nil {
		return err
	}
	// Old manually installed files may have explicit permissions. Protect the
	// credentials as well as their parent without changing other user files.
	for _, file := range []string{"service.yaml", "ssh_host_ed25519_key", connectionKeyName, "authorized_keys", "vnc-password.txt"} {
		path := filepath.Join(dir, file)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("installation credentials must be regular files")
		}
		if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
			windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
			return err
		}
	}
	return nil
}
