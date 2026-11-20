package config

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

type Config struct {
	SSH   SSH  `yaml:"ssh"`
	SFTP  SFTP `yaml:"sftp"`
	VNC   VNC  `yaml:"vnc"`
	Audit bool `yaml:"audit"`
}

type SSH struct {
	Listen             string `yaml:"listen"`
	HostKeyFile        string `yaml:"host_key_file"`
	AuthorizedKeysFile string `yaml:"authorized_keys_file"`
	User               string `yaml:"user"`
	AllowVNC           bool   `yaml:"allow_vnc"`
	Limits             Limits `yaml:"limits"`
}

type Limits struct {
	MaxConnections           int `yaml:"max_connections"`
	MaxChannelsPerConnection int `yaml:"max_channels_per_connection"`
	HandshakesPerMinutePerIP int `yaml:"handshakes_per_minute_per_ip"`
	HandshakeTimeoutSeconds  int `yaml:"handshake_timeout_seconds"`
	IdleTimeoutSeconds       int `yaml:"idle_timeout_seconds"`
}

type SFTP struct {
	Mode           string `yaml:"mode"`
	Root           string `yaml:"root"`
	MaxFileSizeMiB int64  `yaml:"max_file_size_mib"`
	MaxHandles     int    `yaml:"max_handles_per_session"`
}

type VNC struct {
	Engine                string `yaml:"engine"`
	Listen                string `yaml:"listen"`
	Executable            string `yaml:"executable"`
	PasswordFile          string `yaml:"password_file"`
	ViewOnly              bool   `yaml:"view_only"`
	StartupTimeoutSeconds int    `yaml:"startup_timeout_seconds"`
}

func Load(name string) (Config, error) {
	cfg := Config{
		Audit: true,
		SSH: SSH{
			Listen: "0.0.0.0:2222",
			Limits: Limits{
				MaxConnections: 32, MaxChannelsPerConnection: 8, HandshakesPerMinutePerIP: 30,
				HandshakeTimeoutSeconds: 10, IdleTimeoutSeconds: 1800,
			},
		},
		SFTP: SFTP{Mode: "disabled", MaxFileSizeMiB: 1024, MaxHandles: 64},
		VNC:  VNC{Engine: "external", Listen: "127.0.0.1:5900", StartupTimeoutSeconds: 15},
	}

	name, err := filepath.Abs(name)
	if err != nil {
		return Config{}, err
	}
	f, err := os.Open(name)
	if err != nil {
		return Config{}, err
	}
	defer f.Close()

	decoder := yaml.NewDecoder(f)
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Config{}, errors.New("expected a single YAML document")
	}

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}

	dir := filepath.Dir(name)
	for _, value := range []*string{&cfg.SSH.HostKeyFile, &cfg.SSH.AuthorizedKeysFile, &cfg.SFTP.Root, &cfg.VNC.PasswordFile} {
		if *value != "" && !filepath.IsAbs(*value) {
			*value = filepath.Join(dir, *value)
		}
	}
	if executable := cfg.VNC.Executable; executable != "" && !filepath.IsAbs(executable) && (cfg.VNC.Engine == "ultravnc" || cfg.VNC.Engine == "libvncserver" || filepath.Base(executable) != executable) {
		cfg.VNC.Executable = filepath.Join(dir, executable)
	}

	if cfg.SFTP.Mode != "disabled" {
		root, err := filepath.EvalSymlinks(cfg.SFTP.Root)
		if err != nil {
			return Config{}, fmt.Errorf("sftp.root: %w", err)
		}
		protected := []string{name, cfg.SSH.HostKeyFile, cfg.SSH.AuthorizedKeysFile}
		if cfg.SSH.AllowVNC {
			protected = append(protected, cfg.VNC.PasswordFile)
		}
		if c := cfg.VNC; cfg.SSH.AllowVNC && filepath.IsAbs(c.Executable) {
			protected = append(protected, c.Executable)
			if c.Engine == "ultravnc" {
				executable, err := filepath.EvalSymlinks(c.Executable)
				if err != nil {
					return Config{}, err
				}
				dir := filepath.Dir(executable)
				protected = append(protected, filepath.Join(dir, "ultravnc.ini"), filepath.Join(dir, "ultravnc.portable"))
			}
		}
		for _, file := range protected {
			if file == "" {
				continue
			}
			resolved, err := filepath.EvalSymlinks(file)
			if err != nil {
				return Config{}, err
			}
			if rel, err := filepath.Rel(root, resolved); err == nil && filepath.IsLocal(rel) {
				return Config{}, fmt.Errorf("sftp.root contains protected file %q", file)
			}
		}
		cfg.SFTP.Root = root
	}
	return cfg, nil
}

func (c Config) validate() error {
	if _, _, err := net.SplitHostPort(c.SSH.Listen); err != nil {
		return fmt.Errorf("ssh.listen: %w", err)
	}
	if strings.TrimSpace(c.SSH.User) != c.SSH.User || c.SSH.User == "" || c.SSH.HostKeyFile == "" || c.SSH.AuthorizedKeysFile == "" {
		return errors.New("ssh.user, ssh.host_key_file and ssh.authorized_keys_file are required")
	}
	l := c.SSH.Limits
	if l.MaxConnections < 1 || l.MaxConnections > 1024 || l.MaxChannelsPerConnection < 1 || l.MaxChannelsPerConnection > 64 ||
		l.HandshakesPerMinutePerIP < 1 || l.HandshakesPerMinutePerIP > 10000 ||
		l.HandshakeTimeoutSeconds < 1 || l.HandshakeTimeoutSeconds > 120 || l.IdleTimeoutSeconds < 1 || l.IdleTimeoutSeconds > 86400 {
		return errors.New("ssh.limits values out of range")
	}
	switch c.SFTP.Mode {
	case "disabled", "read_only", "write_only", "read_write":
	default:
		return errors.New("sftp.mode must be disabled, read_only, write_only or read_write")
	}
	if c.SFTP.Mode != "disabled" && (c.SFTP.Root == "" || c.SFTP.MaxFileSizeMiB < 1 || c.SFTP.MaxFileSizeMiB > 1048576 || c.SFTP.MaxHandles < 1 || c.SFTP.MaxHandles > 256) {
		return errors.New("SFTP needs a root, max_file_size_mib between 1 and 1048576, and max_handles_per_session between 1 and 256")
	}
	if !c.SSH.AllowVNC && c.SFTP.Mode == "disabled" {
		return errors.New("enable at least one of SSH desktop forwarding and SFTP")
	}
	host, port, err := net.SplitHostPort(c.VNC.Listen)
	number, portErr := strconv.Atoi(port)
	if err != nil || host != "127.0.0.1" || portErr != nil || number < 1 || number > 65535 || strconv.Itoa(number) != port {
		return errors.New("vnc.listen must be 127.0.0.1 with a numeric TCP port")
	}
	if c.VNC.StartupTimeoutSeconds < 1 || c.VNC.StartupTimeoutSeconds > 120 {
		return errors.New("vnc.startup_timeout_seconds must be between 1 and 120")
	}
	switch c.VNC.Engine {
	case "external":
		if c.VNC.Executable != "" || c.VNC.PasswordFile != "" || c.VNC.ViewOnly {
			return errors.New("configure executable, authentication and view-only policy in the external VNC server")
		}
	case "tigervnc":
		if c.VNC.PasswordFile == "" {
			return errors.New("vnc.password_file is required for TigerVNC")
		}
	case "ultravnc":
		if c.VNC.Executable == "" || c.VNC.PasswordFile != "" {
			return errors.New("UltraVNC requires vnc.executable and uses the adjacent ultravnc.ini for authentication")
		}
	case "libvncserver":
		if c.VNC.Executable == "" || c.VNC.PasswordFile == "" {
			return errors.New("LibVNCServer requires vnc.executable and vnc.password_file")
		}
	default:
		return errors.New("vnc.engine must be external, tigervnc, ultravnc or libvncserver")
	}
	return nil
}
