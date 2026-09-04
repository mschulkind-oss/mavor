package speech

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMockReturnsConfiguredText(t *testing.T) {
	m := &Mock{Text: "hello world"}
	got, err := m.Transcribe(t.Context(), "/does/not/matter.wav")
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello world" {
		t.Fatalf("got %q, want %q", got, "hello world")
	}
}

func TestMockPropagatesError(t *testing.T) {
	want := errors.New("boom")
	m := &Mock{Err: want}
	if _, err := m.Transcribe(t.Context(), ""); !errors.Is(err, want) {
		t.Fatalf("got %v, want %v", err, want)
	}
}

func TestWhisperCliReadsSidecar(t *testing.T) {
	wav := filepath.Join(t.TempDir(), "clip.wav")
	if err := os.WriteFile(wav, []byte("RIFF...WAVE"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := NewWhisperCli("ignored")
	w.Build = func(ctx context.Context, _, wavPath string) *exec.Cmd {
		// Fake whisper: write the sidecar that DefaultCommand promises.
		return exec.CommandContext(ctx, "sh", "-c",
			`printf "the quick brown fox\n" > "$1.txt"`, "sh", wavPath)
	}
	got, err := w.Transcribe(t.Context(), wav)
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if got != "the quick brown fox" {
		t.Fatalf("got %q, want %q", got, "the quick brown fox")
	}
}

func TestWhisperCliReportsCommandFailure(t *testing.T) {
	wav := filepath.Join(t.TempDir(), "clip.wav")
	_ = os.WriteFile(wav, []byte{0}, 0o644)
	w := NewWhisperCli("ignored")
	w.Build = func(ctx context.Context, _, _ string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "echo broken >&2; exit 7")
	}
	_, err := w.Transcribe(t.Context(), wav)
	if err == nil {
		t.Fatal("expected error from failing command")
	}
	if !strings.Contains(err.Error(), "broken") {
		t.Fatalf("error %q does not contain command stderr", err)
	}
}

func TestWhisperCliReportsMissingSidecar(t *testing.T) {
	wav := filepath.Join(t.TempDir(), "clip.wav")
	_ = os.WriteFile(wav, []byte{0}, 0o644)
	w := NewWhisperCli("ignored")
	w.Build = func(ctx context.Context, _, _ string) *exec.Cmd {
		// Succeed but don't write the sidecar.
		return exec.CommandContext(ctx, "true")
	}
	_, err := w.Transcribe(t.Context(), wav)
	if err == nil {
		t.Fatal("expected error when sidecar is missing")
	}
}

func TestWhisperCliCancelKillsProcess(t *testing.T) {
	wav := filepath.Join(t.TempDir(), "clip.wav")
	_ = os.WriteFile(wav, []byte{0}, 0o644)
	w := NewWhisperCli("ignored")
	w.Build = func(ctx context.Context, _, _ string) *exec.Cmd {
		return exec.CommandContext(ctx, "sleep", "60")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := w.Transcribe(ctx, wav)
	if err == nil {
		t.Fatal("expected error when context times out")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Transcribe took %v after ctx timeout, want <2s", elapsed)
	}
}

func TestDefaultCommandWithOpts(t *testing.T) {
	cmd := DefaultCommandWithOpts(t.Context(), "/models/base.bin", "/tmp/audio.wav", 32, 8)
	args := strings.Join(cmd.Args, " ")
	if !strings.Contains(args, "-ngl 32") {
		t.Errorf("expected -ngl 32 in args, got: %s", args)
	}
	if !strings.Contains(args, "-t 8") {
		t.Errorf("expected -t 8 in args, got: %s", args)
	}
}
