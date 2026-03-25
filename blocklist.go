package main

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// BlocklistManager handles IP blocklist + built-in SOCKS5 filtering proxy
type BlocklistManager struct {
	cfg       Config
	cacheFile string
	networks  []*net.IPNet
	listener  net.Listener
	proxyAddr string // "127.0.0.1:PORT" when running
	mu        sync.RWMutex
}

func NewBlocklistManager(cfg Config) *BlocklistManager {
	home, _ := os.UserHomeDir()
	return &BlocklistManager{
		cfg:       cfg,
		cacheFile: filepath.Join(home, ".config", "downbox", "blocklist.dat"),
	}
}

// Start downloads all blocklists and starts the filtering SOCKS5 proxy
func (bm *BlocklistManager) Start() error {
	if bm.cfg.BlocklistURL == "" {
		return nil
	}

	urls := strings.Split(bm.cfg.BlocklistURL, ",")
	for i, u := range urls {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		// Validate blocklist URL against SSRF before fetching
		if err := validateDownloadURL(u); err != nil {
			slog.Warn("blocklist URL blocked by SSRF validation", "url", u, "error", err)
			continue
		}
		cacheFile := bm.cacheFile
		if i > 0 {
			cacheFile = fmt.Sprintf("%s.%d", bm.cacheFile, i)
		}
		if err := downloadIfNeeded(u, cacheFile); err != nil {
			slog.Warn("blocklist download failed", "url", u, "error", err)
			continue
		}
		if err := bm.loadFromFile(cacheFile); err != nil {
			slog.Warn("blocklist parse failed", "file", cacheFile, "error", err)
		}
	}

	if len(bm.networks) == 0 {
		return fmt.Errorf("no blocklist entries loaded")
	}

	slog.Info("blocklist total entries", "count", len(bm.networks))
	return bm.startProxy()
}

// Stop shuts down the SOCKS5 proxy
func (bm *BlocklistManager) Stop() {
	if bm.listener != nil {
		bm.listener.Close()
	}
}

// ProxyAddr returns the local SOCKS5 proxy address, empty if not running
func (bm *BlocklistManager) ProxyAddr() string {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	return bm.proxyAddr
}

// Status returns blocklist info
func (bm *BlocklistManager) Status() map[string]interface{} {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	return map[string]interface{}{
		"url":       bm.cfg.BlocklistURL,
		"entries":   len(bm.networks),
		"active":    bm.proxyAddr != "",
		"proxyAddr": bm.proxyAddr,
	}
}

// isBlocked checks if an IP is in the blocklist
func (bm *BlocklistManager) isBlocked(ip net.IP) bool {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	for _, n := range bm.networks {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// --- Blocklist download & parsing ---

func downloadIfNeeded(url, cacheFile string) error {
	if info, err := os.Stat(cacheFile); err == nil {
		if time.Since(info.ModTime()) < 24*time.Hour {
			slog.Info("blocklist cache is fresh", "file", cacheFile)
			return nil
		}
	}

	slog.Info("downloading blocklist", "url", url)
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	dir := filepath.Dir(cacheFile)
	os.MkdirAll(dir, 0o700)

	f, err := os.OpenFile(cacheFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := io.Copy(f, io.LimitReader(resp.Body, 100<<20)); err != nil {
		return err
	}

	slog.Info("blocklist downloaded", "file", cacheFile)
	return nil
}

func (bm *BlocklistManager) loadFromFile(cacheFile string) error {
	f, err := os.Open(cacheFile)
	if err != nil {
		return err
	}
	defer f.Close()

	var networks []*net.IPNet
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		nets := parseLine(line)
		networks = append(networks, nets...)
	}

	bm.mu.Lock()
	bm.networks = append(bm.networks, networks...)
	bm.mu.Unlock()

	slog.Info("blocklist loaded", "file", cacheFile, "entries", len(networks))
	return nil
}

// parseLine handles multiple blocklist formats
func parseLine(line string) []*net.IPNet {
	// ipfilter.dat: 001.002.003.004 - 001.002.003.255 , 100 , description
	if strings.Contains(line, ",") && strings.Contains(line, "-") {
		parts := strings.SplitN(line, ",", 3)
		if len(parts) >= 2 {
			rangeParts := strings.SplitN(parts[0], "-", 2)
			if len(rangeParts) == 2 {
				return ipRangeToNets(strings.TrimSpace(rangeParts[0]), strings.TrimSpace(rangeParts[1]))
			}
		}
		return nil
	}

	// P2P format: name:1.2.3.4-1.2.3.255
	if idx := strings.LastIndex(line, ":"); idx > 0 && strings.Contains(line[idx:], "-") {
		rangePart := line[idx+1:]
		parts := strings.SplitN(rangePart, "-", 2)
		if len(parts) == 2 {
			return ipRangeToNets(strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]))
		}
		return nil
	}

	// CIDR: 1.2.3.0/24
	if strings.Contains(line, "/") {
		_, n, err := net.ParseCIDR(line)
		if err == nil {
			return []*net.IPNet{n}
		}
		return nil
	}

	// Plain IP: 1.2.3.4
	ip := net.ParseIP(strings.TrimSpace(line))
	if ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			return []*net.IPNet{{IP: ip4, Mask: net.CIDRMask(32, 32)}}
		}
		return []*net.IPNet{{IP: ip, Mask: net.CIDRMask(128, 128)}}
	}

	return nil
}

// ipRangeToNets converts a start-end IP range to the smallest set of CIDRs
func ipRangeToNets(startStr, endStr string) []*net.IPNet {
	start := net.ParseIP(startStr)
	end := net.ParseIP(endStr)
	if start == nil || end == nil {
		return nil
	}
	start = start.To4()
	end = end.To4()
	if start == nil || end == nil {
		return nil
	}

	// Simple approach: use /24 for ranges, /32 for singles
	s := binary.BigEndian.Uint32(start)
	e := binary.BigEndian.Uint32(end)
	if s > e {
		return nil
	}

	var nets []*net.IPNet
	for s <= e {
		// Find the largest block that fits
		bits := 32
		for bits > 0 {
			mask := uint32(1<<(32-bits+1)) - 1
			if (s & mask) != 0 {
				break
			}
			if s+(1<<(32-bits+1))-1 > e {
				break
			}
			bits--
		}
		ip := make(net.IP, 4)
		binary.BigEndian.PutUint32(ip, s)
		nets = append(nets, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, 32)})
		s += 1 << (32 - bits)
		if s == 0 { // overflow
			break
		}
	}
	return nets
}

// --- Built-in SOCKS5 filtering proxy ---

// maxSOCKS5Conns limits concurrent SOCKS5 proxy connections to prevent FD exhaustion.
const maxSOCKS5Conns = 500

func (bm *BlocklistManager) startProxy() error {
	addr := "127.0.0.1:0"
	if bm.cfg.BlocklistPort > 0 {
		addr = fmt.Sprintf("127.0.0.1:%d", bm.cfg.BlocklistPort)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	bm.mu.Lock()
	bm.listener = ln
	bm.proxyAddr = ln.Addr().String()
	bm.mu.Unlock()

	slog.Info("blocklist SOCKS5 proxy started", "addr", bm.proxyAddr, "rules", len(bm.networks))

	// Semaphore to limit concurrent connections
	connSem := make(chan struct{}, maxSOCKS5Conns)

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // listener closed
			}
			select {
			case connSem <- struct{}{}:
				go func() {
					defer func() { <-connSem }()
					bm.handleSOCKS5(conn)
				}()
			default:
				// At max connections, reject
				conn.Close()
			}
		}
	}()

	return nil
}

func (bm *BlocklistManager) handleSOCKS5(conn net.Conn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(30 * time.Second))

	// 1. Greeting
	buf := make([]byte, 258)
	if _, err := io.ReadFull(conn, buf[:2]); err != nil {
		return
	}
	if buf[0] != 0x05 {
		return // not SOCKS5
	}
	nmethods := int(buf[1])
	if _, err := io.ReadFull(conn, buf[:nmethods]); err != nil {
		return
	}
	// Reply: no auth required
	conn.Write([]byte{0x05, 0x00})

	// 2. Request
	if _, err := io.ReadFull(conn, buf[:4]); err != nil {
		return
	}
	if buf[0] != 0x05 || buf[1] != 0x01 {
		conn.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return // only CONNECT supported
	}

	atype := buf[3]
	var host string
	switch atype {
	case 0x01: // IPv4
		if _, err := io.ReadFull(conn, buf[:4]); err != nil {
			return
		}
		host = net.IP(buf[:4]).String()
	case 0x03: // Domain
		if _, err := io.ReadFull(conn, buf[:1]); err != nil {
			return
		}
		domLen := int(buf[0])
		if _, err := io.ReadFull(conn, buf[:domLen]); err != nil {
			return
		}
		host = string(buf[:domLen])
	case 0x04: // IPv6
		if _, err := io.ReadFull(conn, buf[:16]); err != nil {
			return
		}
		host = net.IP(buf[:16]).String()
	default:
		conn.Write([]byte{0x05, 0x08, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	// Read port
	if _, err := io.ReadFull(conn, buf[:2]); err != nil {
		return
	}
	port := binary.BigEndian.Uint16(buf[:2])

	// 3. Resolve and check blocklist
	ips, err := net.LookupHost(host)
	if err != nil {
		// Connection refused reply
		conn.Write([]byte{0x05, 0x04, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if bm.isBlocked(ip) {
			slog.Debug("blocklist: blocked connection", "host", host, "ip", ipStr)
			conn.Write([]byte{0x05, 0x02, 0x00, 0x01, 0, 0, 0, 0, 0, 0}) // connection not allowed
			return
		}
	}

	// 4. Connect to target
	target := fmt.Sprintf("%s:%d", host, port)

	var upstream net.Conn
	if bm.cfg.Proxy != "" {
		// Chain through user's proxy
		upstream, err = dialViaProxy(bm.cfg.Proxy, target)
	} else {
		upstream, err = net.DialTimeout("tcp", target, 10*time.Second)
	}
	if err != nil {
		conn.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer upstream.Close()

	// 5. Success reply
	conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})

	// 6. Pipe data
	conn.SetDeadline(time.Time{}) // remove deadline for data transfer
	done := make(chan struct{}, 2)
	go func() { io.Copy(upstream, conn); done <- struct{}{} }()
	go func() { io.Copy(conn, upstream); done <- struct{}{} }()
	<-done
}

// dialViaProxy connects through a SOCKS5 or HTTP proxy
func dialViaProxy(proxyURL, target string) (net.Conn, error) {
	// Parse proxy URL
	proxy := proxyURL
	proxy = strings.TrimPrefix(proxy, "socks5://")
	proxy = strings.TrimPrefix(proxy, "socks5h://")
	proxy = strings.TrimPrefix(proxy, "http://")

	conn, err := net.DialTimeout("tcp", proxy, 10*time.Second)
	if err != nil {
		return nil, err
	}

	// SOCKS5 handshake to upstream proxy
	host, portStr, _ := net.SplitHostPort(target)
	port := 0
	fmt.Sscanf(portStr, "%d", &port)

	// Greeting: no auth
	conn.Write([]byte{0x05, 0x01, 0x00})
	buf := make([]byte, 2)
	if _, err := io.ReadFull(conn, buf); err != nil {
		conn.Close()
		return nil, err
	}

	// Connect request
	req := []byte{0x05, 0x01, 0x00, 0x03, byte(len(host))}
	req = append(req, []byte(host)...)
	req = append(req, byte(port>>8), byte(port&0xff))
	conn.Write(req)

	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply[:4]); err != nil {
		conn.Close()
		return nil, err
	}
	if reply[1] != 0x00 {
		conn.Close()
		return nil, fmt.Errorf("proxy connect failed: 0x%02x", reply[1])
	}
	// Read remaining reply based on address type
	switch reply[3] {
	case 0x01:
		io.ReadFull(conn, reply[:6]) // IPv4 + port
	case 0x04:
		io.ReadFull(conn, make([]byte, 18)) // IPv6 + port
	case 0x03:
		io.ReadFull(conn, reply[:1])
		io.ReadFull(conn, make([]byte, int(reply[0])+2))
	}

	return conn, nil
}
