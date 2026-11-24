package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

func openManagedWindowsService(access uint32) (*mgr.Service, error) {
	manager, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return nil, err
	}
	defer windows.CloseServiceHandle(manager)
	handle, err := windows.OpenService(manager, windows.StringToUTF16Ptr(windowsServiceName), access)
	if err != nil {
		return nil, err
	}
	return &mgr.Service{Name: windowsServiceName, Handle: handle}, nil
}

type serviceRegistration struct{ configPath, runUser string }

func installedServiceRegistration() (serviceRegistration, error) {
	service, err := openManagedWindowsService(windows.SERVICE_QUERY_CONFIG)
	if err != nil {
		return serviceRegistration{}, err
	}
	defer service.Close()
	cfg, err := service.Config()
	if err != nil {
		return serviceRegistration{}, err
	}
	path, err := registeredServiceConfigPath(cfg.BinaryPathName)
	return serviceRegistration{configPath: path, runUser: cfg.ServiceStartName}, err
}

// The configuration comes from protected SCM registration, not a caller's -config.
func registeredServiceConfigPath(command string) (string, error) {
	args, err := windows.DecomposeCommandLine(command)
	if err != nil {
		return "", err
	}
	var path string
	service := false
	for i := 1; i < len(args); i++ {
		switch {
		case args[i] == "-service" || args[i] == "--service" || args[i] == "-service=true":
			service = true
		case args[i] == "-config" || args[i] == "--config":
			if i+1 < len(args) {
				i++
				path = args[i]
			}
		case strings.HasPrefix(args[i], "-config="):
			path = strings.TrimPrefix(args[i], "-config=")
		case strings.HasPrefix(args[i], "--config="):
			path = strings.TrimPrefix(args[i], "--config=")
		}
	}
	if !service || !filepath.IsAbs(path) {
		return "", errors.New("installed service must specify -service and an absolute -config path")
	}
	return filepath.Clean(path), nil
}

type windowsServiceBackend struct{}

func (windowsServiceBackend) query() (serviceState, error) {
	service, err := openManagedWindowsService(windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return serviceUnavailable, err
	}
	defer service.Close()
	status, err := service.Query()
	if err != nil {
		return serviceUnavailable, err
	}
	switch status.State {
	case svc.Stopped:
		return serviceStopped, nil
	case svc.StartPending, svc.ContinuePending:
		return serviceStarting, nil
	case svc.StopPending:
		return serviceStopping, nil
	case svc.Running:
		return serviceRunning, nil
	default:
		return serviceUnavailable, fmt.Errorf("unsupported Windows service state: %d", status.State)
	}
}

func (windowsServiceBackend) start(ctx context.Context) error {
	service, err := openManagedWindowsService(windows.SERVICE_QUERY_STATUS | windows.SERVICE_START)
	if err != nil {
		return err
	}
	defer service.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := service.Start(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
		return err
	}
	return waitManagedWindowsService(ctx, service, svc.Running)
}

func (windowsServiceBackend) stop(ctx context.Context) error {
	service, err := openManagedWindowsService(windows.SERVICE_QUERY_STATUS | windows.SERVICE_STOP)
	if err != nil {
		return err
	}
	defer service.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := service.Control(svc.Stop); err != nil && !errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
		return err
	}
	return waitManagedWindowsService(ctx, service, svc.Stopped)
}

func waitManagedWindowsService(ctx context.Context, service *mgr.Service, want svc.State) error {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		status, err := service.Query()
		if err != nil {
			return err
		}
		if status.State == want {
			return nil
		}
		if want == svc.Running && status.State == svc.Stopped {
			return fmt.Errorf("Windows service failed to start (exit %d/%d); see service logs", status.Win32ExitCode, status.ServiceSpecificExitCode)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
