// Preview resolution: where the text in the overlay comes from while you
// speak, and the companion model that produces it.
//
// Two mechanisms, both named in docs/design/configuration-surface.md §6:
//
//   - A COMPANION MODEL is a small streaming recognizer loaded alongside the
//     main model, fed the same audio, emitting partial text continuously. It
//     never contributes to the final transcript.
//   - PHRASE MODE loads no second model: when you pause, the audio since the
//     last pause is transcribed with the main model and appended to the
//     preview. It is the fallback, not the default, because whisper
//     hallucinates on short clips and each phrase is decoded with no context
//     from the last.
//
// The rule that picks between them is §6.2, implemented in ResolvePreview.
package speech

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/mschulkind-oss/mavor/internal/config"
	"github.com/mschulkind-oss/mavor/internal/models"
)

// DefaultCompanionModel is the recognizer `preview.source = "auto"` loads
// alongside a main model that cannot decode incrementally.
//
// The NeMo streaming FastConformer, not the 20M zipformer that Decision Ledger
// OQ-2 chose on size. Measured against the same fixture, the same 30 ms chunks
// and the same decode loop:
//
//	zipformer-streaming-20m   first output 1.68s   "'S IS"   -> "'S IS IN THE PIT ..."
//	fastconformer-streaming   first output 1.53s   "lux"     -> "luxe is in the pit ..."
//
// The zipformer loses the opening words and shouts; the FastConformer gets
// them and is already lower case. That is the difference between a preview a
// user trusts and one that makes them wonder what else is wrong, and it is
// worth 429 MB against 121 MB — the model is a one-time download and the
// preview is on screen every time anyone dictates.
//
// zipformer-streaming-20m stays selectable by name for anyone who wants the
// smaller download.
const DefaultCompanionModel = "fastconformer-streaming"

// PreviewMode is which of the mechanisms above produces the preview text.
type PreviewMode string

const (
	// PreviewOff paints no text at all: `preview.enabled = false`.
	PreviewOff PreviewMode = "off"

	// PreviewMainModel reads the main model's own partial output. No second
	// model is loaded, because the one already loaded decodes as you speak.
	PreviewMainModel PreviewMode = "main-model"

	// PreviewCompanion runs a second, smaller streaming model alongside the
	// main one and paints its partials.
	PreviewCompanion PreviewMode = "companion"

	// PreviewPhrases re-transcribes with the main model at every pause.
	PreviewPhrases PreviewMode = "phrases"
)

// PreviewPlan is where the preview text will come from, decided once at
// daemon start from the configuration alone. It is what `mavor doctor`
// reports and what LoadPreview builds from.
type PreviewPlan struct {
	Mode PreviewMode

	// Companion is the catalog name of the model to load alongside the main
	// one, set only when Mode is PreviewCompanion.
	Companion string

	// Reason states in one clause why this mode was chosen, for `mavor
	// doctor` and the daemon's startup log.
	Reason string

	// Warnings are things worth saying out loud that are not errors — a
	// companion that is not installed, which downgrades the preview and
	// nothing else.
	Warnings []string
}

// ResolvePreview decides where the preview text comes from, per §6.2 of the
// design. `preview.source = "auto"` resolves in this order:
//
//  1. The main model already decodes incrementally (catalog Streaming) — read
//     its partials directly, load no second model.
//  2. A companion model is installed — load it and run it alongside.
//  3. Otherwise fall back to phrase mode, and say which model to pull.
//
// An explicit value overrides the order: a model name forces that companion,
// and "phrases" forces phrase mode even when a companion is available.
//
// Only case 3 downgrades. A model NAMED in `preview.source` and not installed
// is an error, never a quiet substitution (§10.2) — the caller is expected to
// treat that error as fatal at daemon start.
func ResolvePreview(cfg config.Config) (PreviewPlan, error) {
	if !cfg.Preview.Enabled {
		return PreviewPlan{Mode: PreviewOff, Reason: "preview.enabled = false"}, nil
	}

	src := strings.TrimSpace(cfg.Preview.Source)
	switch src {
	case "", "auto":
		return resolveAutoPreview(cfg), nil
	case "phrases":
		return PreviewPlan{
			Mode:   PreviewPhrases,
			Reason: `preview.source = "phrases" asked for the main model at every pause`,
		}, nil
	}

	// §10.1: `preview.source` equal to `model` is case 1, and never loads the
	// same model twice. If that model does not decode incrementally there are
	// no partials to read, and re-transcribing with it at every pause is
	// exactly phrase mode.
	if src == cfg.Model {
		if streamingModel(src) {
			return PreviewPlan{
				Mode:   PreviewMainModel,
				Reason: fmt.Sprintf("preview.source names the main model %q, which decodes incrementally", src),
			}, nil
		}
		return PreviewPlan{
			Mode:   PreviewPhrases,
			Reason: fmt.Sprintf("preview.source names the main model %q, which does not decode incrementally", src),
		}, nil
	}

	if err := companionInstalled(cfg, src); err != nil {
		return PreviewPlan{}, err
	}
	return PreviewPlan{
		Mode:      PreviewCompanion,
		Companion: src,
		Reason:    fmt.Sprintf("preview.source names %q as the model to run alongside %q", src, cfg.Model),
	}, nil
}

func resolveAutoPreview(cfg config.Config) PreviewPlan {
	if streamingModel(cfg.Model) {
		return PreviewPlan{
			Mode:   PreviewMainModel,
			Reason: fmt.Sprintf("%q decodes incrementally, so the preview reads its partials and no second model is loaded", cfg.Model),
		}
	}

	if err := companionInstalled(cfg, DefaultCompanionModel); err != nil {
		return PreviewPlan{
			Mode: PreviewPhrases,
			Reason: fmt.Sprintf("%q does not decode incrementally and the companion %q is not installed",
				cfg.Model, DefaultCompanionModel),
			// Deliberately not wrapping err: it says the model is not
			// installed, which Reason has already said, and its full
			// candidate-path chain turns one doctor line into five.
			Warnings: []string{fmt.Sprintf(
				"run `mavor models pull %s` (or `mavor setup`) for a lower-latency preview",
				DefaultCompanionModel)},
		}
	}

	return PreviewPlan{
		Mode:      PreviewCompanion,
		Companion: DefaultCompanionModel,
		Reason: fmt.Sprintf("%q does not decode incrementally, so the streaming companion %q runs alongside it",
			cfg.Model, DefaultCompanionModel),
	}
}

// streamingModel reports whether the catalog says this model decodes
// incrementally. A model the catalog does not carry is one the user installed
// by hand, and nothing here can know what it does, so it is not assumed to
// stream.
func streamingModel(name string) bool {
	m, ok := models.Lookup(name)
	return ok && m.Streaming
}

// PreviewModels reports the models this configuration's preview needs
// installed, beyond the main model. It is what makes `mavor setup` pull the
// companion (Decision Ledger OQ-3): setup asks what the config NAMES, so
// unlike ResolvePreview it does not care what happens to be on disk — a
// companion that is missing is exactly what setup is for.
func PreviewModels(cfg config.Config) []string {
	if !cfg.Preview.Enabled {
		return nil
	}
	switch src := strings.TrimSpace(cfg.Preview.Source); src {
	case "phrases":
		return nil
	case "", "auto":
		if streamingModel(cfg.Model) {
			return nil
		}
		return []string{DefaultCompanionModel}
	default:
		if src == cfg.Model {
			return nil
		}
		return []string{src}
	}
}

// companionInstalled reports whether the named model is on this machine, by
// resolving it the way the daemon would. The error it returns names the model
// and the directory searched, because that error is what a user sees when a
// model named in the config is missing.
func companionInstalled(cfg config.Config, name string) error {
	if _, err := Resolve(companionConfig(cfg, name)); err != nil {
		return fmt.Errorf("speech: preview.source = %q: %w", name, err)
	}
	return nil
}

// companionConfig is the configuration the companion is loaded under: the
// same settings, pointed at a different model. The `[advanced]` placement
// keys describe where the MAIN model's runtime runs and do not carry over —
// a companion is a streaming recognizer in the daemon's own process, and
// `advanced.server` would make models.Select refuse it outright.
func companionConfig(cfg config.Config, name string) config.Config {
	c := cfg
	c.Model = name
	c.Advanced.Placement = "auto"
	c.Advanced.Server = ""
	return c
}

// LoadedPreview is the preview the daemon will actually run, with the
// companion already loaded if there is one.
type LoadedPreview struct {
	Mode PreviewMode

	// Model is the companion's catalog name, when Mode is PreviewCompanion.
	Model string

	// Companion is the loaded recognizer, when Mode is PreviewCompanion.
	Companion StreamTranscriber

	// Reason is PreviewPlan.Reason, carried through for the startup log.
	Reason string
}

// Close releases the companion. A preview with no companion closes clean.
func (p LoadedPreview) Close() error {
	if c, ok := p.Companion.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// buildCompanion constructs the companion's transcriber. It is a variable so
// a test can stand one in: the real path loads an ONNX model through cgo and
// needs several hundred megabytes on disk.
var buildCompanion = func(cfg config.Config, logger *slog.Logger) (Transcriber, error) {
	res, err := Resolve(cfg)
	if err != nil {
		return nil, err
	}
	return FactoryFor(cfg, res, logger)
}

// LoadPreview resolves the preview and loads the companion it calls for. The
// companion loads HERE, once, at daemon start rather than lazily at the first
// recording (§10.3) — resident memory traded for first-use latency,
// deliberately.
//
// It returns an error only for a model named in the config that is not
// installed, which is fatal (§10.2). Every other failure — a corrupt model, an
// unreadable directory, a recognizer that will not start — degrades to phrase
// mode with a warning, because a broken preview must never cost the user
// dictation.
func LoadPreview(ctx context.Context, cfg config.Config, logger *slog.Logger) (LoadedPreview, error) {
	if logger == nil {
		logger = slog.Default()
	}

	plan, err := ResolvePreview(cfg)
	if err != nil {
		return LoadedPreview{}, err
	}
	for _, w := range plan.Warnings {
		logger.Warn("preview: " + w)
	}

	if plan.Mode != PreviewCompanion {
		logger.Info("preview: resolved", "mode", string(plan.Mode), "reason", plan.Reason)
		return LoadedPreview{Mode: plan.Mode, Reason: plan.Reason}, nil
	}

	companion, err := loadCompanion(ctx, cfg, plan.Companion, logger)
	if err != nil {
		logger.Warn("preview: companion failed to load — falling back to phrase mode, dictation is unaffected",
			"model", plan.Companion, "err", err)
		return LoadedPreview{
			Mode:   PreviewPhrases,
			Reason: fmt.Sprintf("companion %q failed to load: %v", plan.Companion, err),
		}, nil
	}

	logger.Info("preview: resolved", "mode", string(plan.Mode), "companion", plan.Companion, "reason", plan.Reason)
	return LoadedPreview{
		Mode:      PreviewCompanion,
		Model:     plan.Companion,
		Companion: companion,
		Reason:    plan.Reason,
	}, nil
}

func loadCompanion(ctx context.Context, cfg config.Config, name string, logger *slog.Logger) (StreamTranscriber, error) {
	ccfg := companionConfig(cfg, name)

	t, err := buildCompanion(ccfg, logger)
	if err != nil {
		return nil, err
	}

	st, ok := t.(StreamTranscriber)
	if !ok {
		closeQuietly(t)
		return nil, fmt.Errorf("speech: model %q cannot feed a live preview: it does not accept audio while it is being spoken", name)
	}

	// The recognizer is warmed here so the first dictation is not the slow
	// one. A transcriber with nothing to warm up has no Start.
	if starter, ok := t.(interface{ Start(context.Context) error }); ok {
		if err := starter.Start(ctx); err != nil {
			closeQuietly(t)
			return nil, fmt.Errorf("speech: start companion %q: %w", name, err)
		}
	}
	return st, nil
}

func closeQuietly(t Transcriber) {
	if c, ok := t.(io.Closer); ok {
		_ = c.Close()
	}
}
