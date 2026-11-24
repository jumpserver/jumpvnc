package main

import (
	"testing"

	"jumpvnc/internal/config"
)

func TestServiceAllowsScopedFilesWithoutEnablingOtherServices(t *testing.T) {
	cfg := config.Config{
		SSH:  config.SSH{AllowVNC: true},
		VNC:  config.VNC{Engine: "libvncserver"},
		SFTP: config.SFTP{Root: t.TempDir()},
	}
	for _, mode := range []string{"disabled", "read_only", "write_only", "read_write"} {
		cfg.SFTP.Mode = mode
		if err := validateServiceBackend(cfg); err != nil {
			t.Fatalf("explicit root with mode %s: %v", mode, err)
		}
	}
	cfg.VNC.Compatibility.Enabled = true
	if validateServiceBackend(cfg) == nil {
		t.Fatal("file access must not enable the unencrypted VNC compatibility listener")
	}
}

func TestServiceRejectsImplicitServiceHome(t *testing.T) {
	cfg := config.Config{
		SSH:  config.SSH{AllowVNC: true},
		VNC:  config.VNC{Engine: "libvncserver"},
		SFTP: config.SFTP{Mode: "read_write"},
	}
	for _, root := range []string{"", "~", "relative"} {
		cfg.SFTP.Root = root
		if validateServiceBackend(cfg) == nil {
			t.Fatalf("accepted implicit/unresolved root %q", root)
		}
	}
	cfg.SFTP.Root = t.TempDir()
	cfg.SFTP.Home = true // config.Load resolved '~' to the service account's HOME.
	if validateServiceBackend(cfg) == nil {
		t.Fatal("accepted the resolved service profile")
	}
	cfg.SFTP.Mode = "disabled"
	if err := validateServiceBackend(cfg); err != nil {
		t.Fatalf("disabled file access should retain existing service behavior: %v", err)
	}
}
