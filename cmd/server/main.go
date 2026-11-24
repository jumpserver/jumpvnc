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

const productName = "SuperVNC Server"

func main() {
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "%s — SVNC desktop and SFTP gateway\n\nUsage: %s [options]\n", productName, os.Args[0])
		flag.PrintDefaults()
	}
	name := flag.String("config", "config.yaml", "path to the YAML configuration")
	noTray := flag.Bool("no-tray", false, "run in the foreground without a notification area icon (Windows)")
	service := flag.Bool("service", false, "run under the Windows Service Control Manager")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log := slog.New(slog.NewJSONHandler(os.Stderr, nil)).With("product", productName)
	var err error
	if *service {
		err = runWindowsService(ctx, *name)
	} else {
		err = runAgent(ctx, *name, *noTray, log)
	}
	if err != nil && ctx.Err() == nil {
		log.Error("agent.error", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, name string, log *slog.Logger, ready func()) error {
	return runWithBackend(ctx, name, log, ready, func(ctx context.Context, cfg config.Config, log *slog.Logger) (vnc.Backend, error) {
		if !cfg.SSH.AllowVNC {
			return vnc.Backend{}, nil
		}
		exited, stop, err := vnc.Start(ctx, cfg.VNC, log)
		return vnc.Backend{Exited: exited, Stop: stop}, err
	})
}

func runWithBackend(ctx context.Context, name string, log *slog.Logger, ready func(), startBackend func(context.Context, config.Config, *slog.Logger) (vnc.Backend, error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
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

	listener, err := listenTCP(cfg.SSH.Listen)
	if err != nil {
		return fmt.Errorf("listen for SSH: %w", err)
	}
	defer listener.Close()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	backend, err := startBackend(ctx, cfg, log)
	if err != nil {
		return err
	}
	if backend.Stop != nil {
		defer backend.Stop()
	}
	gateway.SetVNCDialer(backend.Dial)
	var compatibility net.Listener
	if cfg.VNC.Compatibility.Enabled {
		compatibility, err = listenTCP(cfg.VNC.Compatibility.Listen)
		if err != nil {
			return fmt.Errorf("listen for VNC compatibility: %w", err)
		}
		defer compatibility.Close()
	}
	return serve(ctx, cancel, listener, compatibility, gateway, backend.Exited, cfg, log, ready)
}

func serve(ctx context.Context, cancel context.CancelFunc, listener, compatibility net.Listener, gateway *sshgateway.Server, exited <-chan error, cfg config.Config, log *slog.Logger, ready func()) error {
	served := make(chan error, 2)
	remaining := 1
	go func() { served <- gateway.Serve(ctx, listener) }()
	if compatibility != nil {
		remaining++
		go func() { served <- gateway.ServeVNC(ctx, compatibility) }()
		log.Info("vnc.compatibility.ready", "listen", compatibility.Addr().String(), "authentication", "backend_vnc", "ssh_encrypted", false)
	}
	log.Info("ssh.ready", "listen", listener.Addr().String(), "host_key", gateway.HostFingerprint(),
		"allow_vnc", cfg.SSH.AllowVNC, "sftp_mode", cfg.SFTP.Mode)
	if ready != nil {
		ready()
	}
	var result error
	select {
	case result = <-served:
		remaining--
	case err := <-exited:
		result = vnc.ExitError(cfg.VNC.Engine, err)
	}
	cancel()
	for range remaining {
		<-served
	}
	return result
}

func listenTCP(address string) (net.Listener, error) {
	network := "tcp"
	host, _, _ := net.SplitHostPort(address)
	if ip := net.ParseIP(host); ip.To4() != nil {
		network = "tcp4"
	}
	return net.Listen(network, address)
}
