// Package output dispatches transcribed text to the user — typing it into
// the focused window via wtype AND placing it on the clipboard via wl-copy.
// Both happen on every emission; if one fails the other is still attempted
// and errors are joined.
package output

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type Dispatcher interface {
	Emit(ctx context.Context, text string) error
}

// Runner is the indirection that lets tests intercept external commands
// without spawning real wtype / wl-copy. It runs the named binary with args
// and pipes the given stdin (used by wl-copy).
type Runner func(ctx context.Context, name string, args []string, stdin []byte) error

// DefaultRunner shells out via os/exec.
func DefaultRunner(ctx context.Context, name string, args []string, stdin []byte) error {
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	if name == "wl-copy" {
		// wl-copy daemonizes itself to hold clipboard selection and leaves stderr open in the child.
		// Running without capturing child stderr prevents blocking indefinitely on pipe EOF.
		return cmd.Run()
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w (%s)", name, err, bytes.TrimSpace(out))
	}
	return nil
}

type Wayland struct {
	Run    Runner
	Logger *slog.Logger
}

func NewWayland() *Wayland {
	return &Wayland{Run: DefaultRunner, Logger: slog.Default()}
}

// CleanText normalizes whitespace and removes interior newlines so wtype does
// not emit accidental Return keystrokes into focused applications.
func CleanText(s string) string {
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

// Emit types text into the focused window AND copies it to the clipboard.
// We run both regardless of which (if any) succeeds; a copy-on-failure is
// still useful even when typing didn't reach the right window.
func (w *Wayland) Emit(ctx context.Context, text string) error {
	log := w.Logger
	if log == nil {
		log = slog.Default()
	}
	text = CleanText(text)
	if text == "" {
		return nil
	}
	start := time.Now()
	log.Info("output: dispatching", "text_len", len(text), "text_preview", truncate(text, 200))
	typeErr := w.Run(ctx, "wtype", []string{"--", text}, nil)
	log.Info("output: wtype done", "err", fmt.Sprint(typeErr), "elapsed_ms", time.Since(start).Milliseconds())
	copyStart := time.Now()
	copyErr := w.Run(ctx, "wl-copy", nil, []byte(text))
	log.Info("output: wl-copy done", "err", fmt.Sprint(copyErr), "elapsed_ms", time.Since(copyStart).Milliseconds())
	return errors.Join(typeErr, copyErr)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Mock records every Emit call. Used for unit tests of the daemon
// orchestration; Emit is goroutine-safe so daemon tests don't trip the race
// detector when reading Calls from one goroutine while the daemon emits
// from another.
type Mock struct {
	Err   error
	mu    sync.Mutex
	calls []string
}

func (m *Mock) Emit(_ context.Context, text string) error {
	m.mu.Lock()
	m.calls = append(m.calls, text)
	m.mu.Unlock()
	return m.Err
}

// Calls returns a snapshot of the text strings Emit was called with.
func (m *Mock) Calls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.calls))
	copy(out, m.calls)
	return out
}

// CopyOnly places text on the clipboard without typing it. Recovery from the
// transcript history has to reach the clipboard without injecting keystrokes
// into whatever window happens to be focused at the time.
func (w *Wayland) CopyOnly(ctx context.Context, text string) error {
	if err := w.Run(ctx, "wl-copy", nil, []byte(text)); err != nil {
		return fmt.Errorf("wl-copy: %w", err)
	}
	return nil
}
