// Supervisor keeps a whisper-server child process warm, so a transcription
// does not pay model load time on every utterance.
package speech

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// SupervisorConfig configures child server process lifecycle and readiness probing.
type SupervisorConfig struct {
	// BinaryPath is the server binary path or name. If empty, Supervisor checks
	// "whisper-server" and "whisper-cpp-server" in PATH.
	BinaryPath string

	// ModelPath is the absolute path to the model binary.
	ModelPath string

	// ServerSocket is the Unix domain socket path or HTTP URL/address.
	ServerSocket string

	// GPULayers is the number of model layers offloaded to GPU (-ngl).
	GPULayers int

	// Threads is the number of CPU threads (-t).
	Threads int

	// Device specifies the compute device ("auto", "vulkan", "rocm", "cpu").
	Device string

	// CommandFunc allows injecting a custom command generator (useful for unit tests).
	CommandFunc func(ctx context.Context, cfg SupervisorConfig) *exec.Cmd

	// ReadyTimeout is the maximum duration to wait for the server to accept connections.
	// Defaults to 10 seconds.
	ReadyTimeout time.Duration

	// PollInterval is the polling interval for readiness checks.
	// Defaults to 50 milliseconds.
	PollInterval time.Duration

	// Logger is the structured logger.
	Logger *slog.Logger
}

// Supervisor manages the lifecycle of a child server process (such as whisper-server).
type Supervisor struct {
	cfg    SupervisorConfig
	cmd    *exec.Cmd
	done   chan struct{}
	mu     sync.Mutex
	logger *slog.Logger
}

// NewSupervisor creates a Supervisor with the given configuration.
func NewSupervisor(cfg SupervisorConfig) *Supervisor {
	if cfg.ReadyTimeout <= 0 {
		cfg.ReadyTimeout = 10 * time.Second
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 50 * time.Millisecond
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Supervisor{
		cfg:    cfg,
		logger: log,
	}
}

// DefaultServerCommand constructs the exec.Cmd to start the child server.
func DefaultServerCommand(ctx context.Context, cfg SupervisorConfig) *exec.Cmd {
	binary := cfg.BinaryPath
	if binary == "" {
		if path, err := exec.LookPath("whisper-server"); err == nil {
			binary = path
		} else if path, err := exec.LookPath("whisper-cpp-server"); err == nil {
			binary = path
		} else {
			binary = "whisper-server"
		}
	}

	args := []string{}
	if cfg.ModelPath != "" {
		args = append(args, "-m", cfg.ModelPath)
	}
	if cfg.Threads > 0 {
		args = append(args, "-t", fmt.Sprint(cfg.Threads))
	}
	if cfg.GPULayers > 0 {
		args = append(args, "-ngl", fmt.Sprint(cfg.GPULayers))
	}

	isUnix, socketPath := IsUnixSocket(cfg.ServerSocket)
	if isUnix {
		args = append(args, "--socket", socketPath)
	} else if cfg.ServerSocket != "" {
		target := cfg.ServerSocket
		var host, port string
		if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
			if u, err := url.Parse(target); err == nil {
				host = u.Hostname()
				port = u.Port()
			}
		} else if strings.Contains(target, ":") {
			h, p, err := net.SplitHostPort(target)
			if err == nil {
				host, port = h, p
			}
		}
		if host != "" {
			args = append(args, "--host", host)
		}
		if port != "" {
			args = append(args, "--port", port)
		}
	}

	return exec.CommandContext(ctx, binary, args...)
}

// Start launches the child server process and blocks until it is ready to accept connections.
func (s *Supervisor) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.isRunningLocked() {
		return nil
	}

	isUnix, socketPath := IsUnixSocket(s.cfg.ServerSocket)
	if isUnix {
		_ = os.Remove(socketPath)
		if err := os.MkdirAll(filepath.Dir(socketPath), 0o755); err != nil {
			return fmt.Errorf("speech: supervisor: create socket dir: %w", err)
		}
	}

	var cmd *exec.Cmd
	if s.cfg.CommandFunc != nil {
		cmd = s.cfg.CommandFunc(ctx, s.cfg)
	} else {
		cmd = DefaultServerCommand(ctx, s.cfg)
	}

	s.logger.Info("speech: supervisor launching child server",
		"binary", cmd.Path,
		"argv", cmd.Args,
		"socket", s.cfg.ServerSocket,
		"model", s.cfg.ModelPath,
	)

	var stderrBuf bytes.Buffer
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderrBuf)
	cmd.Stdout = os.Stdout

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("speech: supervisor: start child server %s: %w", cmd.Path, err)
	}

	s.cmd = cmd
	s.done = make(chan struct{})
	doneCh := s.done

	go func(c *exec.Cmd, d chan struct{}) {
		err := c.Wait()
		close(d)
		s.mu.Lock()
		if s.cmd == c {
			s.cmd = nil
		}
		s.mu.Unlock()
		if err != nil {
			s.logger.Warn("speech: child server process exited", "err", err)
		} else {
			s.logger.Info("speech: child server process exited cleanly")
		}
	}(cmd, doneCh)

	if err := s.waitForReady(ctx, doneCh, &stderrBuf); err != nil {
		_ = s.stopLocked()
		return fmt.Errorf("speech: supervisor: server readiness check failed: %w", err)
	}

	s.logger.Info("speech: child server ready", "pid", cmd.Process.Pid)
	return nil
}

func (s *Supervisor) waitForReady(ctx context.Context, doneCh chan struct{}, stderr *bytes.Buffer) error {
	timeout := s.cfg.ReadyTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	ticker := time.NewTicker(s.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			errMsg := strings.TrimSpace(stderr.String())
			if errMsg != "" {
				return fmt.Errorf("timed out after %v waiting for server readiness (stderr: %s)", timeout, errMsg)
			}
			return fmt.Errorf("timed out after %v waiting for server readiness", timeout)
		case <-doneCh:
			errMsg := strings.TrimSpace(stderr.String())
			if errMsg != "" {
				return fmt.Errorf("child server exited prematurely (stderr: %s)", errMsg)
			}
			return errors.New("child server exited prematurely")
		case <-ticker.C:
			if s.probeReady() {
				return nil
			}
		}
	}
}

func (s *Supervisor) probeReady() bool {
	isUnix, socketPath := IsUnixSocket(s.cfg.ServerSocket)
	if isUnix {
		conn, err := net.DialTimeout("unix", socketPath, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return true
		}
		return false
	}

	target := s.cfg.ServerSocket
	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		u, err := url.Parse(target)
		if err == nil {
			target = u.Host
		}
	}
	conn, err := net.DialTimeout("tcp", target, 100*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		return true
	}
	return false
}

// Stop terminates the child server process gracefully via SIGTERM, falling back to SIGKILL.
func (s *Supervisor) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopLocked()
}

func (s *Supervisor) stopLocked() error {
	if s.cmd == nil || s.cmd.Process == nil {
		return nil
	}
	p := s.cmd.Process
	done := s.done

	s.logger.Info("speech: stopping child server", "pid", p.Pid)
	_ = p.Signal(syscall.SIGTERM)

	select {
	case <-done:
		// Exited gracefully
	case <-time.After(3 * time.Second):
		s.logger.Warn("speech: child server did not exit within grace period; killing", "pid", p.Pid)
		_ = p.Kill()
		<-done
	}

	s.cmd = nil
	s.done = nil

	isUnix, socketPath := IsUnixSocket(s.cfg.ServerSocket)
	if isUnix {
		_ = os.Remove(socketPath)
	}

	return nil
}

// IsRunning returns true if the supervised child server is actively running.
func (s *Supervisor) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.isRunningLocked()
}

func (s *Supervisor) isRunningLocked() bool {
	return s.cmd != nil && s.cmd.Process != nil
}

// PID returns the child server process ID, or 0 if not running.
func (s *Supervisor) PID() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd != nil && s.cmd.Process != nil {
		return s.cmd.Process.Pid
	}
	return 0
}

// Restart stops and restarts the child server process.
func (s *Supervisor) Restart(ctx context.Context) error {
	s.mu.Lock()
	_ = s.stopLocked()
	s.mu.Unlock()
	return s.Start(ctx)
}

// IsUnixSocket determines if endpoint represents a Unix domain socket path.
func IsUnixSocket(endpoint string) (bool, string) {
	if strings.HasPrefix(endpoint, "unix://") {
		return true, strings.TrimPrefix(endpoint, "unix://")
	}
	if strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://") {
		return false, ""
	}
	if strings.Contains(endpoint, ":") && !strings.Contains(endpoint, "/") {
		return false, ""
	}
	if strings.HasPrefix(endpoint, "/") || strings.HasPrefix(endpoint, ".") {
		return true, endpoint
	}
	return true, endpoint
}
