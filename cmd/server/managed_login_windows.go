package main

import (
	"encoding/json"
	"errors"

	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"jumpvnc/internal/sshgateway"
)

func loadManagedLogin(configPath string) (*sshgateway.ManagedLogin, error) {
	root, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, 0)
	if err != nil {
		return nil, err
	}
	root = filepath.Join(root, "SuperVNC")
	if !strings.EqualFold(configPath, filepath.Join(root, "service.yaml")) {
		return nil, nil
	}
	data, err := os.ReadFile(filepath.Join(root, "managed-account.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state struct {
		SID, Fingerprint string
		Encrypted        []byte
	}
	if json.Unmarshal(data, &state) != nil || !strings.HasPrefix(state.Fingerprint, "SHA256:") || len(state.Encrypted) == 0 || len(state.Encrypted) > 8192 {
		return nil, errors.New("invalid managed account record")
	}
	expected, err := windows.StringToSid(state.SID)
	if err != nil {
		return nil, err
	}
	computer, err := windows.ComputerName()
	if err != nil {
		return nil, err
	}
	check := func() bool {
		actual, domain, kind, err := windows.LookupSID("", computer+`\`+sshgateway.ManagedAccount)
		if err != nil || !strings.EqualFold(domain, computer) || kind != windows.SidTypeUser || !expected.Equals(actual) {
			return false
		}
		groups, err := (&user.User{Uid: state.SID}).GroupIds()
		return err == nil && slices.Contains(groups, "S-1-5-32-545") && !slices.Contains(groups, "S-1-5-32-544")
	}
	return &sshgateway.ManagedLogin{Fingerprint: state.Fingerprint, Valid: check, Password: func() ([]byte, error) {
		if !check() {
			return nil, errors.New("managed account identity changed")
		}
		in := windows.DataBlob{Size: uint32(len(state.Encrypted)), Data: &state.Encrypted[0]}
		var out windows.DataBlob
		if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
			return nil, err
		}
		defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
		plain := unsafe.Slice(out.Data, out.Size)
		defer clear(plain)
		if len(plain) == 0 || len(plain) > 1024 {
			return nil, errors.New("invalid managed password length")
		}
		return append([]byte(nil), plain...), nil
	}}, nil
}
