package main

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"syscall"
	"time"

	"github.com/freelux/downbox/aria2"
	"github.com/freelux/downbox/files"
)

// NewServer creates and configures the HTTP mux with all routes.
func NewServer(cfg *Config, aria2Client *aria2.Client, fileHandler *files.Handler, tunnelMgr *TunnelManager, webFS http.FileSystem) http.Handler {
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

	// --- Setup wizard ---
	mux.HandleFunc("GET /api/setup/status", handleSetupStatus(cfg))
	mux.HandleFunc("GET /api/setup/defaults", handleSetupDefaults())
	mux.HandleFunc("POST /api/setup/save", handleSetupSave(cfg, tunnelMgr))

	// --- System ---
	mux.HandleFunc("GET /api/status", handleStatus(cfg, aria2Client, tunnelMgr))

	// --- WebUI ---
	mux.Handle("GET /", http.FileServer(webFS))

	return withMiddleware(mux)
}

// --- Middleware ---

func withMiddleware(next http.Handler) http.Handler {
	return recoveryMiddleware(loggingMiddleware(next))
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

// --- System handler ---

func handleStatus(cfg *Config, client *aria2.Client, tunnelMgr *TunnelManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status := map[string]interface{}{
			"publicURL": cfg.PublicURL,
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
