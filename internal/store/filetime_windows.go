package store

import (
	"os"
	"syscall"
)

func fileChangeTime(fi os.FileInfo) int64 {
	stat, ok := fi.Sys().(*syscall.Win32FileAttributeData)
	if !ok {
		return fi.ModTime().UnixNano()
	}
	return stat.CreationTime.Nanoseconds()
}
