package speech

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/mschulkind-oss/mavor/internal/config"
	"github.com/mschulkind-oss/mavor/internal/models"
)

// Resolution is what the daemon will actually load: the model, the runtime
// that owns it, where that runtime runs, and the path the model occupies on
// this machine. It is derived once, at daemon start, and is what `mavor
// doctor` reports and what Factory builds from.
type Resolution struct {
	models.Selection

	// ModelPath is the whisper GGML file, set when Runtime is whisper.cpp
	// and inference happens on this machine.
	ModelPath string

	// ModelDir is the sherpa model directory, set when Runtime is
	// sherpa-onnx.
	ModelDir string
}

// Resolve turns a configuration into the model, runtime and placement the
// daemon will use, and fails if the model named cannot be found.
//
// A model that cannot be found is fatal rather than a fallback to something
// else: a user who writes a model name gets that model or an error. `mavor
// setup` leaves the current config fully runnable, so reaching this error
// means the config changed after setup — which is what the message says.
func Resolve(cfg config.Config) (Resolution, error) {
	sel, err := models.Select(cfg.Model, cfg.Advanced.Placement, cfg.Advanced.Server)
	if err != nil {
		return Resolution{}, fmt.Errorf("speech: %w", err)
	}
	res := Resolution{Selection: sel}

	switch sel.Runtime {
	case models.RuntimeWhisper:
		res.ModelPath = WhisperModelPath(cfg.Paths.Models, cfg.Model)
		if sel.Placement == models.PlacementRemote {
			// The model lives wherever the server runs; there is nothing
			// here to check.
			return res, nil
		}
		if _, err := os.Stat(res.ModelPath); err != nil {
			return Resolution{}, fmt.Errorf(
				"speech: model %q not found at %s — run `mavor models pull %s`, or `mavor setup` to install everything this config names: %w",
				cfg.Model, res.ModelPath, cfg.Model, err)
		}
	default:
		dir, err := ResolveSherpaModelDir(cfg)
		if err != nil {
			return Resolution{}, missingSherpaModel(cfg, err)
		}
		res.ModelDir = dir
	}
	return res, nil
}

// missingSherpaModel builds the error for a model that resolved to no
// directory. A name the catalog does not carry is almost always a typo or a
// name from the pre-rename schema, so the message names the entries closest
// to what was written rather than only reporting the miss.
func missingSherpaModel(cfg config.Config, cause error) error {
	if _, known := models.Lookup(cfg.Model); known {
		return fmt.Errorf(
			"speech: model %q is in the catalog but not installed — run `mavor models pull %s`, or `mavor setup` to install everything this config names: %w",
			cfg.Model, cfg.Model, cause)
	}
	near := models.Nearest(cfg.Model, 3)
	if len(near) == 0 {
		return fmt.Errorf(
			"speech: model %q is not in the catalog and no directory of that name exists under %s — `mavor models list` shows every name that resolves: %w",
			cfg.Model, cfg.Paths.Models, cause)
	}
	quoted := make([]string, len(near))
	for i, n := range near {
		quoted[i] = fmt.Sprintf("%q", n)
	}
	return fmt.Errorf(
		"speech: model %q is not in the catalog and no directory of that name exists under %s — did you mean %s?: %w",
		cfg.Model, cfg.Paths.Models, strings.Join(quoted, " or "), cause)
}

// Factory instantiates a Transcriber for the configured model. Which
// transcriber that is follows from the model, not from a key: the catalog
// says which runtime owns the model, and the runtime plus `[advanced]` says
// where it runs.
func Factory(cfg config.Config, logger *slog.Logger) (Transcriber, error) {
	if logger == nil {
		logger = slog.Default()
	}

	res, err := Resolve(cfg)
	if err != nil {
		return nil, err
	}
	return FactoryFor(cfg, res, logger)
}

// FactoryFor builds the transcriber for an already-resolved model, so a
// caller that has resolved the configuration for its own reasons — to log it,
// or to report it in `doctor` — does not resolve it twice.
func FactoryFor(cfg config.Config, res Resolution, logger *slog.Logger) (Transcriber, error) {
	if logger == nil {
		logger = slog.Default()
	}

	switch res.Placement {
	case models.PlacementSubprocess:
		cli := NewWhisperCli(res.ModelPath)
		cli.Threads = cfg.Advanced.Threads
		cli.NoGPU = cfg.GPUOff()
		cli.Logger = logger
		return cli, nil

	case models.PlacementRemote:
		st := NewServerTranscriber(res.Server)
		st.Model = cfg.Model
		st.Logger = logger
		return st, nil

	case models.PlacementLocalServer:
		// There is no endpoint to configure: mavor starts the child and the
		// supervisor picks a free loopback port for it, which the transcriber
		// reads back through Supervisor.Endpoint.
		st := NewServerTranscriber("")
		st.Model = cfg.Model
		st.Logger = logger
		st.Supervisor = NewSupervisor(SupervisorConfig{
			ModelPath: res.ModelPath,
			Threads:   cfg.Advanced.Threads,
			NoGPU:     cfg.GPUOff(),
			Logger:    logger,
		})
		return st, nil

	default:
		return newSherpaTranscriber(cfg, logger)
	}
}
