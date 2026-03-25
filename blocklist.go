package main

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// BlocklistManager handles IP blocklist loading and application
type BlocklistManager struct {
	cfg       Config
	cacheFile string
	entries   int
}

func NewBlocklistManager(cfg Config) *BlocklistManager {
	home, _ := os.UserHomeDir()
	return &BlocklistManager{
		cfg:       cfg,
		cacheFile: filepath.Join(home, ".config", "downbox", "blocklist.dat"),
	}
}

// Start downloads the blocklist and applies it based on the configured mode
func (bm *BlocklistManager) Start() error {
	if bm.cfg.BlocklistURL == "" || bm.cfg.BlocklistMode == "" || bm.cfg.BlocklistMode == "none" {
		return nil
	}

	// Download blocklist if needed (refresh every 24h)
	if err := bm.downloadIfNeeded(); err != nil {
		return fmt.Errorf("download blocklist: %w", err)
	}

	switch bm.cfg.BlocklistMode {
	case "iptables":
		return bm.applyIptables()
	case "proxy":
		slog.Info("blocklist mode: proxy — configure your SOCKS proxy to enforce the blocklist")
		return nil
	default:
		return fmt.Errorf("unknown blocklist mode: %s", bm.cfg.BlocklistMode)
	}
}

// Stop removes iptables rules
func (bm *BlocklistManager) Stop() {
	if bm.cfg.BlocklistMode == "iptables" {
		bm.removeIptables()
	}
}

// downloadIfNeeded fetches the blocklist URL if cache is missing or older than 24h
func (bm *BlocklistManager) downloadIfNeeded() error {
	if info, err := os.Stat(bm.cacheFile); err == nil {
		if time.Since(info.ModTime()) < 24*time.Hour {
			slog.Info("blocklist cache is fresh", "file", bm.cacheFile)
			return bm.countEntries()
		}
	}

	slog.Info("downloading blocklist", "url", bm.cfg.BlocklistURL)
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(bm.cfg.BlocklistURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("blocklist download failed: HTTP %d", resp.StatusCode)
	}

	dir := filepath.Dir(bm.cacheFile)
	os.MkdirAll(dir, 0o700)

	f, err := os.OpenFile(bm.cacheFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()

	// Limit to 100MB
	if _, err := io.Copy(f, io.LimitReader(resp.Body, 100<<20)); err != nil {
		return err
	}

	slog.Info("blocklist downloaded", "file", bm.cacheFile)
	return bm.countEntries()
}

func (bm *BlocklistManager) countEntries() error {
	f, err := os.Open(bm.cacheFile)
	if err != nil {
		return err
	}
	defer f.Close()

	count := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			count++
		}
	}
	bm.entries = count
	slog.Info("blocklist loaded", "entries", count)
	return nil
}

// applyIptables creates an ipset and iptables rules to block listed IPs
func (bm *BlocklistManager) applyIptables() error {
	// Check if ipset is available
	if _, err := exec.LookPath("ipset"); err != nil {
		return fmt.Errorf("ipset not installed (apt install ipset)")
	}

	slog.Info("applying blocklist via iptables/ipset")

	// Create ipset (destroy first if exists)
	exec.Command("ipset", "destroy", "downbox-block").Run()
	if out, err := exec.Command("ipset", "create", "downbox-block", "hash:net", "maxelem", "1000000").CombinedOutput(); err != nil {
		return fmt.Errorf("ipset create: %s", string(out))
	}

	// Parse blocklist and add entries
	f, err := os.Open(bm.cacheFile)
	if err != nil {
		return err
	}
	defer f.Close()

	added := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Support multiple formats:
		// - Plain IP: 1.2.3.4
		// - CIDR: 1.2.3.0/24
		// - ipfilter.dat: 001.002.003.004 - 001.002.003.255 , 100 , description
		// - P2P format: name:1.2.3.4-1.2.3.255
		var cidr string
		if strings.Contains(line, "-") && strings.Contains(line, ",") {
			// ipfilter.dat format
			parts := strings.SplitN(line, "-", 2)
			ip := strings.TrimSpace(parts[0])
			cidr = ip + "/32"
		} else if strings.Contains(line, ":") && strings.Contains(line, "-") {
			// P2P format: name:start-end
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				rangePart := strings.SplitN(parts[1], "-", 2)
				cidr = strings.TrimSpace(rangePart[0]) + "/32"
			}
		} else if strings.Contains(line, "/") || !strings.Contains(line, " ") {
			// CIDR or plain IP
			cidr = line
			if !strings.Contains(cidr, "/") {
				cidr += "/32"
			}
		} else {
			continue
		}

		if cidr != "" {
			exec.Command("ipset", "add", "downbox-block", cidr, "-exist").Run()
			added++
		}
	}

	// Add iptables rule (if not already present)
	checkCmd := exec.Command("iptables", "-C", "OUTPUT", "-m", "set", "--match-set", "downbox-block", "dst", "-j", "DROP")
	if checkCmd.Run() != nil {
		if out, err := exec.Command("iptables", "-I", "OUTPUT", "-m", "set", "--match-set", "downbox-block", "dst", "-j", "DROP").CombinedOutput(); err != nil {
			return fmt.Errorf("iptables: %s", string(out))
		}
	}

	slog.Info("blocklist applied via iptables", "rules", added)
	return nil
}

// removeIptables cleans up iptables rules and ipset
func (bm *BlocklistManager) removeIptables() {
	exec.Command("iptables", "-D", "OUTPUT", "-m", "set", "--match-set", "downbox-block", "dst", "-j", "DROP").Run()
	exec.Command("ipset", "destroy", "downbox-block").Run()
	slog.Info("blocklist iptables rules removed")
}

// Status returns blocklist info
func (bm *BlocklistManager) Status() map[string]interface{} {
	return map[string]interface{}{
		"mode":    bm.cfg.BlocklistMode,
		"url":     bm.cfg.BlocklistURL,
		"entries": bm.entries,
		"active":  bm.cfg.BlocklistMode != "" && bm.cfg.BlocklistMode != "none",
	}
}
