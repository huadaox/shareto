package httpd

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"shareto/internal/config"
	"shareto/internal/crypto"
	"shareto/internal/netutil"
	"shareto/internal/store"
)

var crawlerUserAgents = []string{
	"micromessenger",
	"telegrambot",
	"twitterbot",
	"slackbot",
	"discordbot",
	"facebookexternalhit",
	"whatsapp",
	"applebot",
}

func isCrawler(ua string) bool {
	uaLower := strings.ToLower(ua)
	for _, crawler := range crawlerUserAgents {
		if strings.Contains(uaLower, crawler) {
			return true
		}
	}
	return false
}

type ShareHandler struct {
	cfg          *config.Config
	storeMgr     *store.Manager
	rateLimiter  *netutil.RateLimiter
	trustedCIDRs []*net.IPNet
}

func NewShareHandler(cfg *config.Config, storeMgr *store.Manager, rl *netutil.RateLimiter, trustedCIDRs []*net.IPNet) *ShareHandler {
	return &ShareHandler{
		cfg:          cfg,
		storeMgr:     storeMgr,
		rateLimiter:  rl,
		trustedCIDRs: trustedCIDRs,
	}
}

func (h *ShareHandler) renderError(w http.ResponseWriter, status int, title, message string) {
	log.Printf("[HTTP %d] %s: %s", status, title, message)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = RenderErrorPage(w, ErrorPageData{
		StatusCode: status,
		Title:      title,
		Message:    message,
	})
}

// HandleGetShare: GET /s/{token}
func (h *ShareHandler) HandleGetShare(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(strings.TrimSuffix(r.PathValue("token"), "/"))
	if token == "" {
		h.renderError(w, http.StatusNotFound, "Not Found", "Invalid share token.")
		return
	}

	entry, err := h.storeMgr.GetShare(token)
	if err != nil {
		if errors.Is(err, store.ErrShareExpired) || errors.Is(err, store.ErrDownloadExhausted) {
			h.renderError(w, http.StatusGone, "Link Expired", "This share link has expired or reached its maximum download limit.")
			return
		}
		h.renderError(w, http.StatusNotFound, "Not Found", "The requested share does not exist or has been revoked.")
		return
	}

	// Verify file integrity
	if _, _, err := store.GetSectionReader(entry); err != nil {
		if err := h.storeMgr.RemoveShare(token); err != nil {
			h.renderError(w, http.StatusServiceUnavailable, "Storage Unavailable", "Unable to persist share removal.")
			return
		}
		h.renderError(w, http.StatusGone, "File Unavailable", "The original file has been altered or removed.")
		return
	}

	clientIP := netutil.ExtractClientIP(r, h.trustedCIDRs)
	clientSubnet := netutil.SessionSubnetKey(clientIP)

	// Direct link terminal mode: raw=1 or X-Shareto-Direct: 1
	isRaw := r.URL.Query().Get("raw") == "1" || r.Header.Get("X-Shareto-Direct") == "1"
	if r.Method == http.MethodGet && isRaw && entry.PasswordHash == "" {
		if !isCrawler(r.UserAgent()) {
			_, session, err := h.storeMgr.ClaimSession(token, clientIP)
			if err != nil {
				if errors.Is(err, store.ErrDownloadExhausted) || errors.Is(err, store.ErrShareExpired) || errors.Is(err, store.ErrShareNotFound) {
					h.renderError(w, http.StatusGone, "Link Expired", "This share is no longer available.")
				} else {
					h.renderError(w, http.StatusServiceUnavailable, "Storage Unavailable", "Unable to persist download authorization.")
				}
				return
			}
			redirectURL := fmt.Sprintf("/d/%s/%s/%s", token, session.SessionID, url.PathEscape(entry.FileName))
			http.Redirect(w, r, redirectURL, http.StatusTemporaryRedirect) // 307
			return
		}
	}

	// Normal browser landing page
	csrfToken := crypto.GenerateCSRFToken(h.storeMgr.CSRFSecret(), token, clientSubnet, 10*time.Minute)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = RenderLandingPage(w, SharePageData{
		Token:              entry.Token,
		FileName:           entry.FileName,
		FileSizeFormatted:  FormatBytes(entry.FileSize),
		RemainingDownloads: entry.Remaining(),
		ExpiresAtFormatted: entry.ExpiresAt.Format("2006-01-02 15:04:05 MST"),
		HasPassword:        entry.PasswordHash != "",
		CSRFToken:          csrfToken,
	})
}

// HandleClaimShare: POST /s/{token}/claim
func (h *ShareHandler) HandleClaimShare(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(strings.TrimSuffix(r.PathValue("token"), "/"))
	if token == "" {
		h.renderError(w, http.StatusNotFound, "Not Found", "Invalid share token.")
		return
	}

	// 1. Limit body size to 4KB
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		h.renderError(w, http.StatusBadRequest, "Bad Request", "Malformed form submission.")
		return
	}

	clientIP := netutil.ExtractClientIP(r, h.trustedCIDRs)
	clientSubnet := netutil.SessionSubnetKey(clientIP)

	// 2. Validate CSRF token
	csrfToken := r.FormValue("csrf_token")
	if err := crypto.VerifyCSRFToken(h.storeMgr.CSRFSecret(), csrfToken, token, clientSubnet); err != nil {
		h.renderError(w, http.StatusBadRequest, "Invalid Request", "CSRF token verification failed or expired. Please refresh the page.")
		return
	}

	// 3. Rate limiter check
	if locked, remaining := h.rateLimiter.IsLocked(token, clientIP); locked {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", remaining))
		h.renderError(w, http.StatusTooManyRequests, "Too Many Requests", fmt.Sprintf("Too many failed attempts. Try again in %d seconds.", remaining))
		return
	}

	entry, err := h.storeMgr.GetShare(token)
	if err != nil {
		h.renderError(w, http.StatusGone, "Link Expired", "The share is no longer available.")
		return
	}

	// 4. Verify password if required
	if entry.PasswordHash != "" {
		password := r.FormValue("password")

		release, err := h.storeMgr.AcquireAuthSlots(entry)
		if err != nil {
			w.Header().Set("Retry-After", "1")
			h.renderError(w, http.StatusServiceUnavailable, "Service Unavailable", "Authentication server is currently busy, please retry.")
			return
		}
		defer release()
		finishAttempt, retryAfter := h.rateLimiter.BeginAttempt(token, clientIP)
		if finishAttempt == nil {
			w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
			h.renderError(w, http.StatusTooManyRequests, "Too Many Requests", "Too many password attempts. Please wait before retrying.")
			return
		}

		valid, err := crypto.VerifyPassword(password, entry.PasswordHash)
		finishAttempt(err == nil && valid)
		if err != nil || !valid {
			locked, rem := h.rateLimiter.IsLocked(token, clientIP)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			errMsg := "Incorrect password. Please try again."
			if locked {
				errMsg = fmt.Sprintf("Too many failed attempts. Locked for %d seconds.", rem)
			}
			newCSRF := crypto.GenerateCSRFToken(h.storeMgr.CSRFSecret(), token, clientSubnet, 10*time.Minute)
			_ = RenderLandingPage(w, SharePageData{
				Token:              entry.Token,
				FileName:           entry.FileName,
				FileSizeFormatted:  FormatBytes(entry.FileSize),
				RemainingDownloads: entry.Remaining(),
				ExpiresAtFormatted: entry.ExpiresAt.Format("2006-01-02 15:04:05 MST"),
				HasPassword:        true,
				CSRFToken:          newCSRF,
				ErrorMessage:       errMsg,
			})
			return
		}
	}

	// 5. Claim session and decrement download quota atomically
	_, session, err := h.storeMgr.ClaimSession(token, clientIP)
	if err != nil {
		if errors.Is(err, store.ErrDownloadExhausted) || errors.Is(err, store.ErrShareExpired) {
			h.renderError(w, http.StatusGone, "Download Limit Exceeded", "The download limit for this link has been reached.")
			return
		}
		h.renderError(w, http.StatusServiceUnavailable, "Storage Unavailable", "Unable to persist download authorization.")
		return
	}

	// 303 See Other
	downloadURL := fmt.Sprintf("/d/%s/%s/%s", token, session.SessionID, url.PathEscape(entry.FileName))
	http.Redirect(w, r, downloadURL, http.StatusSeeOther)
}

// HandleDownload: GET or HEAD /d/{token}/{session}/{filename}
func (h *ShareHandler) HandleDownload(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(strings.TrimSuffix(r.PathValue("token"), "/"))
	sessionID := strings.TrimSpace(strings.TrimSuffix(r.PathValue("session"), "/"))
	if token == "" || sessionID == "" {
		h.renderError(w, http.StatusBadRequest, "Bad Request", "Missing token or session parameter.")
		return
	}

	clientIP := netutil.ExtractClientIP(r, h.trustedCIDRs)

	// 1 & 2. Validate session and subnet binding
	entry, session, err := h.storeMgr.GetSession(token, sessionID, clientIP)
	if err != nil {
		if errors.Is(err, store.ErrSubnetMismatch) {
			h.renderError(w, http.StatusForbidden, "Forbidden", "Session is bound to another IP subnet.")
			return
		}
		h.renderError(w, http.StatusNotFound, "Session Expired", "Session expired or invalid.")
		return
	}

	// 3. Acquire 3-tier concurrency slots
	releaseSlots, err := h.storeMgr.AcquireStreamSlots(entry, session)
	if err != nil {
		w.Header().Set("Retry-After", "5")
		h.renderError(w, http.StatusTooManyRequests, "Concurrency Limit Exceeded", "Maximum active download streams reached. Please wait and retry.")
		return
	}
	defer releaseSlots()

	// 4. SectionReader and mutation check
	reader, mtime, err := store.GetSectionReader(entry)
	if err != nil {
		if err := h.storeMgr.RemoveShare(token); err != nil {
			h.renderError(w, http.StatusServiceUnavailable, "Storage Unavailable", "Unable to persist share removal.")
			return
		}
		h.renderError(w, http.StatusGone, "File Mutated", "The shared file has been altered or replaced.")
		return
	}

	// 5. Strict security response headers
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="download.bin"; filename*=UTF-8''%s`, url.PathEscape(entry.FileName)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Cache-Control", "private, no-transform, no-store")
	w.Header().Set("ETag", fmt.Sprintf(`"%x-%x"`, entry.Inode, entry.FileSize))

	// 6. Serve content with optional slow connection wrapper for GET
	var targetW http.ResponseWriter = w
	if r.Method == http.MethodGet {
		var cleanup func()
		targetW, cleanup = netutil.WrapSlowConn(w, r.Context(), h.cfg.MinTransferRateKBps)
		defer cleanup()
	}

	http.ServeContent(targetW, r, "", mtime, reader)
	if entry.FileMutated() {
		if err := h.storeMgr.RemoveShare(token); err != nil {
			log.Printf("failed to persist mutated share removal: %v", err)
		}
		panic(http.ErrAbortHandler)
	}
}
