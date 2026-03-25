package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/freelux/downbox/aria2"
	"github.com/freelux/downbox/files"
)

// NewServer creates and configures the HTTP mux with all routes.
func NewServer(cfg *Config, aria2Client *aria2.Client, fileHandler *files.Handler, tunnelMgr *TunnelManager, shareMgr *ShareManager, webFS http.FileSystem) http.Handler {
	mux := http.NewServeMux()

	// --- Downloads (aria2 proxy) ---
	mux.HandleFunc("GET /api/downloads", handleListDownloads(aria2Client))
	mux.HandleFunc("POST /api/downloads", handleAddDownload(aria2Client))
	mux.HandleFunc("DELETE /api/downloads/{gid}", handleRemoveDownload(aria2Client))
	mux.HandleFunc("POST /api/downloads/{gid}/pause", handlePauseDownload(aria2Client))
	mux.HandleFunc("POST /api/downloads/{gid}/resume", handleResumeDownload(aria2Client))

	// --- Files ---
	mux.HandleFunc("GET /api/files", fileHandler.HandleList)
	mux.HandleFunc("GET /api/files/download", fileHandler.HandleDownload)
	mux.HandleFunc("DELETE /api/files", fileHandler.HandleDelete)
	mux.HandleFunc("POST /api/files/rename", fileHandler.HandleRename)
	mux.HandleFunc("GET /api/files/info", fileHandler.HandleInfo)
	mux.HandleFunc("POST /api/files/upload", fileHandler.HandleUpload)

	// --- Setup wizard ---
	mux.HandleFunc("GET /api/setup/status", handleSetupStatus(cfg))
	mux.HandleFunc("GET /api/setup/defaults", handleSetupDefaults())
	mux.HandleFunc("POST /api/setup/save", handleSetupSave(cfg, tunnelMgr))

	// --- Shares ---
	mux.HandleFunc("POST /api/shares", handleCreateShare(cfg, shareMgr, tunnelMgr))
	mux.HandleFunc("GET /api/shares", handleListShares(shareMgr))
	mux.HandleFunc("GET /api/shares/file", handleFileShares(shareMgr))
	mux.HandleFunc("DELETE /api/shares/{token}", handleDeleteShare(shareMgr))
	mux.HandleFunc("GET /s/{token}", shareMgr.ServeShare)

	// --- System ---
	mux.HandleFunc("GET /api/status", handleStatus(cfg, aria2Client, tunnelMgr))

	// --- WebUI ---
	mux.Handle("GET /", http.FileServer(webFS))

	return withMiddleware(cfg, mux)
}

// --- Middleware ---

func withMiddleware(cfg *Config, next http.Handler) http.Handler {
	return recoveryMiddleware(securityHeaders(loggingMiddleware(authMiddleware(cfg, next))))
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-eval'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; media-src 'self' blob:")
		next.ServeHTTP(w, r)
	})
}

func authMiddleware(cfg *Config, next http.Handler) http.Handler {
	passHash := sha256.Sum256([]byte(cfg.Password))

	// Session tokens: map[tokenHash] -> expiry
	var sessions sync.Map

	// Rate limiter for login: map[ip] -> lastAttempt
	var loginAttempts sync.Map

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Share links are public — no auth needed
		if strings.HasPrefix(r.URL.Path, "/s/") {
			next.ServeHTTP(w, r)
			return
		}

		// Setup wizard endpoints — allow without auth when setup not done
		if !cfg.SetupDone && (r.URL.Path == "/api/setup/status" || r.URL.Path == "/api/setup/defaults" || r.URL.Path == "/api/setup/save") {
			next.ServeHTTP(w, r)
			return
		}

		// Check session cookie
		if c, err := r.Cookie("downbox_session"); err == nil {
			tokenHash := sha256.Sum256([]byte(c.Value))
			if exp, ok := sessions.Load(tokenHash); ok {
				if time.Now().Before(exp.(time.Time)) {
					next.ServeHTTP(w, r)
					return
				}
				sessions.Delete(tokenHash)
			}
		}

		// Login endpoint
		if r.URL.Path == "/api/login" && r.Method == "POST" {
			// Rate limit: 1 attempt per 2 seconds per IP
			ip, _, _ := net.SplitHostPort(r.RemoteAddr)
			if ip == "" {
				ip = r.RemoteAddr
			}
			if last, ok := loginAttempts.Load(ip); ok {
				if time.Since(last.(time.Time)) < 2*time.Second {
					writeError(w, http.StatusTooManyRequests, "too many attempts, wait a moment")
					return
				}
			}
			loginAttempts.Store(ip, time.Now())

			var req struct {
				Password string `json:"password"`
			}
			if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
				writeError(w, http.StatusBadRequest, "invalid JSON")
				return
			}
			h := sha256.Sum256([]byte(req.Password))
			if subtle.ConstantTimeCompare(h[:], passHash[:]) == 1 {
				// Generate random session token
				tokenBytes := make([]byte, 32)
				rand.Read(tokenBytes)
				token := hex.EncodeToString(tokenBytes)
				tokenHash := sha256.Sum256([]byte(token))
				sessions.Store(tokenHash, time.Now().Add(30*24*time.Hour))

				http.SetCookie(w, &http.Cookie{
					Name:     "downbox_session",
					Value:    token,
					Path:     "/",
					MaxAge:   86400 * 30,
					HttpOnly: true,
					SameSite: http.SameSiteLaxMode,
				})
				writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
			} else {
				writeError(w, http.StatusUnauthorized, "wrong password")
			}
			return
		}

		// Static assets (CSS, JS, vendor) — allow without auth for login page
		if !strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/" {
			next.ServeHTTP(w, r)
			return
		}

		// Not authenticated
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeError(w, http.StatusUnauthorized, "authentication required")
		} else {
			next.ServeHTTP(w, r)
		}
	})
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			return
		}
		slog.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"ms", time.Since(start).Milliseconds(),
		)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				slog.Error("panic recovered", "error", err, "path", r.URL.Path)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// --- JSON helpers ---

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// --- Security helpers ---

// isPrivateIP checks if an IP is internal/private/loopback/link-local
func isPrivateIP(ip net.IP) bool {
	if ip == nil {
		return true // treat unparseable as blocked
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() ||
		ip.Equal(net.ParseIP("169.254.169.254")) // AWS metadata
}

// validateDownloadURL blocks SSRF: file://, internal IPs (including decimal/hex/octal), metadata endpoints
func validateDownloadURL(rawURL string) error {
	lower := strings.ToLower(rawURL)

	// Only allow http(s), magnet, ftp
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") &&
		!strings.HasPrefix(lower, "ftp://") && !strings.HasPrefix(lower, "magnet:") {
		return fmt.Errorf("only http(s), ftp and magnet URLs are allowed")
	}

	// Magnet links are safe (no network target to validate)
	if strings.HasPrefix(lower, "magnet:") {
		return nil
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL")
	}

	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("missing host")
	}

	// Block localhost variants
	if host == "localhost" || host == "0.0.0.0" || strings.HasSuffix(host, ".local") {
		return fmt.Errorf("internal hosts are not allowed")
	}

	// First: try parsing the host directly as an IP (catches decimal 2130706433, hex 0x7f000001, etc.)
	// net.ParseIP only handles standard notation. For decimal/hex/octal, use net.ResolveIPAddr
	if ipAddr, err := net.ResolveIPAddr("ip", host); err == nil {
		if isPrivateIP(ipAddr.IP) {
			return fmt.Errorf("internal/private IPs are not allowed")
		}
		return nil // valid public IP literal
	}

	// It's a hostname — resolve via DNS
	ips, err := net.LookupHost(host)
	if err != nil {
		// Can't resolve = block it (don't let aria2 try something we can't validate)
		return fmt.Errorf("cannot resolve host: %s", host)
	}

	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if isPrivateIP(ip) {
			return fmt.Errorf("internal/private IPs are not allowed")
		}
	}

	return nil
}

// filterAria2Options whitelist — only safe options pass through
func filterAria2Options(opts map[string]string) map[string]string {
	if opts == nil {
		return nil
	}
	allowed := map[string]bool{
		"split": true, "max-connection-per-server": true,
		"min-split-size": true, "header": true, "referer": true,
		"user-agent": true, "check-integrity": true,
	}
	safe := make(map[string]string)
	for k, v := range opts {
		if allowed[k] {
			safe[k] = v
		}
	}
	if len(safe) == 0 {
		return nil
	}
	return safe
}

// --- Download handlers ---

func handleListDownloads(client *aria2.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		downloads, err := client.ListAll()
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"downloads":    []interface{}{},
				"aria2_online": false,
				"error":        err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"downloads":    downloads,
			"aria2_online": true,
		})
	}
}

func handleAddDownload(client *aria2.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		contentType := r.Header.Get("Content-Type")
		if strings.HasPrefix(contentType, "multipart/form-data") {
			if err := r.ParseMultipartForm(10 << 20); err != nil {
				writeError(w, http.StatusBadRequest, "invalid multipart form")
				return
			}
			file, _, err := r.FormFile("torrent")
			if err != nil {
				writeError(w, http.StatusBadRequest, "missing torrent file")
				return
			}
			defer file.Close()
			data, err := io.ReadAll(io.LimitReader(file, 10<<20))
			if err != nil {
				writeError(w, http.StatusInternalServerError, "read torrent: "+err.Error())
				return
			}
			gid, err := client.AddTorrent(base64.StdEncoding.EncodeToString(data))
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"gid": gid})
			return
		}

		var req struct {
			URL     string            `json:"url"`
			Options map[string]string `json:"options,omitempty"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if req.URL == "" {
			writeError(w, http.StatusBadRequest, "url is required")
			return
		}

		// Block dangerous URLs (SSRF)
		if err := validateDownloadURL(req.URL); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		// Whitelist safe aria2 options only
		req.Options = filterAria2Options(req.Options)

		gid, err := client.AddURI([]string{req.URL}, req.Options)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"gid": gid})
	}
}

func handleRemoveDownload(client *aria2.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		gid := r.PathValue("gid")
		if err := client.Remove(gid); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
	}
}

func handlePauseDownload(client *aria2.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		gid := r.PathValue("gid")
		if err := client.Pause(gid); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "paused"})
	}
}

func handleResumeDownload(client *aria2.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		gid := r.PathValue("gid")
		if err := client.Resume(gid); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "resumed"})
	}
}

// --- Setup handlers ---

func handleSetupStatus(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"needsSetup": !cfg.SetupDone,
		})
	}
}

func handleSetupDefaults() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"port":        8080,
			"downloadDir": "~/Downloads",
			"tunnel":      "none",
			"tools":       AvailableTunnels(),
		})
	}
}

func handleSetupSave(cfg *Config, tunnelMgr *TunnelManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Port                int    `json:"port"`
			DownloadDir         string `json:"downloadDir"`
			Tunnel              string `json:"tunnel"`
			CloudflaredToken    string `json:"cloudflaredToken"`
			CloudflaredHostname string `json:"cloudflaredHostname"`
			BoreServer          string `json:"boreServer"`
			BoreSecret          string `json:"boreSecret"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}

		// Update config
		if req.Port > 0 {
			cfg.Port = req.Port
		}
		if req.DownloadDir != "" {
			cfg.DownloadDir = expandHome(req.DownloadDir)
		}
		cfg.Tunnel = req.Tunnel
		cfg.CloudflaredToken = req.CloudflaredToken
		cfg.CloudflaredHostname = req.CloudflaredHostname
		cfg.BoreServer = req.BoreServer
		cfg.BoreSecret = req.BoreSecret
		cfg.SetupDone = true

		// Set public URL based on tunnel
		switch cfg.Tunnel {
		case "cloudflared":
			if cfg.CloudflaredHostname != "" {
				cfg.PublicURL = "https://" + cfg.CloudflaredHostname
			}
		case "bore":
			cfg.PublicURL = "" // set dynamically when bore starts
		default:
			cfg.PublicURL = ""
		}

		// Save to disk
		if err := saveConfig(cfg); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		slog.Info("setup saved", "tunnel", cfg.Tunnel)

		// Start tunnel if configured
		if cfg.Tunnel != "" && cfg.Tunnel != "none" {
			tunnelMgr.Stop()
			tunnelMgr.cfg = *cfg
			if err := tunnelMgr.Start(); err != nil {
				slog.Warn("tunnel start failed after setup", "error", err)
			}
		}

		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
	}
}

// --- Share handlers ---

func handleCreateShare(cfg *Config, mgr *ShareManager, tunnelMgr *TunnelManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Path string `json:"path"`
			Type string `json:"type"` // "local" or "public"
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if req.Type == "" {
			req.Type = "local"
		}

		var baseURL string
		switch req.Type {
		case "local":
			baseURL = fmt.Sprintf("http://localhost:%d", cfg.Port)
		case "public":
			baseURL = cfg.PublicURL
			if _, tURL, _ := tunnelMgr.Status(); tURL != "" {
				baseURL = tURL
			}
			if baseURL == "" {
				writeError(w, http.StatusBadRequest, "no tunnel configured")
				return
			}
		default:
			writeError(w, http.StatusBadRequest, "type must be local or public")
			return
		}

		share, err := mgr.Create(req.Path, req.Type, baseURL)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, share)
	}
}

func handleListShares(mgr *ShareManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, mgr.List())
	}
}

func handleFileShares(mgr *ShareManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Query().Get("path")
		writeJSON(w, http.StatusOK, mgr.FindByPath(path))
	}
}

func handleDeleteShare(mgr *ShareManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.PathValue("token")
		if mgr.Delete(token) {
			writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
		} else {
			writeError(w, http.StatusNotFound, "share not found")
		}
	}
}

// --- System handler ---

func handleStatus(cfg *Config, client *aria2.Client, tunnelMgr *TunnelManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Convert downloadDir back to ~ form for display
		dlDir := cfg.DownloadDir
		if home, err := os.UserHomeDir(); err == nil {
			if strings.HasPrefix(dlDir, home) {
				dlDir = "~" + dlDir[len(home):]
			}
		}

		status := map[string]interface{}{
			"publicURL": cfg.PublicURL,
			"config": map[string]interface{}{
				"port":                cfg.Port,
				"downloadDir":         dlDir,
				"tunnel":              cfg.Tunnel,
				"cloudflaredHostname": cfg.CloudflaredHostname,
				"boreServer":          cfg.BoreServer,
			},
		}

		// Disk info
		var stat syscall.Statfs_t
		if err := syscall.Statfs(cfg.DownloadDir, &stat); err == nil {
			status["disk"] = map[string]interface{}{
				"total": stat.Blocks * uint64(stat.Bsize),
				"free":  stat.Bavail * uint64(stat.Bsize),
			}
		}

		// aria2 status
		if globalStat, err := client.GetGlobalStat(); err == nil {
			status["aria2"] = map[string]interface{}{
				"online":        true,
				"downloadSpeed": aria2.ParseSize(globalStat.DownloadSpeed),
				"uploadSpeed":   aria2.ParseSize(globalStat.UploadSpeed),
				"active":        aria2.ParseSize(globalStat.NumActive),
				"waiting":       aria2.ParseSize(globalStat.NumWaiting),
			}
		} else {
			status["aria2"] = map[string]interface{}{"online": false}
		}

		// Tunnel
		tStatus, tURL, tErr := tunnelMgr.Status()
		status["tunnel"] = map[string]interface{}{
			"type":   cfg.Tunnel,
			"status": tStatus,
			"url":    tURL,
			"error":  tErr,
		}

		writeJSON(w, http.StatusOK, status)
	}
}
