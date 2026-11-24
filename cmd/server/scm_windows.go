package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/svc"

	"jumpvnc/internal/config"
	"jumpvnc/internal/vnc"
)

// Keep the SCM identity so existing Windows service installations can be upgraded.
const windowsServiceName = "SuperVNC"

func runWindowsService(ctx context.Context, name string) error {
	name, err := filepath.Abs(name)
	if err != nil {
		return err
	}
	return svc.Run(windowsServiceName, windowsService{ctx: ctx, configPath: name})
}

type windowsService struct {
	ctx        context.Context
	configPath string
}

func (s windowsService) Execute(_ []string, commands <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	current := svc.Status{State: svc.StartPending, CheckPoint: 1, WaitHint: 5000}
	status <- current
	file, err := os.OpenFile(s.configPath+".service.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return true, 1
	}
	defer file.Close()
	log := slog.New(slog.NewJSONHandler(file, nil)).With("product", productName)
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- runWithBackend(ctx, s.configPath, log, func() { close(ready) }, func(ctx context.Context, cfg config.Config, log *slog.Logger) (vnc.Backend, error) {
			if err := validateServiceBackend(cfg); err != nil {
				return vnc.Backend{}, err
			}
			return vnc.StartConsole(ctx, cfg.VNC, log, s.configPath+".desktop.log")
		})
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ready:
			ready = nil
			if current.State == svc.StartPending {
				current = svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
				status <- current
			}
		case request := <-commands:
			switch request.Cmd {
			case svc.Interrogate:
				status <- current
			case svc.Stop, svc.Shutdown:
				current = svc.Status{State: svc.StopPending, CheckPoint: 1, WaitHint: 5000}
				status <- current
				cancel()
			}
		case <-ticker.C:
			if current.State == svc.StartPending || current.State == svc.StopPending {
				current.CheckPoint++
				status <- current
			}
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) && ctx.Err() == nil {
				log.Error("service.error", "error", err)
				return true, 1
			}
			return false, 0
		}
	}
}
