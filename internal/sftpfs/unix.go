//go:build unix

package sftpfs

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

const nonblock = unix.O_NONBLOCK

func singleLink(_ *os.File, info os.FileInfo) (bool, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink == 1, nil
}
