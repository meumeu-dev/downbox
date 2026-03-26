package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// ModuleDef describes an available module
type ModuleDef struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Binary      string            `json:"binary"` // binary name inside modules dir
	Downloads   map[string]string `json:"downloads"` // arch → URL
	NeedsRoot   bool              `json:"needsRoot"`
}

// ModuleStatus represents current state of a module
type ModuleStatus struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Installed   bool   `json:"installed"`
	Version     string `json:"version,omitempty"`
	NeedsRoot   bool   `json:"needsRoot"`
	Path        string `json:"path,omitempty"`
}

// ModuleManager handles module install/remove/list
type ModuleManager struct {
	modulesDir string
	registry   []ModuleDef
	mu         sync.Mutex
}

func NewModuleManager() *ModuleManager {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".config", "downbox", "modules")
	os.MkdirAll(dir, 0o700)

	return &ModuleManager{
		modulesDir: dir,
		registry:   builtinRegistry(),
	}
}

// builtinRegistry returns the list of available modules
func builtinRegistry() []ModuleDef {
	ytdlpBase := "https://github.com/yt-dlp/yt-dlp/releases/latest/download/"
	rcloneBase := "https://downloads.rclone.org/rclone-current-"

	return []ModuleDef{
		{
			Name:        "yt-dlp",
			Description: "Download videos from YouTube, Twitch, and 1000+ sites",
			Binary:      "yt-dlp",
			Downloads: map[string]string{
				"amd64": ytdlpBase + "yt-dlp_linux",
				"arm64": ytdlpBase + "yt-dlp_linux_aarch64",
				"armv7": ytdlpBase + "yt-dlp_linux_armv7l",
				"i386":  ytdlpBase + "yt-dlp_linux",
			},
		},
		{
			Name:        "rclone",
			Description: "Sync files to Google Drive, S3, Dropbox, and 40+ cloud providers",
			Binary:      "rclone",
			Downloads: map[string]string{
				"amd64": rcloneBase + "linux-amd64.zip",
				"arm64": rcloneBase + "linux-arm64.zip",
				"armv7": rcloneBase + "linux-arm.zip",
				"i386":  rcloneBase + "linux-386.zip",
			},
		},
		{
			Name:        "wireguard",
			Description: "VPN tunnel — route downloads through WireGuard",
			Binary:      "wg-quick",
			NeedsRoot:   true,
			Downloads:   map[string]string{}, // installed via system package manager
		},
		{
			Name:        "openvpn",
			Description: "VPN tunnel — route downloads through OpenVPN",
			Binary:      "openvpn",
			NeedsRoot:   true,
			Downloads:   map[string]string{}, // installed via system package manager
		},
	}
}

// List returns status of all modules
func (mm *ModuleManager) List() []ModuleStatus {
	var result []ModuleStatus
	for _, mod := range mm.registry {
		status := ModuleStatus{
			Name:        mod.Name,
			Description: mod.Description,
			NeedsRoot:   mod.NeedsRoot,
		}

		// Check if installed (in modules dir or system PATH)
		binPath := filepath.Join(mm.modulesDir, mod.Name, mod.Binary)
		if _, err := os.Stat(binPath); err == nil {
			status.Installed = true
			status.Path = binPath
		} else if path, err := exec.LookPath(mod.Binary); err == nil {
			status.Installed = true
			status.Path = path
		}

		// Don't auto-execute binaries for version check on every list call.
		// Version is only checked on explicit request, not on page load.
		if status.Installed {
			status.Version = "installed"
		}

		result = append(result, status)
	}
	return result
}

// IsInstalled checks if a specific module is available
func (mm *ModuleManager) IsInstalled(name string) bool {
	for _, s := range mm.List() {
		if s.Name == name && s.Installed {
			return true
		}
	}
	return false
}

// BinPath returns the path to a module's binary, or empty if not installed
func (mm *ModuleManager) BinPath(name string) string {
	for _, s := range mm.List() {
		if s.Name == name && s.Installed {
			return s.Path
		}
	}
	return ""
}

// Install downloads and installs a module
func (mm *ModuleManager) Install(name string) error {
	mm.mu.Lock()
	defer mm.mu.Unlock()
	mod := mm.findMod(name)
	if mod == nil {
		return fmt.Errorf("unknown module: %s", name)
	}

	// VPN modules need system package manager
	if mod.NeedsRoot && len(mod.Downloads) == 0 {
		return mm.installSystemPackage(name)
	}

	arch := goArch()
	url, ok := mod.Downloads[arch]
	if !ok {
		return fmt.Errorf("module %s not available for %s", name, arch)
	}

	destDir := filepath.Join(mm.modulesDir, mod.Name)
	os.MkdirAll(destDir, 0o700)
	destPath := filepath.Join(destDir, mod.Binary)

	slog.Info("downloading module", "name", name, "arch", arch)

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}

	// Handle zip files (rclone)
	if strings.HasSuffix(url, ".zip") {
		return mm.installFromZip(resp.Body, destDir, mod.Binary)
	}

	// Direct binary download
	f, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := io.Copy(f, io.LimitReader(resp.Body, 200<<20)); err != nil {
		os.Remove(destPath)
		return err
	}

	slog.Info("module installed", "name", name, "path", destPath)
	return nil
}

// Remove uninstalls a module
func (mm *ModuleManager) Remove(name string) error {
	mm.mu.Lock()
	defer mm.mu.Unlock()
	mod := mm.findMod(name)
	if mod == nil {
		return fmt.Errorf("unknown module: %s", name)
	}

	destDir := filepath.Join(mm.modulesDir, mod.Name)
	if err := os.RemoveAll(destDir); err != nil {
		return err
	}

	slog.Info("module removed", "name", name)
	return nil
}

func (mm *ModuleManager) findMod(name string) *ModuleDef {
	for i := range mm.registry {
		if mm.registry[i].Name == name {
			return &mm.registry[i]
		}
	}
	return nil
}

func (mm *ModuleManager) getVersion(mod ModuleDef, binPath string) string {
	var cmd *exec.Cmd
	switch mod.Name {
	case "yt-dlp":
		cmd = exec.Command(binPath, "--version")
	case "rclone":
		cmd = exec.Command(binPath, "version", "--check")
	default:
		cmd = exec.Command(binPath, "--version")
	}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	v := strings.TrimSpace(string(out))
	if len(v) > 30 {
		v = v[:30]
	}
	return v
}

func (mm *ModuleManager) installSystemPackage(name string) error {
	var pkg string
	switch name {
	case "wireguard":
		pkg = "wireguard-tools"
	case "openvpn":
		pkg = "openvpn"
	default:
		return fmt.Errorf("unknown system package for %s", name)
	}

	// Try common package managers
	for _, pm := range []struct {
		check string
		args  []string
	}{
		{"apt-get", []string{"sudo", "apt-get", "install", "-y", "-qq", pkg}},
		{"apk", []string{"sudo", "apk", "add", "--quiet", pkg}},
		{"dnf", []string{"sudo", "dnf", "install", "-y", "-q", pkg}},
		{"pacman", []string{"sudo", "pacman", "-S", "--noconfirm", pkg}},
	} {
		if _, err := exec.LookPath(pm.check); err == nil {
			cmd := exec.Command(pm.args[0], pm.args[1:]...)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			return cmd.Run()
		}
	}

	return fmt.Errorf("install %s manually: no supported package manager found", pkg)
}

func (mm *ModuleManager) installFromZip(r io.Reader, destDir, binaryName string) error {
	// Download to temp file first
	tmpFile, err := os.CreateTemp("", "downbox-module-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(tmpFile.Name())

	if _, err := io.Copy(tmpFile, io.LimitReader(r, 200<<20)); err != nil {
		tmpFile.Close()
		return err
	}
	tmpFile.Close()

	// Extract using unzip (available on most systems)
	extractDir, err := os.MkdirTemp("", "downbox-extract-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(extractDir)

	cmd := exec.Command("unzip", "-o", "-q", tmpFile.Name(), "-d", extractDir)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("unzip failed: %w", err)
	}

	// Find the binary recursively
	var found string
	filepath.Walk(extractDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if info.Name() == binaryName {
			found = path
			return filepath.SkipAll
		}
		return nil
	})

	if found == "" {
		return fmt.Errorf("binary %s not found in archive", binaryName)
	}

	// Reject symlinks (zip slip prevention)
	fi, err := os.Lstat(found)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("symlink in archive rejected: %s", binaryName)
	}

	destPath := filepath.Join(destDir, binaryName)
	data, err := os.ReadFile(found)
	if err != nil {
		return err
	}
	return os.WriteFile(destPath, data, 0o755)
}

func goArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "amd64"
	case "arm64":
		return "arm64"
	case "arm":
		return "armv7"
	case "386":
		return "i386"
	default:
		return runtime.GOARCH
	}
}

// --- HTTP handlers for module management ---

func handleModuleList(mm *ModuleManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, mm.List())
	}
}

func handleModuleInstall(mm *ModuleManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if err := mm.Install(req.Name); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "installed"})
	}
}

func handleModuleRemove(mm *ModuleManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if err := mm.Remove(name); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
	}
}
