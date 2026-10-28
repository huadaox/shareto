package store

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"shareto/internal/config"
	"shareto/internal/crypto"
	"shareto/internal/netutil"
)

type Manager struct {
	mu                sync.RWMutex
	shares            map[string]*ShareEntry
	cfg               *config.Config
	stateFilePath     string
	globalActiveConns atomic.Int32
	csrfSecret        []byte
	saveMu            sync.Mutex
	authSem           chan struct{}
}

// DefaultStatePath returns the path to state.json under $XDG_STATE_HOME.
func DefaultStatePath() string {
	stateDir := os.Getenv("XDG_STATE_HOME")
	if stateDir == "" && (runtime.GOOS == "darwin" || runtime.GOOS == "windows") {
		var err error
		stateDir, err = os.UserConfigDir()
		if err != nil {
			stateDir = os.Getenv("AppData")
		}
	}
	if stateDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		stateDir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(stateDir, "shareto", "state.json")
}

// NewManager initializes the store manager, loads state and pins existing valid files.
func NewManager(cfg *config.Config, customStatePath ...string) (*Manager, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	statePath := DefaultStatePath()
	if len(customStatePath) > 0 && customStatePath[0] != "" {
		statePath = customStatePath[0]
	}

	secret, err := crypto.GenerateSecretKey()
	if err != nil {
		return nil, fmt.Errorf("failed to generate csrf secret: %w", err)
	}

	m := &Manager{
		shares:        make(map[string]*ShareEntry),
		cfg:           cfg,
		stateFilePath: statePath,
		csrfSecret:    secret,
		authSem:       make(chan struct{}, cfg.MaxConcurrentAuth),
	}

	if err := m.loadState(); err != nil {
		for _, entry := range m.shares {
			entry.Close()
		}
		return nil, err
	}
	return m, nil
}

func (m *Manager) CSRFSecret() []byte {
	return m.csrfSecret
}

func (m *Manager) GlobalActiveConns() int32 {
	return m.globalActiveConns.Load()
}

// loadState reads state.json and repins valid files.
func (m *Manager) loadState() error {
	data, err := os.ReadFile(m.stateFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read state: %w", err)
	}

	var state PersistentState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("failed to decode state: %w", err)
	}
	if state.Version != 3 {
		return fmt.Errorf("unsupported state version: %d", state.Version)
	}

	now := time.Now()
	for token, entry := range state.Shares {
		if entry == nil || entry.Token != token || entry.RemainingDownloads < 0 || entry.RemainingDownloads > entry.MaxDownloads {
			return fmt.Errorf("invalid persisted share")
		}
		if now.After(entry.ExpiresAt) {
			continue
		}

		f, fi, dev, ino, err := OpenAndPinFile(entry.FilePath)
		if err != nil {
			continue
		}

		// Verify metadata
		statT := fi.ModTime().UnixNano()
		if dev != entry.DeviceID || ino != entry.Inode || fi.Size() != entry.FileSize || statT != entry.MtimeUnixNano {
			_ = f.Close()
			continue
		}
		if entry.CtimeUnixNano != 0 && fileChangeTime(fi) != entry.CtimeUnixNano {
			f.Close()
			continue
		}
		entry.CtimeUnixNano = fileChangeTime(fi)

		entry.InitRuntime(f)

		// Filter out expired sessions
		validSessions := make(map[string]*DownloadSession)
		for sid, sess := range entry.Sessions {
			if sess != nil && now.Before(sess.ExpiresAt) {
				validSessions[sid] = sess
			}
		}
		entry.Sessions = validSessions
		if entry.RemainingDownloads == 0 && len(validSessions) == 0 {
			entry.Close()
			continue
		}

		m.shares[token] = entry
	}
	return nil
}

func (m *Manager) snapshotState() *PersistentState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	state := &PersistentState{
		Version: 3,
		Shares:  make(map[string]*ShareEntry, len(m.shares)),
	}

	for k, entry := range m.shares {
		entry.Mu.Lock()
		sessionsCopy := make(map[string]*DownloadSession, len(entry.Sessions))
		for sk, sv := range entry.Sessions {
			sessionsCopy[sk] = &DownloadSession{
				SessionID: sv.SessionID,
				SubnetKey: sv.SubnetKey,
				CreatedAt: sv.CreatedAt,
				ExpiresAt: sv.ExpiresAt,
			}
		}
		entryCopy := &ShareEntry{
			Token:              entry.Token,
			FilePath:           entry.FilePath,
			FileName:           entry.FileName,
			DeviceID:           entry.DeviceID,
			Inode:              entry.Inode,
			FileSize:           entry.FileSize,
			MtimeUnixNano:      entry.MtimeUnixNano,
			CtimeUnixNano:      entry.CtimeUnixNano,
			PasswordHash:       entry.PasswordHash,
			BaseURL:            entry.BaseURL,
			CreatedAt:          entry.CreatedAt,
			ExpiresAt:          entry.ExpiresAt,
			MaxDownloads:       entry.MaxDownloads,
			RemainingDownloads: entry.RemainingDownloads,
			Sessions:           sessionsCopy,
		}
		entry.Mu.Unlock()

		state.Shares[k] = entryCopy
	}

	return state
}

// SaveState atomically writes PersistentState snapshot to disk.
func (m *Manager) SaveState() error {
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	return m.saveStateLocked()
}

func (m *Manager) saveStateLocked() error {
	state := m.snapshotState()

	dir := filepath.Dir(m.stateFilePath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create state directory: %w", err)
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".state-*")
	if err != nil {
		return fmt.Errorf("failed to write state file: %w", err)
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("failed to write state file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("failed to sync state file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close state file: %w", err)
	}

	if runtime.GOOS == "windows" {
		_ = os.Remove(m.stateFilePath)
	}
	if err := os.Rename(tmp.Name(), m.stateFilePath); err != nil {
		return fmt.Errorf("failed to commit state file: %w", err)
	}
	if err := syncDir(dir); err != nil {
		return fmt.Errorf("failed to sync state directory: %w", err)
	}
	return nil
}

// CreateShare registers and pins a new file share.
func (m *Manager) CreateShare(filePath string, expiresDuration time.Duration, maxDownloads int, passwordHash string, baseURL ...string) (*ShareEntry, error) {
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	if maxDownloads < 1 || maxDownloads > 1000 {
		return nil, fmt.Errorf("%w: max_downloads must be between 1 and 1000", ErrInvalidShareParams)
	}
	if expiresDuration < 1*time.Minute || expiresDuration > 720*time.Hour {
		return nil, fmt.Errorf("%w: expiration must be between 1m and 720h", ErrInvalidShareParams)
	}

	file, fi, dev, ino, err := OpenAndPinFile(filePath)
	if err != nil {
		return nil, err
	}

	token, err := crypto.GenerateToken()
	if err != nil {
		_ = file.Close()
		return nil, err
	}

	var shareBaseURL string
	if len(baseURL) > 0 && baseURL[0] != "" {
		shareBaseURL = baseURL[0]
	}

	now := time.Now()
	entry := &ShareEntry{
		Token:              token,
		FilePath:           file.Name(),
		FileName:           filepath.Base(filePath),
		DeviceID:           dev,
		Inode:              ino,
		FileSize:           fi.Size(),
		MtimeUnixNano:      fi.ModTime().UnixNano(),
		CtimeUnixNano:      fileChangeTime(fi),
		PasswordHash:       passwordHash,
		BaseURL:            shareBaseURL,
		CreatedAt:          now,
		ExpiresAt:          now.Add(expiresDuration),
		MaxDownloads:       maxDownloads,
		RemainingDownloads: maxDownloads,
		Sessions:           make(map[string]*DownloadSession),
	}
	entry.InitRuntime(file)

	m.mu.Lock()
	if len(m.shares) >= m.cfg.MaxActiveShares {
		m.mu.Unlock()
		entry.Close()
		return nil, fmt.Errorf("max active shares limit (%d) reached", m.cfg.MaxActiveShares)
	}
	m.shares[token] = entry
	m.mu.Unlock()

	if err := m.saveStateLocked(); err != nil {
		m.mu.Lock()
		delete(m.shares, token)
		m.mu.Unlock()
		entry.Close()
		return nil, err
	}

	return entry, nil
}

// GetShare retrieves an active ShareEntry by token.
func (m *Manager) GetShare(token string) (*ShareEntry, error) {
	m.mu.RLock()
	entry, exists := m.shares[token]
	m.mu.RUnlock()

	if !exists {
		return nil, ErrShareNotFound
	}

	if time.Now().After(entry.ExpiresAt) {
		return nil, ErrShareExpired
	}

	return entry, nil
}

// RemoveShare removes a share, closes file handle, and saves state.
func (m *Manager) RemoveShare(token string) error {
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	m.mu.Lock()
	entry, exists := m.shares[token]
	if exists {
		delete(m.shares, token)
	}
	m.mu.Unlock()

	if exists {
		entry.Close()
		return m.saveStateLocked()
	}
	return nil
}

// RevokeShare revokes shares matching full token, token prefix (>= 6 chars), URL, or real file path.
func (m *Manager) RevokeShare(target string) ([]string, error) {
	target = strings.TrimSpace(target)
	cleanedTarget := strings.TrimRight(target, ".")
	if idx := strings.Index(cleanedTarget, "/s/"); idx != -1 {
		cleanedTarget = strings.Trim(cleanedTarget[idx+3:], "/")
		if qIdx := strings.Index(cleanedTarget, "?"); qIdx != -1 {
			cleanedTarget = cleanedTarget[:qIdx]
		}
	}

	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	m.mu.Lock()
	var revoked []string
	var toClose []*ShareEntry

	for token, entry := range m.shares {
		match := false
		if token == target || token == cleanedTarget {
			match = true
		} else if len(cleanedTarget) >= 6 && strings.HasPrefix(token, cleanedTarget) {
			match = true
		} else if entry.FilePath == target || entry.FilePath == cleanedTarget ||
			filepath.Clean(entry.FilePath) == filepath.Clean(target) ||
			entry.FileName == target || entry.FileName == cleanedTarget {
			match = true
		}

		if match {
			revoked = append(revoked, token)
			toClose = append(toClose, entry)
			delete(m.shares, token)
		}
	}
	m.mu.Unlock()

	for _, entry := range toClose {
		entry.Close()
	}

	if len(revoked) > 0 {
		if err := m.saveStateLocked(); err != nil {
			return revoked, err
		}
	}

	return revoked, nil
}

// ListShares returns all current active shares.
func (m *Manager) ListShares() []*ShareEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()

	now := time.Now()
	list := make([]*ShareEntry, 0, len(m.shares))
	for _, entry := range m.shares {
		if now.Before(entry.ExpiresAt) {
			list = append(list, entry)
		}
	}
	return list
}

// ClaimSession decrements 1 remaining download quota atomically and generates a session.
func (m *Manager) ClaimSession(token string, clientIP net.IP) (*ShareEntry, *DownloadSession, error) {
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	entry, err := m.GetShare(token)
	if err != nil {
		return nil, nil, err
	}

	entry.Mu.Lock()

	if time.Now().After(entry.ExpiresAt) {
		entry.Mu.Unlock()
		return nil, nil, ErrShareExpired
	}

	if entry.RemainingDownloads <= 0 {
		entry.Mu.Unlock()
		return nil, nil, ErrDownloadExhausted
	}

	sessionID, err := crypto.GenerateToken()
	if err != nil {
		entry.Mu.Unlock()
		return nil, nil, err
	}

	now := time.Now()
	sessionExpires := now.Add(2 * time.Hour)
	if entry.ExpiresAt.Before(sessionExpires) {
		sessionExpires = entry.ExpiresAt
	}

	subnetKey := netutil.SessionSubnetKey(clientIP)
	session := &DownloadSession{
		SessionID: sessionID,
		SubnetKey: subnetKey,
		CreatedAt: now,
		ExpiresAt: sessionExpires,
	}

	previousSessions := entry.Sessions
	entry.Sessions = make(map[string]*DownloadSession, len(previousSessions)+1)
	for sid, existing := range previousSessions {
		entry.Sessions[sid] = existing
	}
	// 保留正在传输的会话，删除最早创建的空闲会话。
	if len(entry.Sessions) >= 128 {
		var oldestKey string
		var oldestTime time.Time
		for k, s := range entry.Sessions {
			if s.ActiveConns.Load() == 0 {
				if oldestKey == "" || s.CreatedAt.Before(oldestTime) {
					oldestKey = k
					oldestTime = s.CreatedAt
				}
			}
		}
		if oldestKey != "" {
			delete(entry.Sessions, oldestKey)
		}
	}

	entry.RemainingDownloads--
	entry.Sessions[sessionID] = session
	entry.Mu.Unlock()

	if err := m.saveStateLocked(); err != nil {
		entry.Mu.Lock()
		entry.RemainingDownloads++
		entry.Sessions = previousSessions
		entry.Mu.Unlock()
		return nil, nil, err
	}

	return entry, session, nil
}

func (m *Manager) AcquireAuthSlots(entry *ShareEntry) (func(), error) {
	select {
	case m.authSem <- struct{}{}:
	default:
		return nil, ErrConcurrencyLimit
	}
	select {
	case entry.AuthSem <- struct{}{}:
	default:
		<-m.authSem
		return nil, ErrConcurrencyLimit
	}
	var released atomic.Bool
	return func() {
		if released.CompareAndSwap(false, true) {
			<-entry.AuthSem
			<-m.authSem
		}
	}, nil
}

// GetSession validates token, sessionID and subnet binding.
func (m *Manager) GetSession(token, sessionID string, clientIP net.IP) (*ShareEntry, *DownloadSession, error) {
	entry, err := m.GetShare(token)
	if err != nil {
		return nil, nil, err
	}

	entry.Mu.Lock()
	session, exists := entry.Sessions[sessionID]
	entry.Mu.Unlock()

	if !exists {
		return nil, nil, ErrShareNotFound
	}

	if time.Now().After(session.ExpiresAt) {
		return nil, nil, ErrShareExpired
	}

	expectedSubnet := netutil.SessionSubnetKey(clientIP)
	if session.SubnetKey != expectedSubnet {
		return nil, nil, ErrSubnetMismatch
	}

	return entry, session, nil
}

// AcquireStreamSlots attempts to acquire concurrency slots at all 3 tiers.
// Tiers: Global, Entry, Session.
// Returns a release function if successful, or ErrConcurrencyLimit.
func (m *Manager) AcquireStreamSlots(entry *ShareEntry, session *DownloadSession) (func(), error) {
	// 1. Check Global
	for {
		cur := m.globalActiveConns.Load()
		if int(cur) >= m.cfg.MaxGlobalStreams {
			return nil, ErrConcurrencyLimit
		}
		if m.globalActiveConns.CompareAndSwap(cur, cur+1) {
			break
		}
	}

	// 2. Check Entry
	for {
		cur := entry.ActiveConns.Load()
		if int(cur) >= m.cfg.MaxStreamsPerToken {
			m.globalActiveConns.Add(-1)
			return nil, ErrConcurrencyLimit
		}
		if entry.ActiveConns.CompareAndSwap(cur, cur+1) {
			break
		}
	}

	// 3. Check Session
	for {
		cur := session.ActiveConns.Load()
		if int(cur) >= m.cfg.MaxStreamsPerSession {
			entry.ActiveConns.Add(-1)
			m.globalActiveConns.Add(-1)
			return nil, ErrConcurrencyLimit
		}
		if session.ActiveConns.CompareAndSwap(cur, cur+1) {
			break
		}
	}

	var released atomic.Bool
	release := func() {
		if released.CompareAndSwap(false, true) {
			session.ActiveConns.Add(-1)
			entry.ActiveConns.Add(-1)
			m.globalActiveConns.Add(-1)
		}
	}

	return release, nil
}

// StartCleanupLoop periodically purges expired shares and sessions.
func (m *Manager) StartCleanupLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := m.cleanExpired(); err != nil {
					log.Printf("failed to persist expired share cleanup: %v", err)
				}
			}
		}
	}()
}

func (m *Manager) cleanExpired() error {
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	now := time.Now()
	var toDelete []string
	var toClose []*ShareEntry

	m.mu.Lock()
	for token, entry := range m.shares {
		entry.Mu.Lock()
		rem := entry.RemainingDownloads
		numSessions := len(entry.Sessions)
		entry.Mu.Unlock()

		if now.After(entry.ExpiresAt) || (rem <= 0 && numSessions == 0) {
			toDelete = append(toDelete, token)
			toClose = append(toClose, entry)
			delete(m.shares, token)
			continue
		}

		// Clean expired sessions inside share
		entry.Mu.Lock()
		for sid, sess := range entry.Sessions {
			if now.After(sess.ExpiresAt) && sess.ActiveConns.Load() == 0 {
				delete(entry.Sessions, sid)
			}
		}
		entry.Mu.Unlock()
	}
	m.mu.Unlock()

	for _, entry := range toClose {
		entry.Close()
	}

	if len(toDelete) > 0 {
		return m.saveStateLocked()
	}
	return nil
}
