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

	// ServerSocket is where the child should listen: an HTTP URL or a bare
	// host:port. Anything else — including the empty value, which is what
	// the daemon passes — means "pick somewhere", and Start binds a free
	// loopback port. There is no config key behind this any more; the
	// placement decides whether there is a child at all.
	ServerSocket string

	// Threads is the number of CPU threads (-t).
	Threads int

	// NoGPU forces CPU execution (-ng). whisper-server has no layer-offload
	// flag; it uses whatever GPU backend its build loaded unless told not to.
	NoGPU bool

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

	// endpoint is where the child is actually listening, which is not always
	// what was configured: see Start.
	endpoint string
}

// Endpoint reports the address the supervised child listens on, once it has
// been started. It is not always the configured ServerSocket — a Unix socket
// path becomes a loopback address — so a client must ask rather than assume.
func (s *Supervisor) Endpoint() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.endpoint
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
	if cfg.NoGPU {
		args = append(args, "-ng")
	}

	// A host and a port is the only way this binary can be told where to
	// listen. There is no --socket flag; passing one makes it print its usage
	// and exit before it binds anything, which is why Start rewrites a Unix
	// socket endpoint into a loopback address before getting here.
	host, port := hostPort(cfg.ServerSocket)
	if host != "" {
		args = append(args, "--host", host)
	}
	if port != "" {
		args = append(args, "--port", port)
	}

	return exec.CommandContext(ctx, binary, args...)
}

// hostPort splits a TCP endpoint — an http URL or a bare host:port — into its
// parts. A Unix socket path has neither and yields two empty strings.
func hostPort(endpoint string) (host, port string) {
	if isUnix, _ := IsUnixSocket(endpoint); isUnix || endpoint == "" {
		return "", ""
	}
	if strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://") {
		if u, err := url.Parse(endpoint); err == nil {
			return u.Hostname(), u.Port()
		}
		return "", ""
	}
	if h, p, err := net.SplitHostPort(endpoint); err == nil {
		return h, p
	}
	return endpoint, ""
}

// freeLoopbackPort asks the kernel for an unused port. There is a window
// between closing this listener and the child binding the same port, which is
// the standard cost of this approach: the alternative is a fixed port that
// collides with whatever else the user is running.
func freeLoopbackPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// Start launches the child server process and blocks until it is ready to accept connections.
func (s *Supervisor) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.isRunningLocked() {
		return nil
	}

	// whisper.cpp's server binds a host and a port and cannot bind a Unix
	// socket, so anything that is not a TCP address means "pick somewhere and
	// run one for me". Honour that on loopback, and say so: silently
	// listening somewhere other than where a caller pointed is worse than the
	// failure it replaces.
	endpoint := s.cfg.ServerSocket
	if isUnix, socketPath := IsUnixSocket(endpoint); isUnix {
		port, err := freeLoopbackPort()
		if err != nil {
			return fmt.Errorf("speech: supervisor: find a port for the child server: %w", err)
		}
		endpoint = fmt.Sprintf("http://127.0.0.1:%d", port)
		if socketPath == "" {
			s.logger.Info("speech: supervising whisper-server on loopback", "listening_on", endpoint)
		} else {
			s.logger.Info("speech: the configured endpoint is a Unix socket path, which whisper-server cannot bind; supervising on loopback instead",
				"configured", socketPath, "listening_on", endpoint)
		}
	}
	s.endpoint = endpoint

	childCfg := s.cfg
	childCfg.ServerSocket = endpoint

	var cmd *exec.Cmd
	if s.cfg.CommandFunc != nil {
		cmd = s.cfg.CommandFunc(ctx, childCfg)
	} else {
		cmd = DefaultServerCommand(ctx, childCfg)
	}

	s.logger.Info("speech: supervisor launching child server",
		"binary", cmd.Path,
		"argv", cmd.Args,
		"endpoint", endpoint,
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

// probeReady dials where the child was told to listen. It runs under the same
// lock as Start, which is what makes reading s.endpoint here safe.
func (s *Supervisor) probeReady() bool {
	host, port := hostPort(s.endpoint)
	if host == "" || port == "" {
		return false
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 100*time.Millisecond)
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
	s.endpoint = ""

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
