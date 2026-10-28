package store

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// OpenAndPinFile resolves symlinks, opens the file securely,
// verifies that it is a non-empty regular file, and returns the file descriptor along with metadata.
func OpenAndPinFile(rawPath string) (*os.File, os.FileInfo, uint64, uint64, error) {
	absPath, err := filepath.Abs(rawPath)
	if err != nil {
		return nil, nil, 0, 0, fmt.Errorf("failed to get absolute path: %w", err)
	}

	realPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return nil, nil, 0, 0, fmt.Errorf("failed to resolve symlink: %w", err)
	}

	return openAndPinPlatform(realPath)
}

// GetSectionReader verifies that the pinned file has not been altered, truncated, or replaced,
// and returns an io.SectionReader configured for concurrent pread reads.
func GetSectionReader(entry *ShareEntry) (*io.SectionReader, time.Time, error) {
	entry.Mu.Lock()
	f := entry.PinnedFile
	entry.Mu.Unlock()

	reader := &verifiedReaderAt{file: f, entry: entry}
	if err := reader.verify(); err != nil {
		return nil, time.Time{}, err
	}
	return io.NewSectionReader(reader, 0, entry.FileSize), time.Unix(0, entry.MtimeUnixNano), nil
}

type verifiedReaderAt struct {
	file  *os.File
	entry *ShareEntry
}

func (r *verifiedReaderAt) verify() error {
	if r.file == nil || r.entry.mutated.Load() {
		return ErrFileMutated
	}
	fi, err := r.file.Stat()
	if err != nil {
		r.entry.mutated.Store(true)
		return ErrFileMutated
	}
	if err := verifyFileMetadata(r.file, fi, r.entry); err != nil {
		r.entry.mutated.Store(true)
		return ErrFileMutated
	}
	return nil
}

func (r *verifiedReaderAt) ReadAt(p []byte, offset int64) (int, error) {
	if err := r.verify(); err != nil {
		return 0, err
	}
	n, err := r.file.ReadAt(p, offset)
	// 修改过的读取结果必须在返回给 HTTP 发送代码之前丢弃。
	if verifyErr := r.verify(); verifyErr != nil {
		return 0, verifyErr
	}
	return n, err
}
