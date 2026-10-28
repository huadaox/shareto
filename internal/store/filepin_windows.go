//go:build windows

package store

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func openAndPinPlatform(realPath string) (*os.File, os.FileInfo, uint64, uint64, error) {
	file, err := os.OpenFile(realPath, os.O_RDONLY, 0)
	if err != nil {
		return nil, nil, 0, 0, fmt.Errorf("failed to open file securely: %w", err)
	}

	fi, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, 0, 0, fmt.Errorf("failed to stat file: %w", err)
	}

	if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, 0, 0, errors.New("only regular files can be shared")
	}

	if fi.Size() <= 0 {
		_ = file.Close()
		return nil, nil, 0, 0, errors.New("empty files cannot be shared")
	}

	var data syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(file.Fd()), &data); err != nil {
		_ = file.Close()
		return nil, nil, 0, 0, fmt.Errorf("failed to get file information: %w", err)
	}

	dev := uint64(data.VolumeSerialNumber)
	ino := (uint64(data.FileIndexHigh) << 32) | uint64(data.FileIndexLow)

	return file, fi, dev, ino, nil
}

func verifyFileMetadata(file *os.File, fi os.FileInfo, entry *ShareEntry) error {
	if fi.Size() != entry.FileSize || fi.ModTime().UnixNano() != entry.MtimeUnixNano || fileChangeTime(fi) != entry.CtimeUnixNano {
		return ErrFileMutated
	}

	var data syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(file.Fd()), &data); err != nil {
		return ErrFileMutated
	}

	dev := uint64(data.VolumeSerialNumber)
	ino := (uint64(data.FileIndexHigh) << 32) | uint64(data.FileIndexLow)
	if dev != entry.DeviceID || ino != entry.Inode {
		return ErrFileMutated
	}

	return nil
}
