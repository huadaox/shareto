//go:build unix

package store

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func openAndPinPlatform(realPath string) (*os.File, os.FileInfo, uint64, uint64, error) {
	fd, err := unix.Open(realPath, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, 0, 0, fmt.Errorf("failed to open file securely: %w", err)
	}

	file := os.NewFile(uintptr(fd), realPath)
	fi, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, 0, 0, fmt.Errorf("failed to stat file: %w", err)
	}

	if !fi.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, 0, 0, errors.New("only regular files can be shared")
	}

	if fi.Size() <= 0 {
		_ = file.Close()
		return nil, nil, 0, 0, errors.New("empty files cannot be shared")
	}

	statT, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		_ = file.Close()
		return nil, nil, 0, 0, errors.New("failed to retrieve syscall.Stat_t")
	}

	return file, fi, uint64(statT.Dev), statT.Ino, nil
}

func verifyFileMetadata(file *os.File, fi os.FileInfo, entry *ShareEntry) error {
	statT, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || uint64(statT.Dev) != entry.DeviceID || statT.Ino != entry.Inode || fi.Size() != entry.FileSize || fi.ModTime().UnixNano() != entry.MtimeUnixNano || fileChangeTime(fi) != entry.CtimeUnixNano {
		return ErrFileMutated
	}
	return nil
}
