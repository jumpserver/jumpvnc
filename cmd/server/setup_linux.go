package main

import (
	"errors"
	"os"
	"path/filepath"

	"jumpvnc/internal/config"
)

func initializePlatformService(name string) error {
	if os.Geteuid() == 0 {
		return errors.New("initialize Linux desktop credentials as the desktop user, not root")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	name, err = filepath.Abs(name)
	if err != nil {
		return err
	}
	if err := initializeServiceFilesWithBackend(name, home, "/opt/jumpvnc/backends/linux/svnc-desktop"); err != nil {
		return err
	}
	_, err = config.Load(name)
	return err
}
