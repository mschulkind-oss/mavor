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
	cmd := DefaultCommandWithOpts(t.Context(), "/models/base.bin", "/tmp/audio.wav", 8, false)
	args := strings.Join(cmd.Args, " ")
	if !strings.Contains(args, "-t 8") {
		t.Errorf("expected -t 8 in args, got: %s", args)
	}
}

// TestWhisperCommandNeverPassesNGL is the regression test for the bug that
// gpu = "auto"|"off" replaced. mavor used to append "-ngl <n>" whenever
// gpu_layers was non-zero; whisper-cli has no such flag and exits with
// "error: unknown argument: -ngl", so every transcription failed. No
// configuration may put that flag on the command line again.
func TestWhisperCommandNeverPassesNGL(t *testing.T) {
	for _, noGPU := range []bool{false, true} {
		for _, threads := range []int{0, 1, 32} {
			cmd := DefaultCommandWithOpts(t.Context(), "/models/base.bin", "/tmp/audio.wav", threads, noGPU)
			args := strings.Join(cmd.Args, " ")
			if strings.Contains(args, "-ngl") {
				t.Fatalf("whisper-cli argv contains -ngl, which the binary rejects (threads=%d no_gpu=%v): %s",
					threads, noGPU, args)
			}
		}
	}
}

func TestWhisperCommandGPUOffPassesNG(t *testing.T) {
	cmd := DefaultCommandWithOpts(t.Context(), "/models/base.bin", "/tmp/audio.wav", 4, true)
	args := strings.Join(cmd.Args, " ")
	if !strings.Contains(args, "-ng") {
		t.Errorf("gpu off should pass -ng, got: %s", args)
	}
}

func TestWhisperCommandGPUAutoOmitsNG(t *testing.T) {
	cmd := DefaultCommandWithOpts(t.Context(), "/models/base.bin", "/tmp/audio.wav", 4, false)
	for _, a := range cmd.Args {
		if a == "-ng" || a == "--no-gpu" {
			t.Errorf("gpu auto must not disable the GPU, got argv: %v", cmd.Args)
		}
	}
}
