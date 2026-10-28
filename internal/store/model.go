package store

import (
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrFileMutated        = errors.New("file has been mutated or truncated")
	ErrShareNotFound      = errors.New("share not found")
	ErrShareExpired       = errors.New("share has expired")
	ErrDownloadExhausted  = errors.New("download limit reached")
	ErrSubnetMismatch     = errors.New("subnet mismatch for session")
	ErrConcurrencyLimit   = errors.New("concurrency limit exceeded")
	ErrAuthTimeout        = errors.New("password verification timed out")
	ErrInvalidPassword    = errors.New("invalid password")
	ErrInvalidShareParams = errors.New("invalid share parameters")
)

type PersistentState struct {
	Version int                    `json:"version"` // 固定为 3
	Shares  map[string]*ShareEntry `json:"shares"`  // key: Token
}

type ShareEntry struct {
	Token              string                      `json:"token"`           // 22字符 base64url (128-bit)
	FilePath           string                      `json:"file_path"`       // EvalSymlinks 后的绝对物理路径
	FileName           string                      `json:"file_name"`       // 展示与下载文件名
	DeviceID           uint64                      `json:"device_id"`       // stat.Dev
	Inode              uint64                      `json:"inode"`           // stat.Ino
	FileSize           int64                       `json:"file_size"`       // 初始字节大小
	MtimeUnixNano      int64                       `json:"mtime_unix_nano"` // 初始修改时间(纳秒)
	CtimeUnixNano      int64                       `json:"ctime_unix_nano"`
	PasswordHash       string                      `json:"password_hash,omitempty"` // Argon2id 编码字符串，空表示无密码
	BaseURL            string                      `json:"base_url,omitempty"`      // 创建时指定的 Public Base URL
	CreatedAt          time.Time                   `json:"created_at"`
	ExpiresAt          time.Time                   `json:"expires_at"`
	MaxDownloads       int                         `json:"max_downloads"` // 1 ~ 1000
	RemainingDownloads int                         `json:"remaining_downloads"`
	Sessions           map[string]*DownloadSession `json:"sessions"` // key: SessionID

	// 运行时非导出字段 (json:"-")
	Mu          sync.Mutex    `json:"-"`
	PinnedFile  *os.File      `json:"-"` // 常驻只读文件句柄
	AuthSem     chan struct{} `json:"-"` // 容量为 2 的密码验证信号量，防 Argon2id CPU DoS
	ActiveConns atomic.Int32  `json:"-"` // 当前活跃下载流计数
	mutated     atomic.Bool
}

type DownloadSession struct {
	SessionID   string       `json:"session_id"` // 22字符 base64url (128-bit)
	SubnetKey   string       `json:"subnet_key"` // 绑定的客户端网段: IPv4为 /24, IPv6为 /64
	CreatedAt   time.Time    `json:"created_at"`
	ExpiresAt   time.Time    `json:"expires_at"` // min(Now+2h, ShareEntry.ExpiresAt)
	ActiveConns atomic.Int32 `json:"-"`          // 当前 Session 并发流计数 (上限 8)
}

// InitRuntime initializes non-exported runtime primitives.
func (e *ShareEntry) InitRuntime(f *os.File) {
	e.PinnedFile = f
	e.AuthSem = make(chan struct{}, 2)
	if e.Sessions == nil {
		e.Sessions = make(map[string]*DownloadSession)
	}
}

// Close closes the underlying pinned file handle if open.
func (e *ShareEntry) Close() {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	if e.PinnedFile != nil {
		_ = e.PinnedFile.Close()
		e.PinnedFile = nil
	}
}

func (e *ShareEntry) FileMutated() bool {
	return e.mutated.Load()
}

func (e *ShareEntry) Remaining() int {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	return e.RemainingDownloads
}
