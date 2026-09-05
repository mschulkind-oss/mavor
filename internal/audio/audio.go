// Package audio captures speech from a PulseAudio/PipeWire source via parec.
// The Recorder interface keeps the daemon's state machine agnostic to the
// real capture process — tests use a Mock that returns canned WAV fixtures.
//
// Architecture and invariants: docs/reference/how-mavor-works.md
package audio

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type Recorder interface {
	// Start begins capturing audio. Cancelling ctx is equivalent to Stop().
	// Returns an error if a capture is already in progress.
	Start(ctx context.Context) error
	// Stop ends the current capture and returns the path to the written
	// WAV file. Calling Stop without a prior Start returns an error.
	Stop() (wavPath string, err error)
	// Level returns the current instantaneous audio RMS energy level [0.0, 1.0].
	Level() float64
}

// ChunkReader is an optional interface implemented by Recorders that can
// stream newly captured raw PCM audio chunks while capture is in progress.
type ChunkReader interface {
	// ReadChunk returns raw PCM audio bytes captured since the last call.
	ReadChunk() ([]byte, error)
}

// CommandFunc builds the exec.Cmd that captures audio to outPath. Injected
// so tests can substitute a fake recorder (e.g. shell that copies a fixture).
type CommandFunc func(outPath string) *exec.Cmd

// DefaultCommand builds the parec invocation we use in production. 16 kHz
// mono s16le matches whisper's native input format, so no transcoding is
// needed before passing the WAV to whisper-cli.
func DefaultCommand(outPath string) *exec.Cmd {
	return exec.Command(
		"parec",
		"--format=s16le",
		"--rate=16000",
		"--channels=1",
		"--file-format=wav",
		outPath,
	)
}

type ParecRecorder struct {
	dir         string      // directory where WAV files land
	build       CommandFunc // overridable for tests
	logger      *slog.Logger
	mu          sync.Mutex
	cmd         *exec.Cmd
	stderr      *bytes.Buffer
	outPath     string
	readOffset  int64
	started     time.Time
	stopMonitor chan struct{}
	level       atomic.Uint64
}

func NewParecRecorder(dir string) *ParecRecorder {
	return &ParecRecorder{dir: dir, build: DefaultCommand, logger: slog.Default()}
}

// SetLogger swaps the logger used for verbose diagnostics. Set to a
// no-op handler in tests if you want to silence output.
func (r *ParecRecorder) SetLogger(l *slog.Logger) { r.logger = l }

// SetCommand overrides the command builder. Test-only entry point.
func (r *ParecRecorder) SetCommand(fn CommandFunc) {
	r.build = fn
}

func (r *ParecRecorder) setLevel(lvl float64) {
	r.level.Store(math.Float64bits(lvl))
}

// SetLevel overrides the current level. Test-friendly helper.
func (r *ParecRecorder) SetLevel(lvl float64) {
	r.setLevel(lvl)
}

// Level returns the instantaneous audio energy level [0.0, 1.0].
func (r *ParecRecorder) Level() float64 {
	r.mu.Lock()
	started := r.cmd != nil
	r.mu.Unlock()
	if !started {
		return 0.0
	}
	return math.Float64frombits(r.level.Load())
}

func (r *ParecRecorder) monitorLevel(outPath string, stop chan struct{}) {
	ticker := time.NewTicker(30 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			samples, err := ReadRecentSamples(outPath, FrameSamples)
			if err == nil && len(samples) > 0 {
				rms := CalculateRMS(samples)
				r.setLevel(rms)
			}
		}
	}
}

func (r *ParecRecorder) Start(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd != nil {
		return errors.New("audio: capture already in progress")
	}
	if err := os.MkdirAll(r.dir, 0o755); err != nil {
		return fmt.Errorf("audio: mkdir %s: %w", r.dir, err)
	}
	out := filepath.Join(r.dir, fmt.Sprintf("rec-%d.wav", time.Now().UnixNano()))
	cmd := r.build(out)
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	r.logger.Info("audio: launching capture",
		"argv", cmd.Args,
		"pulse_source", os.Getenv("PULSE_SOURCE"),
		"out", out,
	)
	if err := cmd.Start(); err != nil {
		r.logger.Error("audio: parec start failed", "err", err, "argv", cmd.Args)
		return fmt.Errorf("audio: start: %w", err)
	}
	r.cmd, r.outPath, r.readOffset, r.stderr, r.started = cmd, out, 44, stderr, time.Now()
	stopMon := make(chan struct{})
	r.stopMonitor = stopMon
	go r.monitorLevel(out, stopMon)
	r.logger.Info("audio: parec running", "pid", cmd.Process.Pid)

	// Cancelling ctx is equivalent to calling Stop. We don't take r.mu in the
	// goroutine — Stop synchronizes via Wait().
	if ctx != nil {
		go func() {
			<-ctx.Done()
			r.signal(syscall.SIGINT)
		}()
	}
	return nil
}

// ReadChunk returns raw PCM bytes appended to the active recording WAV since last read.
func (r *ParecRecorder) ReadChunk() ([]byte, error) {
	r.mu.Lock()
	out := r.outPath
	offset := r.readOffset
	r.mu.Unlock()

	if out == "" {
		return nil, nil
	}

	f, err := os.Open(out)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}

	size := stat.Size()
	if size <= 44 || size <= offset {
		return nil, nil
	}

	if offset < 44 {
		offset = 44
	}

	bytesToRead := size - offset
	buf := make([]byte, bytesToRead)
	n, err := f.ReadAt(buf, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}

	r.mu.Lock()
	r.readOffset = offset + int64(n)
	r.mu.Unlock()

	return buf[:n], nil
}

func (r *ParecRecorder) Stop() (string, error) {
	r.mu.Lock()
	cmd, out, stderr, started := r.cmd, r.outPath, r.stderr, r.started
	stopMon := r.stopMonitor
	r.cmd, r.outPath, r.readOffset, r.stderr, r.stopMonitor = nil, "", 0, nil, nil
	r.setLevel(0.0)
	r.mu.Unlock()
	if stopMon != nil {
		close(stopMon)
	}
	if cmd == nil {
		return "", errors.New("audio: not started")
	}
	r.logger.Info("audio: stopping capture (SIGINT to parec)",
		"pid", cmd.Process.Pid, "elapsed_ms", time.Since(started).Milliseconds())
	// SIGINT lets parec flush its WAV header. SIGKILL would leave a truncated
	// file that whisper can't read.
	r.signalCmd(cmd, syscall.SIGINT)
	waitErr := cmd.Wait()
	exitCode := 0
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		exitCode = exitErr.ExitCode()
	}
	stderrBytes := []byte(nil)
	if stderr != nil {
		stderrBytes = stderr.Bytes()
	}
	size := int64(-1)
	if fi, err := os.Stat(out); err == nil {
		size = fi.Size()
	}
	r.logger.Info("audio: parec exited",
		"exit_code", exitCode,
		"wait_err", fmt.Sprint(waitErr),
		"wav_path", out,
		"wav_size", size,
		"stderr", truncate(string(stderrBytes), 2000),
	)
	if waitErr != nil && !errors.As(waitErr, &exitErr) {
		return "", fmt.Errorf("audio: wait: %w", waitErr)
	}
	if size <= 0 {
		return "", fmt.Errorf("audio: empty WAV at %s (parec exit=%d, stderr=%q)", out, exitCode, truncate(string(stderrBytes), 500))
	}
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func (r *ParecRecorder) signal(sig os.Signal) {
	r.mu.Lock()
	cmd := r.cmd
	r.mu.Unlock()
	if cmd != nil {
		r.signalCmd(cmd, sig)
	}
}

func (r *ParecRecorder) signalCmd(cmd *exec.Cmd, sig os.Signal) {
	if cmd.Process != nil {
		_ = cmd.Process.Signal(sig)
	}
}

// MockRecorder is the test-friendly implementation. Start/Stop just toggle a
// flag; Stop returns FixturePath. Useful for unit-testing the daemon's
// orchestration without needing PipeWire running.
type MockRecorder struct {
	FixturePath string
	LevelVal    float64
	ChunkData   []byte
	Chunks      [][]byte
	chunkIdx    int
	mu          sync.Mutex
	started     bool
}

func (m *MockRecorder) SetLevel(lvl float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.LevelVal = lvl
}

func (m *MockRecorder) Level() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.started {
		return 0.0
	}
	return m.LevelVal
}

func (m *MockRecorder) SetChunks(chunks ...[]byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Chunks = chunks
	m.chunkIdx = 0
}

func (m *MockRecorder) SetChunkData(data []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ChunkData = data
}

func (m *MockRecorder) ReadChunk() ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.started {
		return nil, nil
	}
	if len(m.Chunks) > 0 {
		if m.chunkIdx < len(m.Chunks) {
			c := m.Chunks[m.chunkIdx]
			m.chunkIdx++
			return c, nil
		}
		return nil, nil
	}
	if len(m.ChunkData) > 0 {
		return m.ChunkData, nil
	}
	return []byte{0, 0, 0, 0}, nil
}

func (m *MockRecorder) Start(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		return errors.New("audio: capture already in progress")
	}
	m.started = true
	m.chunkIdx = 0
	return nil
}

func (m *MockRecorder) Stop() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.started {
		return "", errors.New("audio: not started")
	}
	m.started = false
	return m.FixturePath, nil
}
