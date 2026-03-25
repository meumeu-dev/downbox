package main

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

var boreURLRegex = regexp.MustCompile(`listening at ([^\s]+:\d+)`)

type TunnelManager struct {
	cfg    Config
	cmd    *exec.Cmd
	url    string
	status string // stopped, starting, running, error
	errMsg string
	mu     sync.Mutex
	cancel context.CancelFunc
}

func NewTunnelManager(cfg Config) *TunnelManager {
	return &TunnelManager{cfg: cfg, status: "stopped"}
}

func (tm *TunnelManager) Start() error {
	tm.mu.Lock()
	if tm.status == "running" || tm.status == "starting" {
		tm.mu.Unlock()
		return nil
	}
	tm.status = "starting"
	tm.errMsg = ""
	tm.mu.Unlock()

	switch tm.cfg.Tunnel {
	case "cloudflared":
		return tm.startCloudflared()
	case "bore":
		return tm.startBore()
	case "none", "":
		return nil
	default:
		return fmt.Errorf("unknown tunnel type: %s", tm.cfg.Tunnel)
	}
}

func (tm *TunnelManager) Stop() {
	tm.mu.Lock()
	cancel := tm.cancel
	cmd := tm.cmd
	tm.status = "stopped"
	tm.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if cmd != nil && cmd.Process != nil {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		done := make(chan struct{})
		go func() { cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}
}

func (tm *TunnelManager) Status() (status, url, errMsg string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return tm.status, tm.url, tm.errMsg
}

func (tm *TunnelManager) setRunning(url string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.status = "running"
	tm.url = url
}

func (tm *TunnelManager) setError(msg string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.status = "error"
	tm.errMsg = msg
}

func (tm *TunnelManager) startCloudflared() error {
	if _, err := exec.LookPath("cloudflared"); err != nil {
		return fmt.Errorf("cloudflared not installed")
	}
	if tm.cfg.CloudflaredToken == "" {
		return fmt.Errorf("cloudflared-token not set")
	}

	ctx, cancel := context.WithCancel(context.Background())
	tm.mu.Lock()
	tm.cancel = cancel
	tm.mu.Unlock()

	cmd := exec.CommandContext(ctx, "cloudflared", "tunnel", "run", "--token", tm.cfg.CloudflaredToken)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("start cloudflared: %w", err)
	}

	tm.mu.Lock()
	tm.cmd = cmd
	tm.mu.Unlock()

	// Watch for connection established
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			slog.Debug("cloudflared", "line", line)
			if strings.Contains(line, "Registered tunnel connection") {
				url := fmt.Sprintf("https://%s", tm.cfg.CloudflaredHostname)
				tm.setRunning(url)
				slog.Info("cloudflared tunnel running", "url", url)
			}
		}
	}()

	// Monitor exit
	go func() {
		err := cmd.Wait()
		tm.mu.Lock()
		if tm.status != "stopped" {
			tm.status = "error"
			if err != nil {
				tm.errMsg = err.Error()
			} else {
				tm.errMsg = "process exited"
			}
		}
		tm.mu.Unlock()
	}()

	// Timeout for connection
	go func() {
		time.Sleep(30 * time.Second)
		tm.mu.Lock()
		if tm.status == "starting" {
			tm.status = "error"
			tm.errMsg = "timeout connecting to Cloudflare"
		}
		tm.mu.Unlock()
	}()

	return nil
}

func (tm *TunnelManager) startBore() error {
	if _, err := exec.LookPath("bore"); err != nil {
		return fmt.Errorf("bore not installed")
	}

	ctx, cancel := context.WithCancel(context.Background())
	tm.mu.Lock()
	tm.cancel = cancel
	tm.mu.Unlock()

	port := tm.cfg.Port
	if port == 0 {
		port = 8080
	}
	slog.Info("starting bore", "port", port)

	boreServer := tm.cfg.BoreServer
	if boreServer == "" {
		boreServer = "bore.pub"
	}

	args := []string{"local", fmt.Sprintf("%d", port), "--to", boreServer}
	if tm.cfg.BoreSecret != "" {
		args = append(args, "--secret", tm.cfg.BoreSecret)
	}

	cmd := exec.CommandContext(ctx, "bore", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return err
	}
	stdout, _ := cmd.StdoutPipe()

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("start bore: %w", err)
	}

	tm.mu.Lock()
	tm.cmd = cmd
	tm.mu.Unlock()

	parseLine := func(line string) {
		slog.Debug("bore", "line", line)
		if match := boreURLRegex.FindStringSubmatch(line); len(match) > 1 {
			url := fmt.Sprintf("http://%s", match[1])
			tm.setRunning(url)
			slog.Info("bore tunnel running", "url", url)
		}
	}

	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			parseLine(scanner.Text())
		}
	}()
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			parseLine(scanner.Text())
		}
	}()

	go func() {
		err := cmd.Wait()
		tm.mu.Lock()
		if tm.status != "stopped" {
			tm.status = "error"
			if err != nil {
				tm.errMsg = err.Error()
			}
		}
		tm.mu.Unlock()
	}()

	return nil
}

// AvailableTunnels returns which tunnel tools are installed.
func AvailableTunnels() map[string]bool {
	tools := map[string]bool{"cloudflared": false, "bore": false}
	for name := range tools {
		if _, err := exec.LookPath(name); err == nil {
			tools[name] = true
		}
	}
	return tools
}
