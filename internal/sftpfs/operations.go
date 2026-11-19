package sftpfs

import (
	"errors"
	"io"
	"os"
	"path"
	"sync"

	"github.com/pkg/sftp"
)

func (h *handler) Filecmd(r *sftp.Request) error {
	return h.result(r.Method, r.Filepath, r.Target, h.mutate(r, false))
}

func (h *handler) PosixRename(r *sftp.Request) error {
	return h.result(r.Method, r.Filepath, r.Target, h.mutate(r, true))
}

func (h *handler) mutate(r *sftp.Request, overwrite bool) error {
	if h.fs.mode == "read_only" {
		return os.ErrPermission
	}
	name, err := localPath(r.Filepath)
	if err != nil || name == "." {
		return os.ErrPermission
	}
	h.fs.mutations.Lock()
	defer h.fs.mutations.Unlock()
	switch r.Method {
	case "Mkdir":
		return h.fs.root.Mkdir(name, 0700)
	case "Rmdir", "Remove", "Rename", "PosixRename":
		info, err := h.fs.root.Lstat(name)
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return os.ErrPermission
		}
		switch r.Method {
		case "Rmdir", "Remove":
			if info.IsDir() != (r.Method == "Rmdir") {
				return os.ErrPermission
			}
			return h.fs.root.Remove(name)
		default:
			target, err := localPath(r.Target)
			if err != nil || target == "." {
				return os.ErrPermission
			}
			info, err := h.fs.root.Lstat(target)
			if !errors.Is(err, os.ErrNotExist) && err != nil {
				return err
			}
			if err == nil {
				if !overwrite {
					return os.ErrExist
				}
				if !info.IsDir() && !info.Mode().IsRegular() {
					return os.ErrPermission
				}
			}
			return h.fs.root.Rename(name, target)
		}
	default:
		return sftp.ErrSSHFxOpUnsupported
	}
}

func (h *handler) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	list, err := h.list(r)
	return list, h.result(r.Method, r.Filepath, "", err)
}

func (h *handler) list(r *sftp.Request) (sftp.ListerAt, error) {
	if r.Method != "List" && r.Method != "Stat" && r.Method != "Lstat" {
		return nil, sftp.ErrSSHFxOpUnsupported
	}
	name, err := localPath(r.Filepath)
	if err != nil {
		return nil, err
	}
	info, err := h.fs.root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return nil, os.ErrPermission
	}
	if r.Method != "List" {
		return singleInfo{info}, nil
	}
	if !info.IsDir() || h.fs.mode == "write_only" {
		return nil, os.ErrPermission
	}
	release, err := h.acquire()
	if err != nil {
		return nil, err
	}
	dir, err := h.fs.root.OpenFile(name, os.O_RDONLY|nonblock, 0)
	if err != nil {
		release()
		return nil, err
	}
	return &directory{fd: dir, h: h, name: r.Filepath, release: release}, nil
}

type singleInfo struct{ os.FileInfo }

func (i singleInfo) ListAt(out []os.FileInfo, off int64) (int, error) {
	if off != 0 || len(out) == 0 {
		return 0, io.EOF
	}
	out[0] = i.FileInfo
	return 1, io.EOF
}

type directory struct {
	mu      sync.Mutex
	fd      *os.File
	h       *handler
	name    string
	offset  int64
	release func()
}

func (d *directory) ListAt(out []os.FileInfo, off int64) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if off != d.offset {
		return 0, sftp.ErrSSHFxFailure
	}
	// The request server reads listings sequentially. Keep memory proportional to a single response.
	count := 0
	for count < len(out) {
		names, readErr := d.fd.Readdirnames(len(out) - count)
		for _, name := range names {
			local, err := localPath(path.Join(d.name, name))
			if err != nil {
				continue
			}
			info, err := d.h.fs.root.Lstat(local)
			if err == nil && (info.IsDir() || info.Mode().IsRegular()) {
				out[count] = info
				count++
			}
		}
		if readErr != nil {
			d.offset += int64(count)
			if errors.Is(readErr, io.EOF) {
				return count, io.EOF
			}
			return count, clientError(readErr)
		}
	}
	d.offset += int64(count)
	return count, nil
}

func (d *directory) Close() error {
	defer d.release()
	return clientError(d.fd.Close())
}
