// Package speech turns recorded audio into text. Transcriber is the one
// contract the daemon depends on, and Factory picks the implementation from
// configuration: an out-of-process whisper-cli, a warm whisper-server reached
// over HTTP or a Unix socket, or in-process sherpa-onnx recognizers. The
// interface also lets the daemon swap in a Mock for unit tests — deterministic
// text, no model load.
package speech

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"
)

type Transcriber interface {
	// Transcribe runs the model against wavPath and returns the recognized
	// text, trimmed of leading/trailing whitespace. Cancelling ctx kills the
	// underlying process.
	Transcribe(ctx context.Context, wavPath string) (string, error)
}

// CommandFunc builds the exec.Cmd that runs whisper against wavPath. The
// command must write its result to wavPath + ".txt" (whisper-cli's `-otxt`
// behavior). Injected so tests can substitute a fake binary.
type CommandFunc func(ctx context.Context, model, wavPath string) *exec.Cmd

// DefaultCommand builds the whisper-cli invocation we use in production.
//
//	whisper-cli -m <model> -f <wav> -otxt -nt -np
//
// -otxt writes <wav>.txt; -nt suppresses timestamps; -np silences progress.
func DefaultCommand(ctx context.Context, model, wavPath string) *exec.Cmd {
	return DefaultCommandWithOpts(ctx, model, wavPath, 0, 0)
}

// DefaultCommandWithOpts builds the whisper-cli invocation with optional GPU layer
// offloading (-ngl) and CPU thread count (-t).
func DefaultCommandWithOpts(ctx context.Context, model, wavPath string, gpuLayers, threads int) *exec.Cmd {
	args := []string{
		"-m", model,
		"-f", wavPath,
		"-otxt", "-nt", "-np",
	}
	if gpuLayers > 0 {
		args = append(args, "-ngl", fmt.Sprint(gpuLayers))
	}
	if threads > 0 {
		args = append(args, "-t", fmt.Sprint(threads))
	}
	return exec.CommandContext(ctx, "whisper-cli", args...)
}

type WhisperCli struct {
	ModelPath string
	GPULayers int
	Threads   int
	Build     CommandFunc
	Logger    *slog.Logger
}

func NewWhisperCli(modelPath string) *WhisperCli {
	return &WhisperCli{
		ModelPath: modelPath,
		Logger:    slog.Default(),
	}
}

func (w *WhisperCli) Transcribe(ctx context.Context, wavPath string) (string, error) {
	log := w.Logger
	if log == nil {
		log = slog.Default()
	}
	var cmd *exec.Cmd
	if w.Build != nil {
		cmd = w.Build(ctx, w.ModelPath, wavPath)
	} else {
		cmd = DefaultCommandWithOpts(ctx, w.ModelPath, wavPath, w.GPULayers, w.Threads)
	}
	wavSize := int64(-1)
	if fi, err := os.Stat(wavPath); err == nil {
		wavSize = fi.Size()
	}
	log.Info("speech: launching whisper-cli",
		"argv", cmd.Args,
		"model", w.ModelPath,
		"wav", wavPath,
		"wav_size", wavSize,
	)
	start := time.Now()
	out, err := cmd.CombinedOutput()
	elapsed := time.Since(start)
	exitCode := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		exitCode = exitErr.ExitCode()
	}
	log.Info("speech: whisper-cli exited",
		"exit_code", exitCode,
		"err", fmt.Sprint(err),
		"duration_ms", elapsed.Milliseconds(),
		"combined_output", truncate(string(out), 4000),
	)
	if err != nil {
		return "", fmt.Errorf("speech: whisper-cli: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	sidecar := wavPath + ".txt"
	body, err := os.ReadFile(sidecar)
	if err != nil {
		log.Error("speech: sidecar missing", "path", sidecar, "err", err)
		return "", fmt.Errorf("speech: read sidecar %s: %w", sidecar, err)
	}
	text := strings.TrimSpace(string(body))
	log.Info("speech: transcript ready",
		"sidecar", sidecar,
		"text_len", len(text),
		"text_preview", truncate(text, 200),
	)
	return text, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Mock returns Text on every call. Useful for unit tests of the daemon's
// orchestration without loading a model.
type Mock struct {
	Text string
	Err  error
	// Delay makes Transcribe block, so tests can observe the Transcribing
	// state rather than racing past it.
	Delay time.Duration
}

func (m *Mock) Transcribe(ctx context.Context, _ string) (string, error) {
	if m.Delay > 0 {
		select {
		case <-time.After(m.Delay):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return m.Text, m.Err
}
