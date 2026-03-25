package files

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Handler struct {
	root string // absolute, symlink-resolved path
}

func NewHandler(root string) *Handler {
	return &Handler{root: root}
}

// FileEntry is a single file/directory entry in a listing.
type FileEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"` // relative to root
	IsDir   bool   `json:"isDir"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"modTime"`
	Type    string `json:"type"` // video, image, audio, archive, torrent, other
}

// safePath resolves a user path and ensures it's under root.
func (h *Handler) safePath(userPath string) (string, error) {
	if strings.ContainsRune(userPath, 0) {
		return "", fmt.Errorf("invalid path")
	}

	cleaned := filepath.Clean("/" + userPath)
	full := filepath.Join(h.root, cleaned)

	// Try resolving symlinks
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		if os.IsNotExist(err) {
			// For rename target, check parent exists and is under root
			parent, err2 := filepath.EvalSymlinks(filepath.Dir(full))
			if err2 != nil {
				return "", fmt.Errorf("invalid path")
			}
			if !strings.HasPrefix(parent+"/", h.root+"/") && parent != h.root {
				return "", fmt.Errorf("path traversal blocked")
			}
			return full, nil
		}
		return "", fmt.Errorf("invalid path: %w", err)
	}

	if !strings.HasPrefix(resolved+"/", h.root+"/") && resolved != h.root {
		return "", fmt.Errorf("path traversal blocked")
	}

	return resolved, nil
}

// relativePath returns the path relative to root for API responses.
func (h *Handler) relativePath(absPath string) string {
	rel, err := filepath.Rel(h.root, absPath)
	if err != nil {
		return absPath
	}
	if rel == "." {
		return ""
	}
	return rel
}

// fileType guesses file type from extension.
func fileType(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".mp4", ".mkv", ".avi", ".mov", ".wmv", ".flv", ".webm", ".m4v", ".ts", ".m2ts":
		return "video"
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".svg", ".ico":
		return "image"
	case ".mp3", ".flac", ".aac", ".ogg", ".wav", ".wma", ".m4a", ".opus":
		return "audio"
	case ".zip", ".rar", ".7z", ".tar", ".gz", ".bz2", ".xz", ".zst":
		return "archive"
	case ".torrent":
		return "torrent"
	case ".srt", ".sub", ".ass", ".vtt":
		return "subtitle"
	case ".nfo", ".txt", ".log":
		return "text"
	default:
		return "other"
	}
}

// HandleList lists directory contents.
// GET /api/files?path=
func (h *Handler) HandleList(w http.ResponseWriter, r *http.Request) {
	userPath := r.URL.Query().Get("path")
	dirPath, err := h.safePath(userPath)
	if err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "directory not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Directories first, then files, alphabetically
	var dirs, files []FileEntry
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		absPath := filepath.Join(dirPath, e.Name())
		entry := FileEntry{
			Name:    e.Name(),
			Path:    h.relativePath(absPath),
			IsDir:   e.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime().Unix(),
		}
		if e.IsDir() {
			entry.Type = "folder"
			dirs = append(dirs, entry)
		} else {
			entry.Type = fileType(e.Name())
			files = append(files, entry)
		}
	}

	result := make([]FileEntry, 0, len(dirs)+len(files))
	result = append(result, dirs...)
	result = append(result, files...)

	writeJSON(w, http.StatusOK, result)
}

// HandleDownload serves a file for download or inline preview.
// GET /api/files/download?path=&inline=true
func (h *Handler) HandleDownload(w http.ResponseWriter, r *http.Request) {
	userPath := r.URL.Query().Get("path")
	filePath, err := h.safePath(userPath)
	if err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	info, err := os.Stat(filePath)
	if err != nil {
		writeError(w, http.StatusNotFound, "file not found")
		return
	}
	if info.IsDir() {
		writeError(w, http.StatusBadRequest, "cannot download a directory")
		return
	}

	inline := r.URL.Query().Get("inline") == "true"
	if inline {
		w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", info.Name()))
	} else {
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", info.Name()))
	}

	http.ServeFile(w, r, filePath)
}

// HandleDelete deletes a file or directory.
// DELETE /api/files?path=
func (h *Handler) HandleDelete(w http.ResponseWriter, r *http.Request) {
	userPath := r.URL.Query().Get("path")
	if userPath == "" || userPath == "/" || userPath == "." {
		writeError(w, http.StatusForbidden, "cannot delete root directory")
		return
	}

	targetPath, err := h.safePath(userPath)
	if err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	if targetPath == h.root {
		writeError(w, http.StatusForbidden, "cannot delete root directory")
		return
	}

	if err := os.RemoveAll(targetPath); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// HandleRename renames a file or directory.
// POST /api/files/rename  {"from": "path/to/file", "to": "newname"}
func (h *Handler) HandleRename(w http.ResponseWriter, r *http.Request) {
	var req struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	// "to" must be just a filename, not a path
	if filepath.Base(req.To) != req.To || req.To == "." || req.To == ".." {
		writeError(w, http.StatusBadRequest, "to must be a simple filename")
		return
	}

	fromPath, err := h.safePath(req.From)
	if err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	toPath := filepath.Join(filepath.Dir(fromPath), req.To)
	// Verify destination is still under root
	if _, err := h.safePath(h.relativePath(toPath)); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	if err := os.Rename(fromPath, toPath); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "renamed"})
}

// HandleUpload saves an uploaded file into the given directory.
// POST /api/files/upload  (multipart/form-data: file + path)
func (h *Handler) HandleUpload(w http.ResponseWriter, r *http.Request) {
	// 10 GB max
	r.Body = http.MaxBytesReader(w, r.Body, 10<<30)

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "invalid multipart form")
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing file field")
		return
	}
	defer file.Close()

	// Sanitize filename
	fileName := filepath.Base(header.Filename)
	if fileName == "." || fileName == ".." || fileName == "" {
		writeError(w, http.StatusBadRequest, "invalid filename")
		return
	}

	// Target directory (current browsed path)
	dirPath := r.FormValue("path")
	targetDir, err := h.safePath(dirPath)
	if err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	info, err := os.Stat(targetDir)
	if err != nil || !info.IsDir() {
		writeError(w, http.StatusBadRequest, "target directory not found")
		return
	}

	destPath := filepath.Join(targetDir, fileName)

	// Avoid overwriting: use O_EXCL to prevent race conditions
	ext := filepath.Ext(fileName)
	base := strings.TrimSuffix(fileName, ext)
	dst, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	for i := 1; os.IsExist(err); i++ {
		destPath = filepath.Join(targetDir, fmt.Sprintf("%s_%d%s", base, i, ext))
		dst, err = os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create file: "+err.Error())
		return
	}
	defer dst.Close()

	written, err := io.Copy(dst, file)
	if err != nil {
		os.Remove(destPath)
		writeError(w, http.StatusInternalServerError, "write file: "+err.Error())
		return
	}

	relPath := h.relativePath(destPath)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"name": filepath.Base(destPath),
		"path": relPath,
		"size": written,
	})
}

// HandleInfo returns metadata for a file or directory.
// GET /api/files/info?path=
func (h *Handler) HandleInfo(w http.ResponseWriter, r *http.Request) {
	userPath := r.URL.Query().Get("path")
	targetPath, err := h.safePath(userPath)
	if err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	info, err := os.Stat(targetPath)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	entry := FileEntry{
		Name:    info.Name(),
		Path:    h.relativePath(targetPath),
		IsDir:   info.IsDir(),
		Size:    info.Size(),
		ModTime: info.ModTime().Unix(),
	}
	if info.IsDir() {
		entry.Type = "folder"
		// Calculate dir size (with timeout)
		entry.Size = dirSize(targetPath, 3*time.Second)
	} else {
		entry.Type = fileType(info.Name())
	}

	writeJSON(w, http.StatusOK, entry)
}

// dirSize calculates total size of a directory with a timeout.
func dirSize(path string, timeout time.Duration) int64 {
	var total int64
	deadline := time.Now().Add(timeout)
	filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil || time.Now().After(deadline) {
			return filepath.SkipDir
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
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
