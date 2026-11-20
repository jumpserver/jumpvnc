// Package sftpfs serves the configured directory through SFTP.
package sftpfs

import (
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/pkg/sftp"

	"sshvnc/internal/config"
)

type Filesystem struct {
	root       *os.Root
	mode       string
	maxSize    int64
	maxHandles int
	mutations  sync.Mutex
}

func Open(cfg config.SFTP) (*Filesystem, error) {
	root, err := os.OpenRoot(cfg.Root)
	if err != nil {
		return nil, err
	}
	return &Filesystem{root: root, mode: cfg.Mode, maxSize: cfg.MaxFileSizeMiB << 20, maxHandles: cfg.MaxHandles}, nil
}

func (f *Filesystem) Close() error { return f.root.Close() }

func (f *Filesystem) Handlers(log *slog.Logger) sftp.Handlers {
	h := &handler{fs: f, log: log, handles: make(chan struct{}, f.maxHandles)}
	return sftp.Handlers{FileGet: h, FilePut: h, FileCmd: h, FileList: h}
}

type handler struct {
	fs      *Filesystem
	log     *slog.Logger
	handles chan struct{}
}

func (h *handler) acquire() (func(), error) {
	select {
	case h.handles <- struct{}{}:
		return func() { <-h.handles }, nil
	default:
		return nil, errors.New("SFTP handle limit reached")
	}
}

func localPath(name string) (string, error) {
	if len(name) > 4096 || !strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\\:\x00") {
		return "", os.ErrPermission
	}
	relative := strings.TrimPrefix(path.Clean(name), "/")
	if relative == "" {
		return ".", nil
	}
	for _, part := range strings.Split(relative, "/") {
		if strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return "", os.ErrPermission
		}
	}
	local, err := filepath.Localize(relative)
	if err != nil {
		return "", os.ErrPermission
	}
	return local, nil
}

func (h *handler) RealPath(name string) (string, error) {
	virtual := path.Join("/", name)
	_, err := localPath(virtual)
	if err != nil {
		return "", h.result("Realpath", virtual, "", err)
	}
	return virtual, nil
}

func (h *handler) Fileread(r *sftp.Request) (io.ReaderAt, error)  { return h.open(r) }
func (h *handler) Filewrite(r *sftp.Request) (io.WriterAt, error) { return h.open(r) }
func (h *handler) OpenFile(r *sftp.Request) (sftp.WriterAtReaderAt, error) {
	return h.open(r)
}

func (h *handler) open(r *sftp.Request) (*file, error) {
	f, err := h.openFile(r)
	if err != nil {
		return nil, h.result(r.Method, r.Filepath, "", err)
	}
	return f, nil
}

func (h *handler) openFile(r *sftp.Request) (*file, error) {
	p := r.Pflags()
	if p.Read && h.fs.mode == "write_only" || p.Write && h.fs.mode == "read_only" ||
		(p.Creat || p.Trunc || p.Append || p.Excl) && !p.Write || p.Excl && !p.Creat || !p.Read && !p.Write {
		return nil, os.ErrPermission
	}
	name, err := localPath(r.Filepath)
	if err != nil || name == "." {
		return nil, os.ErrPermission
	}
	release, err := h.acquire()
	if err != nil {
		return nil, err
	}

	if p.Write {
		h.fs.mutations.Lock()
		defer h.fs.mutations.Unlock()
	}
	fd, err := h.openRegular(name, p)
	if err != nil {
		release()
		return nil, err
	}
	return &file{fd: fd, h: h, op: r.Method, name: r.Filepath, appendMode: p.Append, release: release}, nil
}

func (h *handler) openRegular(name string, p sftp.FileOpenFlags) (*os.File, error) {
	info, err := h.fs.root.Lstat(name)
	if err == nil && !info.Mode().IsRegular() {
		return nil, os.ErrPermission
	}
	if err != nil && !(p.Creat && errors.Is(err, os.ErrNotExist)) {
		return nil, err
	}
	flags := os.O_RDONLY | nonblock
	switch {
	case p.Read && p.Write:
		flags |= os.O_RDWR
	case p.Write:
		flags |= os.O_WRONLY
	}
	if p.Creat {
		flags |= os.O_CREATE
	}
	if p.Excl {
		flags |= os.O_EXCL
	}
	// Delay truncation until the open descriptor is confirmed to be a private regular file.
	fd, err := h.fs.root.OpenFile(name, flags, 0600)
	if err != nil {
		return nil, err
	}
	if err := checkRegular(fd); err != nil {
		fd.Close()
		return nil, err
	}
	if p.Trunc {
		if err := fd.Truncate(0); err != nil {
			fd.Close()
			return nil, err
		}
	}
	return fd, nil
}

func checkRegular(fd *os.File) error {
	info, err := fd.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return os.ErrPermission
	}
	single, err := singleLink(fd, info)
	if err != nil {
		return err
	}
	if !single {
		return os.ErrPermission
	}
	return nil
}

type file struct {
	fd         *os.File
	h          *handler
	op         string
	name       string
	appendMode bool
	release    func()
	read       atomic.Int64
	written    atomic.Int64
	failed     atomic.Bool
}

func (f *file) ReadAt(p []byte, off int64) (int, error) {
	if err := checkRegular(f.fd); err != nil {
		f.failed.Store(true)
		return 0, clientError(err)
	}
	n, err := f.fd.ReadAt(p, off)
	f.read.Add(int64(n))
	if err != nil && !errors.Is(err, io.EOF) {
		f.failed.Store(true)
		return n, clientError(err)
	}
	return n, err
}

func (f *file) WriteAt(p []byte, off int64) (int, error) {
	n, err := f.writeAt(p, off)
	f.written.Add(int64(n))
	if err != nil {
		f.failed.Store(true)
	}
	return n, clientError(err)
}

func (f *file) writeAt(p []byte, off int64) (int, error) {
	f.h.fs.mutations.Lock()
	defer f.h.fs.mutations.Unlock()
	if err := checkRegular(f.fd); err != nil {
		return 0, err
	}
	if f.appendMode {
		info, err := f.fd.Stat()
		if err != nil {
			return 0, err
		}
		off = info.Size()
	}
	if off < 0 || off > f.h.fs.maxSize || int64(len(p)) > f.h.fs.maxSize-off {
		return 0, os.ErrPermission
	}
	return f.fd.WriteAt(p, off)
}

func (f *file) TransferError(error) { f.failed.Store(true) }

func (f *file) Close() error {
	defer f.release()
	err := f.fd.Close()
	result := "success"
	if err != nil || f.failed.Load() {
		result = "failed"
	}
	f.h.log.Info("sftp.operation", "operation", f.op, "path", f.name, "result", result,
		"bytes_read", f.read.Load(), "bytes_written", f.written.Load())
	return clientError(err)
}

func (h *handler) result(op, name, target string, err error) error {
	result := "success"
	if err != nil {
		result = "failed"
	}
	if len(name) > 4096 {
		name = "<oversized>"
	}
	if len(target) > 4096 {
		target = "<oversized>"
	}
	h.log.Info("sftp.operation", "operation", op, "path", name, "target", target, "result", result)
	return clientError(err)
}

func clientError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrPermission):
		return sftp.ErrSSHFxPermissionDenied
	case errors.Is(err, fs.ErrNotExist):
		return fs.ErrNotExist
	case errors.Is(err, fs.ErrExist):
		return fs.ErrExist
	case errors.Is(err, sftp.ErrSSHFxOpUnsupported):
		return sftp.ErrSSHFxOpUnsupported
	default:
		return errors.New("filesystem operation failed")
	}
}
