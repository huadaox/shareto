package httpd

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"shareto/internal/config"
	"shareto/internal/netutil"
	"shareto/internal/store"
)

type Server struct {
	cfg         *config.Config
	storeMgr    *store.Manager
	rateLimiter *netutil.RateLimiter
	httpServer  *http.Server
	listener    net.Listener
	listenerMu  sync.RWMutex
	trusted     []*net.IPNet
}

func NewServer(cfg *config.Config, storeMgr *store.Manager) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	trustedCIDRs, err := netutil.ParseCIDRs(cfg.TrustedProxies)
	if err != nil {
		return nil, fmt.Errorf("failed to parse trusted proxies: %w", err)
	}

	rateLimiter := netutil.NewRateLimiter(netutil.DefaultLRUCapacity)
	handler := NewShareHandler(cfg, storeMgr, rateLimiter, trustedCIDRs)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>shareto</title><style>body{background:#0f172a;color:#f8fafc;font-family:system-ui,-apple-system,sans-serif;display:flex;align-items:center;justify-content:center;height:100vh;margin:0;text-align:center;}h1{margin-bottom:8px;font-size:24px;}p{color:#94a3b8;font-size:14px;}</style></head><body><div><h1>shareto service is active</h1><p>Please access via your specific file share link (/s/{token}).</p></div></body></html>`))
	})
	mux.HandleFunc("GET /s/{token}", handler.HandleGetShare)
	mux.HandleFunc("POST /s/{token}/claim", handler.HandleClaimShare)
	mux.HandleFunc("GET /d/{token}/{session}/{filename}", handler.HandleDownload)
	mux.HandleFunc("HEAD /d/{token}/{session}/{filename}", handler.HandleDownload)

	// Global security header middleware
	secureHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		mux.ServeHTTP(w, r)
	})

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           secureHandler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    64 * 1024,
	}

	return &Server{
		cfg:         cfg,
		storeMgr:    storeMgr,
		rateLimiter: rateLimiter,
		httpServer:  srv,
		trusted:     trustedCIDRs,
	}, nil
}

func expandPath(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

// Start listens and serves HTTP/HTTPS traffic.
func (s *Server) Start() error {
	raw, err := net.Listen("tcp", s.cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", s.cfg.ListenAddr, err)
	}
	var ln net.Listener = netutil.NewLimitedListener(raw, s.cfg.MaxConnections, s.cfg.MaxConnectionsPerIP, s.trusted)

	if s.cfg.TLS.Enabled {
		if s.cfg.TLS.CertFile == "" || s.cfg.TLS.KeyFile == "" {
			raw.Close()
			return fmt.Errorf("TLS is enabled but cert_file or key_file is missing")
		}
		certPath := expandPath(s.cfg.TLS.CertFile)
		keyPath := expandPath(s.cfg.TLS.KeyFile)
		cert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			raw.Close()
			return fmt.Errorf("failed to load TLS certificate: %w", err)
		}
		tlsConfig := &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}
		ln = tls.NewListener(ln, tlsConfig)
	}

	s.listenerMu.Lock()
	s.listener = ln
	s.listenerMu.Unlock()
	return s.httpServer.Serve(ln)
}

// Stop gracefully shuts down the HTTP server.
func (s *Server) Stop(ctx context.Context) error {
	if s.httpServer != nil {
		return s.httpServer.Shutdown(ctx)
	}
	return nil
}

// Addr returns the bound network address.
func (s *Server) Addr() net.Addr {
	s.listenerMu.RLock()
	defer s.listenerMu.RUnlock()
	if s.listener != nil {
		return s.listener.Addr()
	}
	return nil
}
