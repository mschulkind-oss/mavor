package speech

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/mschulkind-oss/mavor/internal/config"
)

// Factory instantiates a Transcriber based on the configured engine in cfg.
// Supported engines: "cli", "server", "sherpa".
func Factory(cfg config.Config, logger *slog.Logger) (Transcriber, error) {
	if logger == nil {
		logger = slog.Default()
	}

	engine := strings.ToLower(strings.TrimSpace(cfg.Engine))
	if engine == "" {
		engine = "cli"
	}

	switch engine {
	case "cli":
		modelPath := WhisperModelPath(cfg.ModelDir, cfg.Model)
		if _, err := os.Stat(modelPath); err != nil {
			return nil, fmt.Errorf("speech: model %q not found at %s — run `mavor models pull %s`: %w", cfg.Model, modelPath, cfg.Model, err)
		}
		cli := NewWhisperCli(modelPath)
		cli.Threads = cfg.Threads
		cli.NoGPU = cfg.GPUOff()
		cli.Logger = logger
		return cli, nil

	case "server":
		modelPath := WhisperModelPath(cfg.ModelDir, cfg.Model)
		isUnix, _ := IsUnixSocket(cfg.ServerSocket)
		if isUnix {
			if _, err := os.Stat(modelPath); err != nil {
				return nil, fmt.Errorf("speech: model %q not found at %s — run `mavor models pull %s`: %w", cfg.Model, modelPath, cfg.Model, err)
			}
		}

		st := NewServerTranscriber(cfg.ServerSocket)
		st.Model = cfg.Model
		st.Logger = logger

		if isUnix {
			st.Supervisor = NewSupervisor(SupervisorConfig{
				ModelPath:    modelPath,
				ServerSocket: cfg.ServerSocket,
				Threads:      cfg.Threads,
				NoGPU:        cfg.GPUOff(),
				Logger:       logger,
			})
		}
		return st, nil

	case "sherpa":
		return newSherpaTranscriber(cfg, logger)

	default:
		return nil, fmt.Errorf("speech: unknown engine %q (supported: cli, server, sherpa)", cfg.Engine)
	}
}
