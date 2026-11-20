package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const maxConnectionFileSize = 1 << 20

// ConnectionFile is the portable .svnc format, separate from the local library.
// Its explicit profile fields exclude credentials and machine-local trust state.
type ConnectionFile struct {
	Format      string               `json:"format"`
	Version     int                  `json:"version"`
	Connections []PortableConnection `json:"connections"`
}

type PortableConnection struct {
	Name    string          `json:"name"`
	Profile PortableProfile `json:"profile"`
}

type PortableProfile struct {
	Transport string `json:"transport"`
	Host      string `json:"host"`
	Port      int    `json:"port,omitempty"`
	User      string `json:"user,omitempty"`
	Jump      string `json:"jump,omitempty"`
	VNCHost   string `json:"vncHost,omitempty"`
	VNCPort   int    `json:"vncPort,omitempty"`
}

func portableConnection(server Server) PortableConnection {
	p := server.Profile
	if p.Transport == "" {
		p.Transport = "forward"
	}
	if p.Transport != "forward" {
		p.VNCHost, p.VNCPort = "", 0
	}
	if p.Transport == "vnc" {
		p.User, p.Jump = "", ""
	}
	return PortableConnection{Name: server.Name, Profile: PortableProfile{
		Transport: p.Transport, Host: p.Host, Port: p.Port, User: p.User,
		Jump: p.Jump, VNCHost: p.VNCHost, VNCPort: p.VNCPort,
	}}
}

func (c PortableConnection) server() (Server, error) {
	c.Name = strings.TrimSpace(c.Name)
	p := Profile{Transport: c.Profile.Transport, Host: strings.TrimSpace(c.Profile.Host),
		Port: c.Profile.Port, User: strings.TrimSpace(c.Profile.User), Jump: strings.TrimSpace(c.Profile.Jump),
		VNCHost: strings.TrimSpace(c.Profile.VNCHost), VNCPort: c.Profile.VNCPort}
	if c.Name == "" || len([]rune(c.Name)) > 100 || strings.ContainsFunc(c.Name+p.Host+p.User+p.Jump+p.VNCHost, unicode.IsControl) {
		return Server{}, errors.New("连接名称或地址包含无效内容")
	}
	if p.Transport != "subsystem" && p.Transport != "vnc" && p.Transport != "forward" {
		return Server{}, errors.New("连接必须指定 subsystem、vnc 或 forward")
	}
	if p.Port == 0 {
		switch p.Transport {
		case "subsystem":
			p.Port = 5922
		case "vnc":
			p.Port = 5900
		case "forward":
			p.Port = 22
		}
	}
	if err := p.validateTransport(); err != nil {
		return Server{}, err
	}
	if strings.HasPrefix(p.Jump, "-") || strings.ContainsAny(p.Jump, " \t") {
		return Server{}, errors.New("跳板机应为 SSH 主机名或别名")
	}
	if p.Transport != "forward" && (p.VNCHost != "" || p.VNCPort != 0) || p.Transport == "vnc" && (p.User != "" || p.Jump != "") {
		return Server{}, errors.New("连接字段与所选协议不匹配")
	}
	return Server{Name: c.Name, Profile: p}, nil
}

func decodeConnectionFile(data []byte) ([]Server, error) {
	if len(data) > maxConnectionFileSize || !utf8.Valid(data) || !json.Valid(data) {
		return nil, errors.New(".svnc 文件必须是有效的 UTF-8 JSON，且不超过 1 MiB")
	}
	var file ConnectionFile
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return nil, fmt.Errorf("无效的 .svnc 配置: %w", err)
	}
	if file.Format != "svnc" || file.Version != 1 {
		return nil, errors.New("不支持此 .svnc 文件格式或版本")
	}
	if len(file.Connections) == 0 || len(file.Connections) > 1000 {
		return nil, errors.New(".svnc 文件应包含 1–1000 个连接")
	}
	servers := make([]Server, 0, len(file.Connections))
	for i, connection := range file.Connections {
		server, err := connection.server()
		if err != nil {
			return nil, fmt.Errorf("第 %d 个连接: %w", i+1, err)
		}
		servers = append(servers, server)
	}
	return servers, nil
}

func readConnectionFile(filename string) ([]Server, error) {
	if !strings.EqualFold(filepath.Ext(filename), ".svnc") {
		return nil, errors.New("请选择 .svnc 连接文件")
	}
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxConnectionFileSize {
		return nil, errors.New(".svnc 文件必须是最多 1 MiB 的普通文件")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxConnectionFileSize+1))
	if err != nil {
		return nil, err
	}
	return decodeConnectionFile(data)
}

type ImportResult struct {
	Library Library  `json:"library"`
	Files   int      `json:"files"`
	Added   int      `json:"added"`
	Skipped int      `json:"skipped"`
	Errors  []string `json:"errors"`
}

func (s *libraryStore) importConnectionFiles(paths []string) (ImportResult, error) {
	result := ImportResult{Files: len(paths), Errors: []string{}}
	for _, filename := range paths {
		servers, err := readConnectionFile(filename)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", filepath.Base(filename), err))
			continue
		}
		added, skipped := 0, 0
		_, err = s.update(func(lib *Library) error {
			for _, server := range servers {
				duplicate := false
				for _, existing := range lib.Servers {
					if portableConnection(existing) == portableConnection(server) {
						duplicate = true
						break
					}
				}
				if duplicate {
					skipped++
					continue
				}
				server.ID, server.CreatedAt = rand.Text(), time.Now().UnixMilli()
				lib.Servers = append(lib.Servers, server)
				added++
			}
			return nil
		})
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", filepath.Base(filename), err))
			continue
		}
		result.Added += added
		result.Skipped += skipped
	}
	var err error
	result.Library, err = s.read()
	return result, err
}

func encodeConnectionFile(server Server) ([]byte, error) {
	connection := portableConnection(server)
	if _, err := connection.server(); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(ConnectionFile{Format: "svnc", Version: 1, Connections: []PortableConnection{connection}}, "", "  ")
	return append(data, '\n'), err
}

func writeConnectionFile(filename string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(filename), ".svnc-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(data)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), filename)
}
