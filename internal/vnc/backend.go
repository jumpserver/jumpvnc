// Package vnc manages the life cycle of the configured VNC backend.
package vnc

import (
	"bytes"
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
	"strings"
	"time"

	"jumpvnc/internal/config"
)

type Backend struct {
	Exited <-chan error
	Stop   func()
	Dial   func(context.Context) (*net.TCPConn, error)
	Login  ConsoleLogin
}

// ConsoleLogin is available only on a writable Windows physical console.
type ConsoleLogin interface {
	Available() bool
	Active(account string) bool
	Ready() bool
	Submit(ctx context.Context, account string, password []byte) error
}

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
		var stdout, stderr *backendLogWriter
		if cfg.Engine == "libvncserver" {
			// Keep native library diagnostics from interleaving with JSON audit records.
			stdout = &backendLogWriter{log: log, stream: "stdout"}
			stderr = &backendLogWriter{log: log, stream: "stderr"}
			cmd.Stdout, cmd.Stderr = stdout, stderr
		}
		if err := cmd.Start(); err != nil {
			return nil, nil, fmt.Errorf("launch %s: %w", cfg.Engine, err)
		}
		processDone = make(chan error, 1)
		waited := make(chan struct{})
		go func() {
			err := cmd.Wait()
			if stdout != nil {
				stdout.flush()
				stderr.flush()
			}
			processDone <- err
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

type backendLogWriter struct {
	log    *slog.Logger
	stream string
	buffer []byte
}

// Each stream has one exec.Cmd copy goroutine; Wait joins it before flush.
func (w *backendLogWriter) Write(data []byte) (int, error) {
	w.buffer = append(w.buffer, data...)
	for len(w.buffer) > 0 {
		n := bytes.IndexByte(w.buffer, '\n') + 1
		if n == 0 {
			if len(w.buffer) <= 8192 {
				break
			}
			n = 8192
		}
		w.record(w.buffer[:n])
		w.buffer = w.buffer[n:]
	}
	return len(data), nil
}

func (w *backendLogWriter) flush() {
	if len(w.buffer) > 0 {
		w.record(w.buffer)
		w.buffer = nil
	}
}

func (w *backendLogWriter) record(data []byte) {
	w.log.Info("vnc.backend", "engine", "libvncserver", "stream", w.stream, "message", strings.TrimRight(string(data), "\r\n"))
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
	case "libvncserver":
		if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
			return nil, errors.New("the LibVNCServer desktop backend requires Windows or Linux X11")
		}
		if err := checkDesktopPassword(cfg.PasswordFile); err != nil {
			return nil, err
		}
		args := []string{"--listen", cfg.Listen, "--password-file", cfg.PasswordFile,
			"--parent-pid", fmt.Sprint(os.Getpid())}
		if cfg.ViewOnly {
			args = append(args, "--view-only")
		}
		return exec.CommandContext(ctx, cfg.Executable, args...), nil
	default:
		return nil, fmt.Errorf("unknown managed VNC engine %q", cfg.Engine)
	}
}

// The desktop backend reads this file itself; the password never enters argv.
func checkDesktopPassword(name string) error {
	password, err := ReadDesktopPassword(name)
	clear(password)
	return err
}

// ReadDesktopPassword reads the password format used by the LibVNCServer backend.
func ReadDesktopPassword(name string) ([]byte, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, fmt.Errorf("LibVNCServer password file: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return nil, errors.New("LibVNCServer password file must be a private regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, 11))
	if err != nil {
		return nil, err
	}
	// Accept one optional LF or CRLF, but no extra lines or silent truncation.
	if len(data) > 0 && data[len(data)-1] == '\n' {
		data = data[:len(data)-1]
		if len(data) > 0 && data[len(data)-1] == '\r' {
			data = data[:len(data)-1]
		}
	}
	if err := ValidateDesktopPassword(string(data)); err != nil {
		clear(data)
		return nil, err
	}
	return data, nil
}

// ValidateDesktopPassword applies the backend's VNC authentication length and encoding rules.
func ValidateDesktopPassword(password string) error {
	if len(password) < 1 || len(password) > 8 {
		return errors.New("LibVNCServer password must contain 1-8 printable ASCII bytes")
	}
	for i := range len(password) {
		if password[i] < 32 || password[i] > 126 {
			return errors.New("LibVNCServer password must contain 1-8 printable ASCII bytes")
		}
	}
	return nil
}

func waitReady(ctx context.Context, cfg config.VNC, exited <-chan error) error {
	return waitForRFB(ctx, cfg, exited, func(ctx context.Context) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "tcp4", cfg.Listen)
	})
}

func waitForRFB(ctx context.Context, cfg config.VNC, exited <-chan error, dial func(context.Context) (net.Conn, error)) error {
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
			if isRFB(ctx, dial) {
				return nil
			}
		}
	}
}

func isRFB(ctx context.Context, dial func(context.Context) (net.Conn, error)) bool {
	ctx, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
	defer cancel()
	conn, err := dial(ctx)
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
