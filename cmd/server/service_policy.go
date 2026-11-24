package main

import (
	"fmt"
	"path/filepath"

	"sshvnc/internal/config"
)

// A service may expose an explicitly scoped file directory. Its account's
// HOME is not the interactive desktop user's HOME (LocalSystem has its own
// system profile), so the foreground-mode default must not silently apply.
func validateServiceBackend(cfg config.Config) error {
	if !cfg.SSH.AllowVNC || cfg.VNC.Engine != "libvncserver" || cfg.VNC.Compatibility.Enabled {
		return fmt.Errorf("service mode requires ssh.allow_vnc, the libvncserver backend, and disabled plain VNC compatibility")
	}
	if cfg.SFTP.Mode != "disabled" &&
		(cfg.SFTP.Home || !filepath.IsAbs(cfg.SFTP.Root)) {
		return fmt.Errorf("service SFTP requires an explicit root directory outside the service account's HOME")
	}
	return nil
}
