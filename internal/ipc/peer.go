package ipc

import (
	"os"
	"path/filepath"
)

// Unix socket 地址使用较短的等价路径，遵守内核 sockaddr_un 的长度限制。
func socketAddress(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	workingDir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(workingDir, absolute)
	if err == nil && len(relative) < len(absolute) {
		return relative, nil
	}
	return absolute, nil
}
