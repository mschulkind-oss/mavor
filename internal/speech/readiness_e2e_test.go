//go:build e2e

package speech

import (
	"context"
	"github.com/mschulkind-oss/mavor/internal/config"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// This acceptance uses mounted existing models and actual encoder/decoder
// calls on the generated controlled fixture; recognized text is discarded.
func TestReadinessMountedModels(t *testing.T) {
	root := os.Getenv("MAVOR_READINESS_MODEL_DIR")
	if root == "" {
		t.Skip("set MAVOR_READINESS_MODEL_DIR to existing mounted models")
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, name := range []string{"whisper-base.en", "zipformer-streaming-20m"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
			defer cancel()
			var tr Transcriber
			if name == "whisper-base.en" {
				st := NewServerTranscriber("")
				st.Logger = logger
				st.Supervisor = NewSupervisor(SupervisorConfig{ModelPath: filepath.Join(root, "ggml-base.en.bin"), NoGPU: true, Threads: 2, Logger: logger})
				tr = st
			} else {
				cfg := config.Default()
				cfg.Model = name
				cfg.Paths.Models = root
				cfg.Advanced.Threads = 2
				cfg.Advanced.GPU = "off"
				var e error
				tr, e = NewSherpaTranscriber(cfg, logger)
				if e != nil {
					t.Fatal(e)
				}
			}
			defer closeQuietly(tr)
			if st, ok := tr.(interface{ Start(context.Context) error }); ok {
				if e := st.Start(ctx); e != nil {
					t.Fatal(e)
				}
			}
			for cycle := 0; cycle < 2; cycle++ {
				probe, stop := context.WithTimeout(ctx, 30*time.Second)
				e := VerifyReadiness(probe, tr)
				stop()
				if e != nil {
					t.Fatalf("probe %d: %v", cycle, e)
				}
			}
		})
	}
}
