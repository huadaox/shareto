package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

type Config struct {
	ListenAddr           string    `json:"listen_addr"`     // 默认 "127.0.0.1:38080"
	PublicBaseURL        string    `json:"public_base_url"` // 默认 "https://share.example.com"
	TLS                  TLSConfig `json:"tls"`
	TrustedProxies       []string  `json:"trusted_proxies"`         // 默认 ["127.0.0.1/32", "::1/128"]
	MaxGlobalStreams     int       `json:"max_global_streams"`      // 默认 64
	MaxStreamsPerToken   int       `json:"max_streams_per_token"`   // 默认 16
	MaxStreamsPerSession int       `json:"max_streams_per_session"` // 默认 8
	MaxActiveShares      int       `json:"max_active_shares"`       // 默认 100
	MinTransferRateKBps  int       `json:"min_transfer_rate_kbps"`  // 默认 16 (即 16KB/s)
	MaxConnections       int       `json:"max_connections"`
	MaxConnectionsPerIP  int       `json:"max_connections_per_ip"`
	MaxConcurrentAuth    int       `json:"max_concurrent_auth"`
}

type TLSConfig struct {
	Enabled  bool   `json:"enabled"`
	CertFile string `json:"cert_file"`
	KeyFile  string `json:"key_file"`
}

// DefaultConfig returns default configuration parameters.
func DefaultConfig() *Config {
	return &Config{
		ListenAddr:    "127.0.0.1:38080",
		PublicBaseURL: "http://127.0.0.1:38080",
		TLS: TLSConfig{
			Enabled:  false,
			CertFile: "",
			KeyFile:  "",
		},
		TrustedProxies:       []string{"127.0.0.1/32", "::1/128"},
		MaxGlobalStreams:     64,
		MaxStreamsPerToken:   16,
		MaxStreamsPerSession: 8,
		MaxActiveShares:      100,
		MinTransferRateKBps:  16,
		MaxConnections:       256,
		MaxConnectionsPerIP:  32,
		MaxConcurrentAuth:    2,
	}
}

// DefaultConfigPath returns the path to config.json according to XDG spec.
func DefaultConfigPath() string {
	configDir := os.Getenv("XDG_CONFIG_HOME")
	if configDir == "" && (runtime.GOOS == "darwin" || runtime.GOOS == "windows") {
		var err error
		configDir, err = os.UserConfigDir()
		if err != nil {
			configDir = os.Getenv("AppData")
		}
	}
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		configDir = filepath.Join(home, ".config")
	}
	return filepath.Join(configDir, "shareto", "config.json")
}

// LoadOrInit loads the config file from disk. If it does not exist,
// it generates and writes the default config.
func LoadOrInit(customPath ...string) (*Config, error) {
	path := DefaultConfigPath()
	if len(customPath) > 0 && customPath[0] != "" {
		path = customPath[0]
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			cfg := DefaultConfig()
			if err := SaveConfig(path, cfg); err != nil {
				return nil, fmt.Errorf("failed to save default config: %w", err)
			}
			return cfg, nil
		}
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	cfg := DefaultConfig()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config JSON: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) Validate() error {
	if c.MaxConnections < 1 || c.MaxConnections > 65536 || c.MaxConnectionsPerIP < 1 || c.MaxConnectionsPerIP > c.MaxConnections {
		return fmt.Errorf("connection limits must satisfy 1 <= max_connections_per_ip <= max_connections <= 65536")
	}
	if c.MaxConcurrentAuth < 1 || c.MaxConcurrentAuth > 16 {
		return fmt.Errorf("max_concurrent_auth must be between 1 and 16")
	}
	if c.MaxActiveShares < 1 || c.MaxGlobalStreams < 1 || c.MaxStreamsPerToken < 1 || c.MaxStreamsPerSession < 1 || c.MinTransferRateKBps < 0 {
		return fmt.Errorf("share and stream limits must be positive and transfer rate must be nonnegative")
	}
	return nil
}

// SaveConfig writes the config to disk with restricted permissions.
func SaveConfig(path string, cfg *Config) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create config dir: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	tmpPath := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write temp config: %w", err)
	}
	if runtime.GOOS == "windows" {
		_ = os.Remove(path)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to commit config file: %w", err)
	}
	return nil
}
