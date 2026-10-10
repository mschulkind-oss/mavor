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

	// AllowCPUFallback opts into one CPU retry after a GPU readiness or inference
	// failure. Disabled by default; NoGPU still selects CPU explicitly.
	AllowCPUFallback bool

	// Prompt is the initial prompt (--prompt) the child applies to every
	// request it serves: the vocabulary, rendered as text. Empty passes no
	// flag. See speech.WhisperPrompt.
	Prompt string

	// CommandFunc allows injecting a custom command generator (useful for unit tests).
	CommandFunc func(ctx context.Context, cfg SupervisorConfig) *exec.Cmd

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
	if cfg.Prompt != "" {
		args = append(args, "--prompt", cfg.Prompt)
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

// Start launches the child server and waits for readiness. A failed GPU-enabled
// startup is retried once with GPU disabled only when AllowCPUFallback is set;
// cancellation and launch errors are not reasons to change devices.
func (s *Supervisor) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	err := s.startLocked(ctx)
	var readinessErr *serverReadinessError
	if err == nil || !s.cfg.AllowCPUFallback || s.cfg.NoGPU || ctx.Err() != nil || !errors.As(err, &readinessErr) {
		return err
	}
	if cpuErr := s.fallbackLocked(ctx, err); cpuErr != nil {
		return errors.Join(err, cpuErr)
	}
	return nil
}

// GPUEnabled reports whether the supervised child may use a GPU. It does not
// assert that the binary actually loaded a GPU backend.
func (s *Supervisor) GPUEnabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.cfg.NoGPU
}

// CPUFallbackAllowed reports the explicit recovery policy, independently of
// whether the child may use a GPU.
func (s *Supervisor) CPUFallbackAllowed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.AllowCPUFallback
}

// FallbackToCPU stops the GPU-enabled child before starting its CPU replacement.
// Callers can then retry the failed request using the original recording.
// With recovery disabled, it returns cause unchanged without touching the child.
func (s *Supervisor) FallbackToCPU(ctx context.Context, cause error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.cfg.AllowCPUFallback {
		return cause
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.cfg.NoGPU {
		return nil
	}
	return s.fallbackLocked(ctx, cause)
}

func (s *Supervisor) fallbackLocked(ctx context.Context, cause error) error {
	s.logger.Warn("speech: GPU-enabled whisper-server failed; retrying on CPU (transcription may be slower)",
		"err", cause, "until", "daemon restart")
	if err := s.stopLocked(); err != nil {
		return fmt.Errorf("speech: stop GPU-enabled server before CPU recovery: %w", err)
	}
	s.cfg.NoGPU = true
	if err := s.startLocked(ctx); err != nil {
		return fmt.Errorf("speech: CPU recovery failed: %w", err)
	}
	return nil
}

type serverReadinessError struct{ err error }

func (e *serverReadinessError) Error() string { return e.err.Error() }
func (e *serverReadinessError) Unwrap() error { return e.err }

// childStderr retains the last 64 KiB of diagnostics. The subprocess copier
// writes concurrently with readiness checks, so both accesses need a lock.
type childStderr struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *childStderr) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, err := b.buf.Write(p)
	if excess := b.buf.Len() - 64*1024; excess > 0 {
		b.buf.Next(excess)
	}
	return n, err
}

func (b *childStderr) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (s *Supervisor) startLocked(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
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

	// Readiness has a deadline, but the transferred warm child is owned by
	// Supervisor.Stop, not by that temporary operation context. On readiness
	// cancellation/failure waitForReady still calls stopLocked and joins it.
	childCtx := context.WithoutCancel(ctx)
	var cmd *exec.Cmd
	if s.cfg.CommandFunc != nil {
		cmd = s.cfg.CommandFunc(childCtx, childCfg)
	} else {
		cmd = DefaultServerCommand(childCtx, childCfg)
	}

	s.logger.Info("speech: supervisor launching child server",
		"binary", cmd.Path,
		"argv", cmd.Args,
		"endpoint", endpoint,
		"model", s.cfg.ModelPath,
	)

	var stderrBuf childStderr
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
		return &serverReadinessError{err: fmt.Errorf("speech: supervisor: server readiness check failed: %w", err)}
	}

	s.logger.Info("speech: child server ready", "pid", cmd.Process.Pid)
	return nil
}

// waitForReady blocks until the child accepts connections, the child exits, or
// ctx is cancelled — and it has no third outcome.
//
// It deliberately has no deadline. A server that is slow to become ready is a
// server that is loading a model, and the time that takes is a property of the
// machine and the moment, not of the code: a 3 GB model read on a busy disk is
// tens of seconds, and on a saturated one it is minutes. A deadline here does
// not detect a broken child — it detects a slow one, converts that into a
// failed startup, and the supervisor's restart then re-reads the model from the
// very resource that was too slow, which makes the next attempt slower still.
//
// Nothing is lost by waiting. A child that has actually died is reported by
// doneCh (and by the exit watcher, which needs no deadline at all), a wedged
// one is visible through the daemon's initializing state and answers neither,
// and a shutdown is reported through ctx. A caller that must give up can pass a
// context with a deadline, which is the one place the policy belongs.
func (s *Supervisor) waitForReady(ctx context.Context, doneCh chan struct{}, stderr *childStderr) error {
	ticker := time.NewTicker(s.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
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
	if s.cmd == nil || s.cmd.Process == nil {
		return false
	}
	select {
	case <-s.done:
		return false
	default:
		return true
	}
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
