package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Share represents an active file share with a unique token.
type Share struct {
	Token     string `json:"token"`
	Path      string `json:"path"`
	FileName  string `json:"fileName"`
	Type      string `json:"type"` // "local" or "public"
	URL       string `json:"url"`
	CreatedAt int64  `json:"createdAt"`
}

// ShareManager manages active file shares.
type ShareManager struct {
	shares map[string]*Share // keyed by token
	root   string
	mu     sync.RWMutex
}

func NewShareManager(root string) *ShareManager {
	return &ShareManager{
		shares: make(map[string]*Share),
		root:   root,
	}
}

func generateToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Create creates a new share for a file path with a given type and base URL.
func (sm *ShareManager) Create(filePath, shareType, baseURL string) (*Share, error) {
	full := filepath.Join(sm.root, filepath.Clean(filePath))
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		return nil, fmt.Errorf("file not found")
	}
	if !strings.HasPrefix(resolved+"/", sm.root+"/") && resolved != sm.root {
		return nil, fmt.Errorf("path not allowed")
	}

	token := generateToken()
	share := &Share{
		Token:     token,
		Path:      filePath,
		FileName:  filepath.Base(filePath),
		Type:      shareType,
		URL:       baseURL + "/s/" + token,
		CreatedAt: time.Now().Unix(),
	}

	sm.mu.Lock()
	// Limit shares to prevent memory exhaustion — check and insert under same lock
	if len(sm.shares) >= 1000 {
		sm.mu.Unlock()
		return nil, fmt.Errorf("maximum number of shares reached (1000)")
	}
	sm.shares[token] = share
	sm.mu.Unlock()

	return share, nil
}

// FindByPath returns all shares for a given file path.
func (sm *ShareManager) FindByPath(path string) []*Share {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	var result []*Share
	for _, s := range sm.shares {
		if s.Path == path {
			result = append(result, s)
		}
	}
	return result
}

// List returns all active shares.
func (sm *ShareManager) List() []*Share {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	list := make([]*Share, 0, len(sm.shares))
	for _, s := range sm.shares {
		list = append(list, s)
	}
	return list
}

// Delete removes a share by token.
func (sm *ShareManager) Delete(token string) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if _, ok := sm.shares[token]; ok {
		delete(sm.shares, token)
		return true
	}
	return false
}

// ServeShare handles GET /s/{token}.
func (sm *ShareManager) ServeShare(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")

	sm.mu.RLock()
	share, ok := sm.shares[token]
	sm.mu.RUnlock()

	if !ok {
		http.Error(w, "Share link expired or invalid", http.StatusNotFound)
		return
	}

	full := filepath.Join(sm.root, filepath.Clean(share.Path))
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}
	if !strings.HasPrefix(resolved+"/", sm.root+"/") && resolved != sm.root {
		http.Error(w, "Access denied", http.StatusForbidden)
		return
	}

	// Open with O_NOFOLLOW to prevent symlink swap race between EvalSymlinks and open.
	// Then serve via /proc/self/fd to ensure we serve the exact file we opened.
	f, err := os.OpenFile(resolved, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.IsDir() {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", info.Name()))
	// Serve from the opened fd via /proc/self/fd to avoid TOCTOU
	fdPath := fmt.Sprintf("/proc/self/fd/%d", f.Fd())
	http.ServeFile(w, r, fdPath)
}
