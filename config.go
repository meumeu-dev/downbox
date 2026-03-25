package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// configPaths returns the list of paths to search for config files, in order.
func configPaths() []string {
	paths := []string{
		"./downbox.conf",
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		paths = append(paths, filepath.Join(xdg, "downbox", "downbox.conf"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".config", "downbox", "downbox.conf"))
	}
	paths = append(paths, "/etc/downbox/downbox.conf")
	return paths
}

// defaultConfigPath returns the preferred config file location.
func defaultConfigPath() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", "downbox", "downbox.conf")
	}
	return "./downbox.conf"
}

// configExists checks if a config with setup: true exists.
func configExists() bool {
	for _, path := range configPaths() {
		values, err := parseConfigFile(path)
		if err != nil {
			continue
		}
		if v, ok := values["setup"]; ok && v == "true" {
			return true
		}
	}
	return false
}

// loadConfig reads the first config file found and applies values to cfg.
func loadConfig(cfg *Config, flagsSet map[string]bool) string {
	for _, path := range configPaths() {
		values, err := parseConfigFile(path)
		if err != nil {
			continue
		}

		if v, ok := values["port"]; ok && !flagsSet["port"] {
			if n, err := strconv.Atoi(v); err == nil {
				cfg.Port = n
			}
		}
		if v, ok := values["download-dir"]; ok && !flagsSet["download-dir"] {
			cfg.DownloadDir = v
		}
		if v, ok := values["aria2-port"]; ok && !flagsSet["aria2-port"] {
			if n, err := strconv.Atoi(v); err == nil {
				cfg.Aria2Port = n
			}
		}
		if v, ok := values["aria2-secret"]; ok && !flagsSet["aria2-secret"] {
			cfg.Aria2Secret = v
		}
		if v, ok := values["public-url"]; ok && !flagsSet["public-url"] {
			cfg.PublicURL = v
		}
		if v, ok := values["tunnel"]; ok {
			cfg.Tunnel = v
		}
		if v, ok := values["cloudflared-token"]; ok {
			cfg.CloudflaredToken = v
		}
		if v, ok := values["cloudflared-hostname"]; ok {
			cfg.CloudflaredHostname = v
		}
		if v, ok := values["bore-server"]; ok {
			cfg.BoreServer = v
		}
		if v, ok := values["bore-secret"]; ok {
			cfg.BoreSecret = v
		}
		if v, ok := values["password"]; ok {
			cfg.Password = v
		}
		if v, ok := values["dns-servers"]; ok {
			cfg.DNSServers = v
		}
		if v, ok := values["interface"]; ok {
			cfg.Interface = v
		}
		if v, ok := values["exclude-trackers"]; ok {
			cfg.ExcludeTrackers = v
		}
		if v, ok := values["proxy"]; ok {
			cfg.Proxy = v
		}
		if v, ok := values["blocklist-url"]; ok {
			cfg.BlocklistURL = v
		}
		if v, ok := values["setup"]; ok && v == "true" {
			cfg.SetupDone = true
		}

		return path
	}
	return ""
}

// saveConfig writes the config to the default config path.
func saveConfig(cfg *Config) error {
	path := defaultConfigPath()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	dlDir := cfg.DownloadDir
	// Convert absolute home path back to ~ for portability
	if home, err := os.UserHomeDir(); err == nil {
		if strings.HasPrefix(dlDir, home) {
			dlDir = "~" + dlDir[len(home):]
		}
	}

	var b strings.Builder
	b.WriteString("# DownBox configuration\n\n")
	b.WriteString(fmt.Sprintf("setup: %v\n\n", cfg.SetupDone))
	b.WriteString(fmt.Sprintf("port: %d\n", cfg.Port))
	b.WriteString(fmt.Sprintf("download-dir: %s\n", dlDir))
	b.WriteString(fmt.Sprintf("aria2-port: %d\n", cfg.Aria2Port))
	if cfg.Aria2Secret != "" {
		b.WriteString(fmt.Sprintf("aria2-secret: %s\n", cfg.Aria2Secret))
	}
	b.WriteString(fmt.Sprintf("\ntunnel: %s\n", cfg.Tunnel))
	if cfg.CloudflaredToken != "" {
		b.WriteString(fmt.Sprintf("cloudflared-token: %s\n", cfg.CloudflaredToken))
	}
	if cfg.CloudflaredHostname != "" {
		b.WriteString(fmt.Sprintf("cloudflared-hostname: %s\n", cfg.CloudflaredHostname))
	}
	if cfg.BoreServer != "" {
		b.WriteString(fmt.Sprintf("bore-server: %s\n", cfg.BoreServer))
	}
	if cfg.BoreSecret != "" {
		b.WriteString(fmt.Sprintf("bore-secret: %s\n", cfg.BoreSecret))
	}
	if cfg.Password != "" {
		b.WriteString(fmt.Sprintf("password: %s\n", cfg.Password))
	}
	if cfg.DNSServers != "" {
		b.WriteString(fmt.Sprintf("dns-servers: %s\n", cfg.DNSServers))
	}
	if cfg.Interface != "" {
		b.WriteString(fmt.Sprintf("interface: %s\n", cfg.Interface))
	}
	if cfg.ExcludeTrackers != "" {
		b.WriteString(fmt.Sprintf("exclude-trackers: %s\n", cfg.ExcludeTrackers))
	}
	if cfg.Proxy != "" {
		b.WriteString(fmt.Sprintf("proxy: %s\n", cfg.Proxy))
	}
	if cfg.BlocklistURL != "" {
		b.WriteString(fmt.Sprintf("blocklist-url: %s\n", cfg.BlocklistURL))
	}
	if cfg.PublicURL != "" {
		b.WriteString(fmt.Sprintf("public-url: %s\n", cfg.PublicURL))
	}

	return os.WriteFile(path, []byte(b.String()), 0o600)
}

// parseConfigFile reads a simple key: value config file.
func parseConfigFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	values := make(map[string]string)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var key, val string
		if idx := strings.Index(line, ":"); idx > 0 {
			key = strings.TrimSpace(line[:idx])
			val = strings.TrimSpace(line[idx+1:])
		} else if idx := strings.Index(line, "="); idx > 0 {
			key = strings.TrimSpace(line[:idx])
			val = strings.TrimSpace(line[idx+1:])
		} else {
			continue
		}
		val = strings.Trim(val, `"'`)
		values[key] = val
	}
	return values, scanner.Err()
}

// generateDefaultConfig writes a default config file.
func generateDefaultConfig(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	content := `# DownBox configuration
# Run 'downbox start' then open http://localhost:8080 to configure

setup: false

port: 8080
download-dir: ~/Downloads
aria2-port: 6800
tunnel: none
`
	return os.WriteFile(path, []byte(content), 0o600)
}

// expandHome replaces ~ with the user's home directory.
func expandHome(path string) string {
	if !strings.HasPrefix(path, "~") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[1:])
}
