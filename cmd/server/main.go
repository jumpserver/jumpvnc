package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"sshvnc/internal/config"
	"sshvnc/internal/sshgateway"
	"sshvnc/internal/vnc"
)

func main() {
	name := flag.String("config", "config.yaml", "path to the YAML configuration")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(ctx, *name, log); err != nil && ctx.Err() == nil {
		log.Error("agent.error", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, name string, log *slog.Logger) error {
	cfg, err := config.Load(name)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	audit := log
	if !cfg.Audit {
		audit = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	gateway, err := sshgateway.New(cfg, audit)
	if err != nil {
		return err
	}
	defer gateway.Close()

	network := "tcp"
	host, _, _ := net.SplitHostPort(cfg.SSH.Listen)
	if ip := net.ParseIP(host); ip.To4() != nil {
		network = "tcp4"
	}
	listener, err := net.Listen(network, cfg.SSH.Listen)
	if err != nil {
		return fmt.Errorf("listen for SSH: %w", err)
	}
	defer listener.Close()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var exited <-chan error
	if cfg.SSH.AllowVNC {
		var stop func()
		exited, stop, err = vnc.Start(ctx, cfg.VNC, log)
		if err != nil {
			return err
		}
		defer stop()
	}
	return serve(ctx, cancel, listener, gateway, exited, cfg, log)
}

func serve(ctx context.Context, cancel context.CancelFunc, listener net.Listener, gateway *sshgateway.Server, exited <-chan error, cfg config.Config, log *slog.Logger) error {
	served := make(chan error, 1)
	go func() { served <- gateway.Serve(ctx, listener) }()
	log.Info("ssh.ready", "listen", listener.Addr().String(), "host_key", gateway.HostFingerprint(),
		"allow_vnc", cfg.SSH.AllowVNC, "sftp_mode", cfg.SFTP.Mode)
	select {
	case err := <-served:
		return err
	case err := <-exited:
		cancel()
		<-served
		return vnc.ExitError(cfg.VNC.Engine, err)
	}
}
