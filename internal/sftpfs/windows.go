//go:build windows

package sftpfs

import (
	"os"

	"golang.org/x/sys/windows"
)

const nonblock = 0

func singleLink(fd *os.File, _ os.FileInfo) (bool, error) {
	var info windows.ByHandleFileInformation
	err := windows.GetFileInformationByHandle(windows.Handle(fd.Fd()), &info)
	return info.NumberOfLinks == 1, err
}
