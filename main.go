package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/freelux/downbox/aria2"
	"github.com/freelux/downbox/files"
)

//go:embed web/*
var webFS embed.FS

var version = "dev"

// pidDir returns ~/.config/downbox, creating it if needed.
func pidDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/tmp"
	}
	dir := filepath.Join(home, ".config", "downbox")
	os.MkdirAll(dir, 0o700)
	return dir
}

func pidFilePath() string {
	return filepath.Join(pidDir(), "downbox.pid")
}

func portFilePath() string {
	return filepath.Join(pidDir(), "downbox.port")
}

type Config struct {
	mu                  sync.RWMutex `json:"-"`
	Port                int
	DownloadDir         string
	Aria2URL            string
	Aria2Secret         string
	Aria2Port           int
	PublicURL           string
	Tunnel              string // cloudflared, bore, none
	CloudflaredToken    string
	CloudflaredHostname string
	BoreServer          string
	BoreSecret          string
	Password            string
	DNSServers          string // comma-separated: "1.1.1.1,8.8.8.8"
	Interface           string // network interface for downloads: "tun0", "eth0"
	ExcludeTrackers     string // comma-separated tracker URIs to block, or "*"
	Proxy               string // SOCKS/HTTP proxy: "socks5://127.0.0.1:9050"
	BlocklistURL        string // URL to ipfilter.dat / blocklist (auto-downloaded)
	BlocklistPort       int    // fixed port for built-in SOCKS proxy (0 = auto)
	SetupDone           bool
	Dev                 bool
}

func main() {
	// Handle subcommands before flag parsing
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "start":
			cmdStart(os.Args[2:])
			return
		case "stop":
			cmdStop()
			return
		case "restart":
			cmdStop()
			time.Sleep(500 * time.Millisecond)
			cmdStart(os.Args[2:])
			return
		case "status":
			cmdStatus()
			return
		case "__daemon":
			// Forked by "start" — run server in foreground (we're already detached)
			runServer(os.Args[2:])
			return
		case "init":
			cmdInit()
			return
		case "update":
			cmdUpdate()
			return
		case "version":
			fmt.Println("downbox", version)
			return
		case "help", "-h", "--help":
			printUsage()
			return
		}
	}

	// No subcommand = foreground start
	runServer(os.Args[1:])
}

func cmdUpdate() {
	fmt.Println("Checking for updates...")

	// Detect arch
	arch := ""
	switch runtime.GOARCH {
	case "amd64":
		arch = "amd64"
	case "arm64":
		arch = "arm64"
	case "arm":
		arch = "armv7"
	default:
		fmt.Fprintf(os.Stderr, "Unsupported architecture: %s\n", runtime.GOARCH)
		os.Exit(1)
	}

	// Get latest version from GitHub
	url := fmt.Sprintf("https://github.com/meumeu-dev/downbox/releases/latest/download/downbox-%s", arch)

	// Download to temp (mktemp to avoid TOCTOU)
	tmpF, err := os.CreateTemp("", "downbox-update-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot create temp file: %v\n", err)
		os.Exit(1)
	}
	tmpFile := tmpF.Name()
	tmpF.Close()
	defer os.Remove(tmpFile) // cleanup on any exit path
	cmd := exec.Command("curl", "-fSL", "--progress-bar", url, "-o", tmpFile)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Download failed: %v\n", err)
		os.Exit(1)
	}

	// Make executable
	os.Chmod(tmpFile, 0o755)

	// Check new version
	out, err := exec.Command(tmpFile, "version").Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid binary: %v\n", err)
		os.Remove(tmpFile)
		os.Exit(1)
	}
	newVersion := strings.TrimSpace(string(out))
	fmt.Printf("Current: downbox %s\n", version)
	fmt.Printf("Latest:  %s\n", newVersion)

	if strings.Contains(newVersion, version) && version != "dev" {
		fmt.Println("Already up to date.")
		os.Remove(tmpFile)
		return
	}

	// Find where current binary is
	execPath, err := os.Executable()
	if err != nil {
		execPath = "/usr/local/bin/downbox"
	}
	execPath, _ = filepath.EvalSymlinks(execPath)

	// Replace binary (may need sudo)
	wasRunning := false
	if _, running := readPid(); running {
		wasRunning = true
		fmt.Println("Stopping DownBox...")
		cmdStop()
		time.Sleep(500 * time.Millisecond)
	}

	// Try direct copy first, fallback to sudo
	if err := copyFile(tmpFile, execPath); err != nil {
		cmd := exec.Command("sudo", "cp", tmpFile, execPath)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to update: %v\nTry: sudo cp %s %s\n", err, tmpFile, execPath)
			os.Exit(1)
		}
	}
	os.Remove(tmpFile)

	fmt.Printf("Updated to %s\n", newVersion)

	if wasRunning {
		fmt.Println("Restarting...")
		cmdStart(nil)
	}
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o755)
}

func cmdInit() {
	home, _ := os.UserHomeDir()
	configPath := filepath.Join(home, ".config", "downbox", "downbox.conf")

	if _, err := os.Stat(configPath); err == nil {
		fmt.Printf("Config already exists: %s\n", configPath)
		return
	}

	if err := generateDefaultConfig(configPath); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Config created: %s\n", configPath)
	fmt.Println("Edit it, then run: downbox start")
}

func printUsage() {
	fmt.Printf(`DownBox %s — Lightweight download station

Usage:
  downbox init                                 Generate config file
  downbox start   [-port 8080] [-public-url URL]   Start as daemon
  downbox stop                                 Stop daemon
  downbox restart                              Restart daemon
  downbox status                               Show status
  downbox update                               Update to latest version

Config file (searched in order):
  ./downbox.conf
  ~/.config/downbox/downbox.conf
  /etc/downbox/downbox.conf

CLI flags override config file values.

Options:
  -port           HTTP server port (default: 8080)
  -download-dir   Download directory (default: ~/Downloads)
  -aria2-port     aria2 RPC port (default: 6800)
  -aria2-secret   aria2 RPC secret (auto-generated if empty)
  -public-url     Public URL for share links
  -dev            Serve web/ from filesystem instead of embed
`, version)
}

// --- Subcommands ---

func cmdStart(args []string) {
	// Check if already running
	if pid, running := readPid(); running {
		fmt.Printf("DownBox already running (PID %d)\n", pid)
		os.Exit(1)
	}

	// Fork into background
	execPath, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot find executable: %v\n", err)
		os.Exit(1)
	}

	// Build args: replace "start" with "__daemon" marker
	daemonArgs := append([]string{"__daemon"}, args...)

	cmd := exec.Command(execPath, daemonArgs...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}

	// Redirect stdout/stderr to log file
	logPath := "/tmp/downbox.log"
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot open log file: %v\n", err)
		os.Exit(1)
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to start: %v\n", err)
		os.Exit(1)
	}

	// Wait briefly to check it didn't die immediately
	time.Sleep(800 * time.Millisecond)
	if cmd.Process != nil {
		// Check process is still alive
		if err := syscall.Kill(cmd.Process.Pid, 0); err != nil {
			fmt.Fprintf(os.Stderr, "DownBox failed to start. Check %s\n", logPath)
			os.Exit(1)
		}
	}

	fmt.Printf("DownBox started (PID %d)\n", cmd.Process.Pid)
	fmt.Printf("  Logs: %s\n", logPath)

	// Parse flags to show port/dir info
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	port := fs.Int("port", 8080, "")
	dlDir := fs.String("download-dir", "", "")
	publicURL := fs.String("public-url", "", "")
	fs.String("aria2-port", "", "")
	fs.String("aria2-secret", "", "")
	fs.Bool("dev", false, "")
	fs.Parse(args)

	if *dlDir == "" {
		home, _ := os.UserHomeDir()
		*dlDir = filepath.Join(home, "Downloads")
	}
	fmt.Printf("  URL:  http://localhost:%d\n", *port)
	if *publicURL != "" {
		fmt.Printf("  Public: %s\n", *publicURL)
	}
	fmt.Printf("  Dir:  %s\n", *dlDir)
}

func cmdStop() {
	pid, running := readPid()
	if !running {
		fmt.Println("DownBox is not running")
		return
	}

	// Send SIGTERM to the process group
	fmt.Printf("Stopping DownBox (PID %d)...\n", pid)
	syscall.Kill(-pid, syscall.SIGTERM)

	// Wait for exit (up to 5s)
	for i := 0; i < 50; i++ {
		time.Sleep(100 * time.Millisecond)
		if err := syscall.Kill(pid, 0); err != nil {
			os.Remove(pidFilePath())
			fmt.Println("DownBox stopped")
			return
		}
	}

	// Force kill
	fmt.Println("Force killing...")
	syscall.Kill(-pid, syscall.SIGKILL)
	os.Remove(pidFilePath())
	fmt.Println("DownBox killed")
}

func cmdStatus() {
	pid, running := readPid()
	if !running {
		fmt.Println("DownBox is not running")
		os.Exit(1)
	}

	fmt.Printf("DownBox is running (PID %d)\n", pid)

	// Try to hit the API for more info
	// Read port from /tmp/downbox.port if available
	port := readPort()
	url := fmt.Sprintf("http://localhost:%d/api/status", port)

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		fmt.Printf("  WebUI: http://localhost:%d (not responding)\n", port)
		return
	}
	defer resp.Body.Close()

	fmt.Printf("  WebUI: http://localhost:%d\n", port)

	// Parse status for display
	var status map[string]interface{}
	if err := decodeJSON(resp, &status); err == nil {
		if a, ok := status["aria2"].(map[string]interface{}); ok {
			if online, ok := a["online"].(bool); ok && online {
				fmt.Println("  aria2: connected")
				if active, ok := a["active"].(float64); ok && active > 0 {
					fmt.Printf("  Active downloads: %.0f\n", active)
				}
			} else {
				fmt.Println("  aria2: disconnected")
			}
		}
		if d, ok := status["disk"].(map[string]interface{}); ok {
			if free, ok := d["free"].(float64); ok {
				fmt.Printf("  Disk free: %s\n", formatSize(int64(free)))
			}
		}
		if s, ok := status["shares"].(map[string]interface{}); ok {
			if active, ok := s["active"].(float64); ok && active > 0 {
				fmt.Printf("  Active shares: %.0f\n", active)
			}
		}
	}
}

// --- PID file management ---

func writePid() {
	os.WriteFile(pidFilePath(), []byte(strconv.Itoa(os.Getpid())), 0o644)
}

func writePort(port int) {
	os.WriteFile(portFilePath(), []byte(strconv.Itoa(port)), 0o644)
}

func readPid() (int, bool) {
	data, err := os.ReadFile(pidFilePath())
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, false
	}
	// Check if process is actually running
	if err := syscall.Kill(pid, 0); err != nil {
		os.Remove(pidFilePath())
		return 0, false
	}
	return pid, true
}

func readPort() int {
	data, err := os.ReadFile(portFilePath())
	if err != nil {
		return 8080
	}
	port, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 8080
	}
	return port
}

func cleanupPid() {
	os.Remove(pidFilePath())
	os.Remove(portFilePath())
}

// --- Server (foreground or daemon) ---

func runServer(args []string) {
	cfg := Config{}
	flagSet := flag.NewFlagSet("downbox", flag.ExitOnError)
	flagSet.IntVar(&cfg.Port, "port", 8080, "HTTP server port")
	flagSet.StringVar(&cfg.DownloadDir, "download-dir", "", "Download directory (default: ~/Downloads)")
	flagSet.IntVar(&cfg.Aria2Port, "aria2-port", 6800, "aria2 RPC port")
	flagSet.StringVar(&cfg.Aria2Secret, "aria2-secret", "", "aria2 RPC secret (auto-generated if empty)")
	flagSet.StringVar(&cfg.PublicURL, "public-url", "", "Public URL for share links (e.g. https://dl.example.com)")
	flagSet.BoolVar(&cfg.Dev, "dev", false, "Serve web/ from filesystem instead of embed")
	flagSet.Parse(args)

	// Track which flags were explicitly set
	flagsSet := make(map[string]bool)
	flagSet.Visit(func(f *flag.Flag) { flagsSet[f.Name] = true })

	// Load config file (flags take precedence)
	if configPath := loadConfig(&cfg, flagsSet); configPath != "" {
		slog.Info("loaded config", "path", configPath)
	}

	// Expand ~ in download-dir
	if cfg.DownloadDir != "" {
		cfg.DownloadDir = expandHome(cfg.DownloadDir)
	}

	// Default download dir: ~/Downloads
	if cfg.DownloadDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			slog.Error("cannot determine home directory", "error", err)
			os.Exit(1)
		}
		cfg.DownloadDir = filepath.Join(home, "Downloads")
	}

	// Resolve to absolute path
	absDir, err := filepath.Abs(cfg.DownloadDir)
	if err != nil {
		slog.Error("invalid download-dir", "error", err)
		os.Exit(1)
	}
	cfg.DownloadDir = absDir

	// Create download dir if needed
	if err := os.MkdirAll(cfg.DownloadDir, 0o755); err != nil {
		slog.Error("cannot create download-dir", "error", err)
		os.Exit(1)
	}

	// Resolve symlinks for security
	cfg.DownloadDir, err = filepath.EvalSymlinks(cfg.DownloadDir)
	if err != nil {
		slog.Error("cannot resolve download-dir", "error", err)
		os.Exit(1)
	}

	// Auto-generate aria2 secret if not provided
	if cfg.Aria2Secret == "" {
		b := make([]byte, 8)
		rand.Read(b)
		cfg.Aria2Secret = hex.EncodeToString(b)
	}

	// Auto-generate password if not set
	firstGeneration := false
	if cfg.Password == "" {
		b := make([]byte, 12)
		rand.Read(b)
		cfg.Password = hex.EncodeToString(b)
		firstGeneration = true
		slog.Info("no password configured, one has been generated")
	}

	// Auto-complete setup on first start so the wizard is never exposed without auth
	if !cfg.SetupDone {
		cfg.SetupDone = true
		firstGeneration = true
	}

	// Save config if anything was generated
	if firstGeneration {
		saveConfig(&cfg)
	}

	cfg.Aria2URL = fmt.Sprintf("http://localhost:%d/jsonrpc", cfg.Aria2Port)

	// Write PID and port files
	writePid()
	writePort(cfg.Port)
	defer cleanupPid()

	slog.Info("starting downbox",
		"version", version,
		"port", cfg.Port,
		"download-dir", cfg.DownloadDir,
	)

	// Blocklist — start before aria2 so the filtering proxy is ready
	blocklistMgr := NewBlocklistManager(&cfg)
	if err := blocklistMgr.Start(); err != nil {
		slog.Warn("blocklist failed", "error", err)
	}
	defer blocklistMgr.Stop()

	// If blocklist proxy is running, route aria2 through it
	if proxyAddr := blocklistMgr.ProxyAddr(); proxyAddr != "" {
		cfg.Proxy = "socks5://127.0.0.1:" + strings.Split(proxyAddr, ":")[1]
		slog.Info("aria2 routed through blocklist proxy", "proxy", cfg.Proxy)
	}

	// --- Start aria2 as subprocess ---
	aria2Cmd, err := startAria2(&cfg)
	if err != nil {
		slog.Error("failed to start aria2", "error", err)
		os.Exit(1)
	}
	defer stopProcess(aria2Cmd)

	// Wait a moment for aria2 to initialize
	time.Sleep(300 * time.Millisecond)

	// Init components
	aria2Client := aria2.NewClient(cfg.Aria2URL, cfg.Aria2Secret)
	fileHandler := files.NewHandler(cfg.DownloadDir)

	// Web filesystem
	var webFileSystem http.FileSystem
	if cfg.Dev {
		slog.Info("dev mode: serving web/ from filesystem")
		webFileSystem = http.Dir("web")
	} else {
		sub, err := fs.Sub(webFS, "web")
		if err != nil {
			slog.Error("cannot access embedded web files", "error", err)
			os.Exit(1)
		}
		webFileSystem = http.FS(sub)
	}

	// Tunnel manager
	tunnelMgr := NewTunnelManager(&cfg)
	if cfg.SetupDone && cfg.Tunnel != "" && cfg.Tunnel != "none" {
		if err := tunnelMgr.Start(); err != nil {
			slog.Warn("tunnel failed to start", "error", err)
		}
	}
	defer tunnelMgr.Stop()

	// HTTP server
	shareMgr := NewShareManager(cfg.DownloadDir)
	mux := NewServer(&cfg, aria2Client, fileHandler, tunnelMgr, shareMgr, webFileSystem)
	// Bind to localhost by default — Docker/LAN users can set DOWNBOX_BIND=0.0.0.0
	bindHost := "127.0.0.1"
	if env := os.Getenv("DOWNBOX_BIND"); env != "" {
		bindHost = env
	}
	bindAddr := fmt.Sprintf("%s:%d", bindHost, cfg.Port)
	srv := &http.Server{
		Addr:         bindAddr,
		Handler:      mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	fmt.Printf("\n  DownBox ready → http://localhost:%d\n", cfg.Port)
	// Only print password on first generation, and only to stderr on an interactive terminal
	if firstGeneration {
		if isTerminal(os.Stderr) {
			fmt.Fprintf(os.Stderr, "  Password:      %s\n", cfg.Password)
		}
	}
	if cfg.PublicURL != "" {
		fmt.Printf("  Public URL:    %s\n", cfg.PublicURL)
	}
	fmt.Printf("  Downloads dir: %s\n\n", cfg.DownloadDir)

	<-sigCh
	fmt.Println("\nShutting down...")

	// Drain HTTP connections
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(ctx)

	slog.Info("downbox stopped")
}

// startAria2 launches aria2c as a managed subprocess.
func startAria2(cfg *Config) (*exec.Cmd, error) {
	aria2Path, err := exec.LookPath("aria2c")
	if err != nil {
		return nil, fmt.Errorf("aria2c not found in PATH — install it: sudo apt install aria2")
	}

	args := []string{
		"--enable-rpc",
		fmt.Sprintf("--rpc-listen-port=%d", cfg.Aria2Port),
		"--rpc-listen-all=false",
		fmt.Sprintf("--rpc-secret=%s", cfg.Aria2Secret),
		fmt.Sprintf("--dir=%s", cfg.DownloadDir),
		"--continue=true",
		"--max-concurrent-downloads=5",
		"--max-connection-per-server=16",
		"--split=16",
		"--min-split-size=1M",
		"--file-allocation=falloc",
		"--auto-file-renaming=true",
		"--allow-overwrite=false",
		"--bt-enable-lpd=true",
		"--dht-listen-port=6881-6999",
		"--listen-port=6881-6999",
		"--seed-ratio=0",
		"--follow-torrent=true",
		"--check-certificate=true",
		"--console-log-level=warn",
	}

	// Custom DNS servers
	if cfg.DNSServers != "" {
		args = append(args, fmt.Sprintf("--async-dns-server=%s", cfg.DNSServers))
		slog.Info("aria2 using custom DNS", "servers", cfg.DNSServers)
	}

	// Bind to specific network interface (VPN, etc.)
	if cfg.Interface != "" {
		args = append(args, fmt.Sprintf("--interface=%s", cfg.Interface))
		slog.Info("aria2 bound to interface", "interface", cfg.Interface)
	}

	// Exclude BitTorrent trackers
	if cfg.ExcludeTrackers != "" {
		args = append(args, fmt.Sprintf("--bt-exclude-tracker=%s", cfg.ExcludeTrackers))
		slog.Info("aria2 excluding trackers", "trackers", cfg.ExcludeTrackers)
	}

	// Proxy (SOCKS5/HTTP)
	if cfg.Proxy != "" {
		args = append(args, fmt.Sprintf("--all-proxy=%s", cfg.Proxy))
		slog.Info("aria2 using proxy", "proxy", cfg.Proxy)
	}

	cmd := exec.Command(aria2Path, args...)

	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid:   true,
		Pdeathsig: syscall.SIGTERM,
	}

	cmd.Stdout = &aria2Logger{level: "info"}
	cmd.Stderr = &aria2Logger{level: "warn"}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start aria2c: %w", err)
	}

	slog.Info("aria2 started", "pid", cmd.Process.Pid, "path", aria2Path)

	go func() {
		err := cmd.Wait()
		if err != nil {
			slog.Warn("aria2 exited", "error", err)
		}
	}()

	return cmd, nil
}

// stopProcess gracefully stops a subprocess.
func stopProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

type aria2Logger struct {
	level string
}

func (l *aria2Logger) Write(p []byte) (n int, err error) {
	msg := string(p)
	if len(msg) > 0 && msg[len(msg)-1] == '\n' {
		msg = msg[:len(msg)-1]
	}
	if msg == "" {
		return len(p), nil
	}
	if l.level == "warn" {
		slog.Warn("aria2: " + msg)
	} else {
		slog.Debug("aria2: " + msg)
	}
	return len(p), nil
}

// --- Helpers ---

func formatSize(bytes int64) string {
	if bytes == 0 {
		return "0 B"
	}
	units := []string{"B", "KB", "MB", "GB", "TB"}
	i := 0
	size := float64(bytes)
	for size >= 1024 && i < len(units)-1 {
		size /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%.0f %s", size, units[i])
	}
	return fmt.Sprintf("%.1f %s", size, units[i])
}

func decodeJSON(resp *http.Response, v interface{}) error {
	return json.NewDecoder(resp.Body).Decode(v)
}

// isTerminal checks if f is connected to a terminal (not redirected/piped).
func isTerminal(f *os.File) bool {
	var termios [256]byte // oversized buffer for struct termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(0x5401), uintptr(unsafe.Pointer(&termios[0]))) // 0x5401 = TCGETS
	return errno == 0
}
