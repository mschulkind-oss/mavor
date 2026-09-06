//go:build e2e

package speech

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/mschulkind-oss/mavor/internal/config"
)

// The supervised warm server shipped unable to start for weeks, and every
// unit test passed throughout: the fakes accepted the flag the real binary
// rejects and answered on the path the real binary does not serve. This test
// is the one that would have caught it — a real `whisper-server`, started the
// way the daemon starts it, over the config a user is actually given.
func TestLocalServerPlacementAgainstTheRealWhisperServer(t *testing.T) {
	if _, err := exec.LookPath("whisper-server"); err != nil {
		if _, err := exec.LookPath("whisper-cpp-server"); err != nil {
			t.Skip("no whisper-server on PATH")
		}
	}
	modelDir := config.DefaultModelDir()
	modelPath := WhisperModelPath(modelDir, "whisper-tiny.en")
	if _, err := os.Stat(modelPath); err != nil {
		t.Skipf("tiny.en is not downloaded (%s); run `just test-e2e`", modelPath)
	}
	audio := "../../test/fixtures/real_speech.wav"
	if _, err := os.Stat(audio); err != nil {
		t.Skipf("no audio fixture at %s", audio)
	}

	// A whisper model with nothing else set is what `mavor config init`
	// writes, and it resolves to the supervised warm server. That is the
	// configuration that has to work.
	cfg := config.Default()
	cfg.Model = "whisper-tiny.en"
	cfg.Paths.Models = modelDir
	cfg.Advanced.Threads = 4

	transcriber, err := Factory(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("Factory: %v", err)
	}
	t.Cleanup(func() {
		if c, ok := transcriber.(interface{ Close() error }); ok {
			_ = c.Close()
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if starter, ok := transcriber.(interface{ Start(context.Context) error }); ok {
		if err := starter.Start(ctx); err != nil {
			t.Fatalf("starting the supervised server: %v", err)
		}
	}

	// Twice: the first call discovers the request path, the second must use
	// what it learned and still land.
	for i := 0; i < 2; i++ {
		text, err := transcriber.Transcribe(ctx, audio)
		if err != nil {
			t.Fatalf("Transcribe %d: %v", i, err)
		}
		if !strings.Contains(strings.ToLower(text), "lux") {
			t.Fatalf("Transcribe %d returned %q, which does not look like the fixture", i, text)
		}
	}
}
