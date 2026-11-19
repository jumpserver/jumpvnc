// Package vnc manages the life cycle of the configured VNC backend.
package vnc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"sshvnc/internal/config"
)

// Start waits for the configured RFB endpoint. An external backend remains
// under its own supervisor. A managed backend reports termination on exited.
func Start(ctx context.Context, cfg config.VNC, log *slog.Logger) (exited <-chan error, stop func(), err error) {
	var processDone chan error
	stop = func() {}
	if cfg.Engine != "external" {
		cmd, err := command(ctx, cfg)
		if err != nil {
			return nil, nil, err
		}
		probe, err := net.Listen("tcp4", cfg.Listen)
		if err != nil {
			return nil, nil, fmt.Errorf("VNC backend port is unavailable: %w", err)
		}
		probe.Close()

		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			return nil, nil, fmt.Errorf("launch %s: %w", cfg.Engine, err)
		}
		processDone = make(chan error, 1)
		waited := make(chan struct{})
		go func() {
			processDone <- cmd.Wait()
			close(waited)
		}()
		stop = func() {
			cmd.Process.Kill()
			<-waited
		}
	}
	if err := waitReady(ctx, cfg, processDone); err != nil {
		stop()
		return nil, nil, err
	}
	log.Info("vnc.ready", "engine", cfg.Engine, "listen", cfg.Listen)
	return processDone, stop, nil
}

func command(ctx context.Context, cfg config.VNC) (*exec.Cmd, error) {
	switch cfg.Engine {
	case "tigervnc":
		executable := cfg.Executable
		if executable == "" {
			// Debian's x0vncserver is a wrapper that may daemonize.
			executable, _ = exec.LookPath("X0tigervnc")
			if executable == "" {
				executable = "x0vncserver"
			}
		}
		info, err := os.Stat(cfg.PasswordFile)
		if err != nil {
			return nil, fmt.Errorf("TigerVNC password file: %w", err)
		}
		if !info.Mode().IsRegular() || info.Size() < 8 || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
			return nil, errors.New("TigerVNC password file must be a private vncpasswd file")
		}
		_, port, _ := net.SplitHostPort(cfg.Listen)
		args := []string{"-interface", "127.0.0.1", "-UseIPv6=0", "-rfbport", port,
			"-SecurityTypes", "VncAuth", "-PasswordFile", cfg.PasswordFile}
		if cfg.ViewOnly {
			args = append(args, "-AcceptKeyEvents=0", "-AcceptPointerEvents=0", "-AcceptCutText=0")
		}
		return exec.CommandContext(ctx, executable, args...), nil
	case "ultravnc":
		if runtime.GOOS != "windows" {
			return nil, errors.New("UltraVNC can only run on Windows")
		}
		executable, err := filepath.EvalSymlinks(cfg.Executable)
		if err != nil {
			return nil, fmt.Errorf("UltraVNC executable: %w", err)
		}
		dir := filepath.Dir(executable)
		if err := checkUltraVNC(cfg, dir); err != nil {
			return nil, err
		}
		cmd := exec.CommandContext(ctx, executable, "-run")
		cmd.Dir = dir
		return cmd, nil
	default:
		return nil, fmt.Errorf("unknown managed VNC engine %q", cfg.Engine)
	}
}

func waitReady(ctx context.Context, cfg config.VNC, exited <-chan error) error {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(cfg.StartupTimeoutSeconds)*time.Second)
	defer cancel()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-exited:
			return ExitError(cfg.Engine, err)
		case <-ctx.Done():
			return fmt.Errorf("waiting for RFB at %s: %w", cfg.Listen, ctx.Err())
		case <-ticker.C:
			if isRFB(ctx, cfg.Listen) {
				return nil
			}
		}
	}
}

func isRFB(ctx context.Context, address string) bool {
	ctx, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
	defer cancel()
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp4", address)
	if err != nil {
		return false
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if err := conn.SetReadDeadline(deadline); err != nil {
		return false
	}
	var banner [12]byte
	if _, err := io.ReadFull(conn, banner[:]); err != nil {
		return false
	}
	return string(banner[:4]) == "RFB " && versionDigits(banner[4:7]) &&
		banner[7] == '.' && versionDigits(banner[8:11]) && banner[11] == '\n'
}

func versionDigits(b []byte) bool {
	for _, character := range b {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func ExitError(engine string, err error) error {
	if err != nil {
		return fmt.Errorf("%s VNC backend exited: %w", engine, err)
	}
	return fmt.Errorf("%s VNC backend exited", engine)
}
