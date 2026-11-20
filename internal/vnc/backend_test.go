package vnc

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"testing"

	"sshvnc/internal/config"
)

func TestLibVNCCommand(t *testing.T) {
	password := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(password, []byte("svnctest\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.VNC{Engine: "libvncserver", Listen: "127.0.0.1:5900", Executable: "svnc-desktop", PasswordFile: password, ViewOnly: true}
	cmd, err := command(context.Background(), cfg)
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		if err == nil {
			t.Fatal("accepted an unsupported native desktop platform")
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"svnc-desktop", "--listen", cfg.Listen, "--password-file", password, "--parent-pid", strconv.Itoa(os.Getpid()), "--view-only"}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("backend arguments = %q, want %q", cmd.Args, want)
	}
}

func TestDesktopPassword(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		valid      bool
	}{
		{"plain", "test1234", true}, {"lf", "test1234\n", true},
		{"crlf", "test1234\r\n", true}, {"spaces", " pass ", true},
		{"empty", "", false}, {"long", "test12345", false},
		{"nul", "abc\x00", false}, {"unicode", "中文", false},
		{"extra_line", "abc\ndef", false}, {"extra_newline", "abc\n\n", false},
		{"oversize", "12345678901\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name := filepath.Join(t.TempDir(), "password")
			if err := os.WriteFile(name, []byte(tc.text), 0600); err != nil {
				t.Fatal(err)
			}
			if err := checkDesktopPassword(name); (err == nil) != tc.valid {
				t.Fatalf("valid=%v, got %v", tc.valid, err)
			}
		})
	}
}
