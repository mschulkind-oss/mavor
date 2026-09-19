//go:build e2e

package speech

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mschulkind-oss/mavor/internal/config"
)

func TestChunkingE2E_WhisperTranscribes68sWithoutDroppingSpeech(t *testing.T) {
	if _, err := exec.LookPath("whisper-cli"); err != nil {
		t.Skip("no whisper-cli on PATH")
	}
	modelDir := config.DefaultModelDir()
	modelPath := WhisperModelPath(modelDir, "whisper-base.en")
	if _, err := os.Stat(modelPath); err != nil {
		t.Skipf("whisper-base.en is not downloaded (%s)", modelPath)
	}
	audioFixture := filepath.Join("..", "..", "test", "fixtures", "llm_prompt_68s.wav")
	if _, err := os.Stat(audioFixture); err != nil {
		t.Skipf("no audio fixture at %s", audioFixture)
	}

	for _, mode := range []string{"auto", "vad", "overlap"} {
		t.Run("mode="+mode, func(t *testing.T) {
			cfg := config.Default()
			cfg.Model = "whisper-base.en"
			cfg.Paths.Models = modelDir
			cfg.Advanced.Placement = "subprocess"
			cfg.Advanced.Chunking = mode
			cfg.Advanced.Threads = 6

			tx, err := Factory(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatalf("Factory: %v", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			start := time.Now()
			text, err := tx.Transcribe(ctx, audioFixture)
			elapsed := time.Since(start)
			if err != nil {
				t.Fatalf("Transcribe failed: %v", err)
			}

			lower := strings.ToLower(text)
			t.Logf("Mode %s transcribed in %v (length %d):\n%s", mode, elapsed, len(text), text)

			// Raw whisper drops "let's verify" across the 30s boundary.
			// Chunking must preserve it.
			if !strings.Contains(lower, "verify") {
				t.Errorf("transcript dropped 'verify' at 30s boundary: %s", text)
			}
			if !strings.Contains(lower, "wayland") && !strings.Contains(lower, "wayloon") {
				t.Errorf("transcript missing 'wayland': %s", text)
			}
			if !strings.Contains(lower, "summary of what broke") {
				t.Errorf("transcript missing tail 'summary of what broke': %s", text)
			}
		})
	}
}
