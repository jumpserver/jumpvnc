package main

import (
	"context"
	"os"
	"runtime"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// Runs in the logged-in user's session during explicit deployment acceptance.
func TestInstalledServiceTrayUI(t *testing.T) {
	if os.Getenv("SVNC_TEST_SERVICE_TRAY_UI") != "1" {
		t.Skip("set SVNC_TEST_SERVICE_TRAY_UI=1 in an interactive session")
	}
	if !interactiveDesktop() {
		t.Fatal("tray acceptance requires an interactive desktop")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	registration, err := installedServiceRegistration()
	if err != nil {
		t.Fatal(err)
	}
	tray := windowsTray{configPath: registration.configPath, registration: &registration}
	tray.service = newManagedService(t.Context(), windowsServiceBackend{}, func() {})
	defer tray.service.close()
	if err := tray.createWindow(); err != nil {
		t.Fatal(err)
	}
	defer destroyWindow.Call(tray.window)
	tray.icon, _, _ = loadIcon.Call(0, idiApplication)
	tray.refreshIcon()
	defer tray.notify(nimDelete, tray.iconData())
	if !tray.notify(nimModify, tray.iconData()) {
		t.Fatal("Explorer did not register the notification icon")
	}
	if err := tray.createPanel(); err != nil {
		t.Fatal(err)
	}
	defer destroyWindow.Call(tray.panel.window)
	tray.updatePanel()
	tray.refreshPanelSettings(false)
	enabled := func(control uintptr) bool {
		value, _, _ := user32.NewProc("IsWindowEnabled").Call(control)
		return value != 0
	}
	if enabled(tray.panel.compatibilityToggle) || enabled(tray.panel.rootInput) {
		t.Fatal("system-service panel offered unsupported access modes")
	}
	if !tray.canConfigure() {
		if enabled(tray.panel.stop) || enabled(tray.panel.configEdit) || enabled(tray.panel.passwordShow) || enabled(tray.panel.passwordInput) {
			t.Fatal("unelevated tray exposed privileged controls")
		}
		if !enabled(tray.panel.admin) {
			t.Fatal("administrator management action is unavailable")
		}
	} else if !enabled(tray.panel.passwordInput) {
		t.Fatal("administrator cannot configure the service password")
	}
	t.Logf("notification icon and management controls passed; administrator=%t", tray.canConfigure())
}

func TestRegisteredServiceConfigPath(t *testing.T) {
	for _, tc := range []struct{ command, want string }{
		{`"C:\Program Files\SuperVNC\bin\sshvnc.exe" -service -config "C:\Program Files\SuperVNC\service.yaml"`, `C:\Program Files\SuperVNC\service.yaml`},
		{`C:\svnc\sshvnc.exe -config=C:\svnc\service.yaml -service`, `C:\svnc\service.yaml`},
		{`C:\svnc\sshvnc.exe -service -config service.yaml`, ""},
		{`C:\svnc\sshvnc.exe -config C:\svnc\config.yaml`, ""},
		{`C:\svnc\sshvnc.exe -service`, ""},
	} {
		got, err := registeredServiceConfigPath(tc.command)
		if got != tc.want || (err != nil) != (tc.want == "") {
			t.Errorf("%q: got %q, %v", tc.command, got, err)
		}
	}
}

// Explicit deployment acceptance only: never stops an installed service in a normal test run.
func TestInstalledWindowsServiceManagement(t *testing.T) {
	if os.Getenv("SVNC_TEST_MANAGED_SERVICE") != "1" {
		t.Skip("set SVNC_TEST_MANAGED_SERVICE=1 for deployment acceptance")
	}
	registration, err := installedServiceRegistration()
	if err != nil || registration.configPath == "" {
		t.Fatalf("registered configuration: %v", err)
	}
	service, err := openManagedWindowsService(windows.SERVICE_QUERY_STATUS)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	before, err := service.Query()
	if err != nil {
		t.Fatal(err)
	}
	updates := make(chan struct{}, 4)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	s := newManagedService(ctx, windowsServiceBackend{}, func() { updates <- struct{}{} })
	if s.currentState() != serviceRunning {
		t.Fatal("acceptance requires an initially running service")
	}
	s.close()
	after, err := service.Query()
	if err != nil || before.ProcessId != after.ProcessId {
		t.Fatal("closing the manager interrupted the running service")
	}
	s = newManagedService(ctx, windowsServiceBackend{}, func() { updates <- struct{}{} })
	defer s.close()
	t.Cleanup(func() {
		restore, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		if err := (windowsServiceBackend{}).start(restore); err != nil {
			t.Error(err)
		}
	})
	await := func() {
		t.Helper()
		select {
		case <-updates:
		case <-time.After(50 * time.Second):
			t.Fatal("SCM operation timed out")
		}
		s.update()
	}
	s.stop(false)
	await()
	if s.currentState() != serviceStopped || s.currentError() != nil {
		t.Fatalf("stop: %v", s.currentError())
	}
	s.start()
	await()
	if s.currentState() != serviceRunning || s.currentError() != nil {
		t.Fatalf("start: %v", s.currentError())
	}
	s.stop(true)
	await()
	if s.currentState() != serviceRunning || s.currentError() != nil {
		t.Fatalf("restart: %v", s.currentError())
	}
	s.close()
	after, err = service.Query()
	if err != nil || after.ProcessId == 0 {
		t.Fatal("exiting the manager stopped the service")
	}
	t.Log("SCM stop/start/restart passed; closing the tray leaves the service running")
}
