package speech

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mschulkind-oss/mavor/internal/config"
	"github.com/mschulkind-oss/mavor/internal/models"
)

// previewConfig is a config whose model cache is a directory this test owns,
// with nothing installed in it yet. XDG_DATA_HOME is redirected too, because
// ResolveSherpaModelDir looks there before it looks in paths.models and a
// model on the developer's machine would otherwise decide the result.
func previewConfig(t *testing.T, model string) config.Config {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "share"))

	cfg := config.Default()
	cfg.Model = model
	cfg.Paths.Models = filepath.Join(root, "models")
	if err := os.MkdirAll(cfg.Paths.Models, 0o755); err != nil {
		t.Fatalf("mkdir models: %v", err)
	}
	return cfg
}

func installWhisperModel(t *testing.T, cfg config.Config, name string) {
	t.Helper()
	path := WhisperModelPath(cfg.Paths.Models, name)
	if err := os.WriteFile(path, []byte("ggml"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func installSherpaModel(t *testing.T, cfg config.Config, name string) {
	t.Helper()
	// Under the directory `models pull` would use, which is not always the
	// catalog name: fastconformer-streaming pins TargetDir to "parakeet" so
	// the rename did not orphan existing downloads. A test that writes to the
	// name instead would pass while the real thing cannot find its model.
	dirName := name
	if m, ok := models.Lookup(name); ok && m.TargetDir != "" {
		dirName = m.TargetDir
	}
	dir := filepath.Join(cfg.Paths.Models, "sherpa", dirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	for _, f := range []string{"encoder.onnx", "tokens.txt"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
}

// Case 1 of §6.2: the main model already decodes incrementally, so its
// partials are read directly and no second model is loaded.
func TestPreviewAutoReadsAStreamingMainModel(t *testing.T) {
	cfg := previewConfig(t, "zipformer-streaming")
	installSherpaModel(t, cfg, "zipformer-streaming")

	plan, err := ResolvePreview(cfg)
	if err != nil {
		t.Fatalf("ResolvePreview: %v", err)
	}
	if plan.Mode != PreviewMainModel {
		t.Fatalf("mode = %q, want %q (%s)", plan.Mode, PreviewMainModel, plan.Reason)
	}
	if plan.Companion != "" {
		t.Fatalf("loaded companion %q alongside a model that already streams", plan.Companion)
	}
}

// Case 2: the main model does not stream, and the companion is installed.
func TestPreviewAutoLoadsTheInstalledCompanion(t *testing.T) {
	cfg := previewConfig(t, "whisper-base.en")
	installWhisperModel(t, cfg, "whisper-base.en")
	installSherpaModel(t, cfg, DefaultCompanionModel)

	plan, err := ResolvePreview(cfg)
	if err != nil {
		t.Fatalf("ResolvePreview: %v", err)
	}
	if plan.Mode != PreviewCompanion {
		t.Fatalf("mode = %q, want %q (%s)", plan.Mode, PreviewCompanion, plan.Reason)
	}
	if plan.Companion != DefaultCompanionModel {
		t.Fatalf("companion = %q, want %q", plan.Companion, DefaultCompanionModel)
	}
	if len(plan.Warnings) != 0 {
		t.Fatalf("warned about a companion that is installed: %v", plan.Warnings)
	}
}

// Case 3, and the ONLY case that downgrades: no companion installed under
// "auto" warns, falls back to phrase mode, and names the model to pull.
func TestPreviewAutoFallsBackToPhrasesWithNoCompanion(t *testing.T) {
	cfg := previewConfig(t, "whisper-base.en")
	installWhisperModel(t, cfg, "whisper-base.en")

	plan, err := ResolvePreview(cfg)
	if err != nil {
		t.Fatalf("auto with no companion must not be an error, got %v", err)
	}
	if plan.Mode != PreviewPhrases {
		t.Fatalf("mode = %q, want %q (%s)", plan.Mode, PreviewPhrases, plan.Reason)
	}
	if len(plan.Warnings) == 0 {
		t.Fatal("downgrading to phrase mode must warn")
	}
	if !strings.Contains(strings.Join(plan.Warnings, " "), DefaultCompanionModel) {
		t.Fatalf("warning does not name the model to pull: %v", plan.Warnings)
	}
}

// §10.2: a model NAMED in preview.source and not installed is fatal, never a
// silent downgrade, and the message names the model and where it was sought.
func TestPreviewNamedModelMissingIsFatal(t *testing.T) {
	cfg := previewConfig(t, "whisper-base.en")
	installWhisperModel(t, cfg, "whisper-base.en")
	installSherpaModel(t, cfg, DefaultCompanionModel) // available, and irrelevant
	cfg.Preview.Source = "zipformer-streaming"

	_, err := ResolvePreview(cfg)
	if err == nil {
		t.Fatal("a named model that is not installed must be fatal, not a downgrade")
	}
	if !strings.Contains(err.Error(), "zipformer-streaming") {
		t.Errorf("error does not name the model: %v", err)
	}
	if !strings.Contains(err.Error(), cfg.Paths.Models) {
		t.Errorf("error does not name the directory searched (%s): %v", cfg.Paths.Models, err)
	}
}

// An explicit "phrases" wins over a companion that is sitting right there.
func TestPreviewPhrasesOverridesAnInstalledCompanion(t *testing.T) {
	cfg := previewConfig(t, "whisper-base.en")
	installWhisperModel(t, cfg, "whisper-base.en")
	installSherpaModel(t, cfg, DefaultCompanionModel)
	cfg.Preview.Source = "phrases"

	plan, err := ResolvePreview(cfg)
	if err != nil {
		t.Fatalf("ResolvePreview: %v", err)
	}
	if plan.Mode != PreviewPhrases {
		t.Fatalf("mode = %q, want %q", plan.Mode, PreviewPhrases)
	}
	if plan.Companion != "" {
		t.Fatalf("companion %q loaded although phrase mode was asked for", plan.Companion)
	}
}

// §10.1: preview.source equal to model is case 1 — the same model is never
// loaded twice.
func TestPreviewSourceEqualToModelLoadsNothingTwice(t *testing.T) {
	cfg := previewConfig(t, "zipformer-streaming")
	installSherpaModel(t, cfg, "zipformer-streaming")
	cfg.Preview.Source = cfg.Model

	plan, err := ResolvePreview(cfg)
	if err != nil {
		t.Fatalf("ResolvePreview: %v", err)
	}
	if plan.Mode != PreviewMainModel || plan.Companion != "" {
		t.Fatalf("mode = %q companion = %q, want the main model and no second load", plan.Mode, plan.Companion)
	}
}

func TestPreviewDisabledResolvesToOff(t *testing.T) {
	cfg := previewConfig(t, "whisper-base.en")
	installWhisperModel(t, cfg, "whisper-base.en")
	installSherpaModel(t, cfg, DefaultCompanionModel)
	cfg.Preview.Enabled = false

	plan, err := ResolvePreview(cfg)
	if err != nil {
		t.Fatalf("ResolvePreview: %v", err)
	}
	if plan.Mode != PreviewOff {
		t.Fatalf("mode = %q, want %q", plan.Mode, PreviewOff)
	}
}

// PreviewModels is what `mavor setup` installs beyond the main model, and it
// asks what the config names rather than what is on disk.
func TestPreviewModelsForSetup(t *testing.T) {
	base := previewConfig(t, "whisper-base.en")

	auto := base
	if got := PreviewModels(auto); len(got) != 1 || got[0] != DefaultCompanionModel {
		t.Errorf("auto with a non-streaming model = %v, want [%s]", got, DefaultCompanionModel)
	}

	streamingMain := base
	streamingMain.Model = "zipformer-streaming"
	if got := PreviewModels(streamingMain); len(got) != 0 {
		t.Errorf("auto with a streaming model = %v, want nothing to install", got)
	}

	phrases := base
	phrases.Preview.Source = "phrases"
	if got := PreviewModels(phrases); len(got) != 0 {
		t.Errorf("phrase mode = %v, want nothing to install", got)
	}

	named := base
	named.Preview.Source = "zipformer-streaming"
	if got := PreviewModels(named); len(got) != 1 || got[0] != "zipformer-streaming" {
		t.Errorf("named source = %v, want [zipformer-streaming]", got)
	}

	off := base
	off.Preview.Enabled = false
	if got := PreviewModels(off); len(got) != 0 {
		t.Errorf("preview off = %v, want nothing to install", got)
	}
}

// stubCompanionBuilder replaces the real recognizer construction, which needs
// an ONNX model on disk and cgo.
func stubCompanionBuilder(t *testing.T, fn func(config.Config, *slog.Logger) (Transcriber, error)) {
	t.Helper()
	prev := buildCompanion
	buildCompanion = fn
	t.Cleanup(func() { buildCompanion = prev })
}

func TestLoadPreviewLoadsTheCompanion(t *testing.T) {
	cfg := previewConfig(t, "whisper-base.en")
	installWhisperModel(t, cfg, "whisper-base.en")
	installSherpaModel(t, cfg, DefaultCompanionModel)

	var loadedModel string
	stubCompanionBuilder(t, func(c config.Config, _ *slog.Logger) (Transcriber, error) {
		loadedModel = c.Model
		return NewMockStreamTranscriber("partial"), nil
	})

	got, err := LoadPreview(t.Context(), cfg, slog.Default())
	if err != nil {
		t.Fatalf("LoadPreview: %v", err)
	}
	if got.Mode != PreviewCompanion || got.Companion == nil {
		t.Fatalf("mode = %q companion = %v, want a loaded companion", got.Mode, got.Companion)
	}
	if loadedModel != DefaultCompanionModel {
		t.Errorf("built companion for model %q, want %q", loadedModel, DefaultCompanionModel)
	}
	if err := got.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// §10.2: a companion that fails to load for any reason other than being
// missing degrades to phrase mode. A broken preview must never cost the user
// dictation, so the daemon still starts.
func TestLoadPreviewDegradesWhenTheCompanionIsCorrupt(t *testing.T) {
	cfg := previewConfig(t, "whisper-base.en")
	installWhisperModel(t, cfg, "whisper-base.en")
	installSherpaModel(t, cfg, DefaultCompanionModel)

	stubCompanionBuilder(t, func(config.Config, *slog.Logger) (Transcriber, error) {
		return nil, errors.New("onnx: model file is truncated")
	})

	got, err := LoadPreview(t.Context(), cfg, slog.Default())
	if err != nil {
		t.Fatalf("a corrupt companion must not stop the daemon, got %v", err)
	}
	if got.Mode != PreviewPhrases {
		t.Fatalf("mode = %q, want %q", got.Mode, PreviewPhrases)
	}
	if got.Companion != nil {
		t.Fatal("degraded preview still carries a companion")
	}
	if !strings.Contains(got.Reason, "truncated") {
		t.Errorf("reason does not carry the cause: %q", got.Reason)
	}
}

// A model that cannot accept audio while it is being spoken is no use as a
// preview source, and saying so beats painting an overlay that never updates.
func TestLoadPreviewDegradesWhenTheCompanionCannotStream(t *testing.T) {
	cfg := previewConfig(t, "whisper-base.en")
	installWhisperModel(t, cfg, "whisper-base.en")
	installSherpaModel(t, cfg, DefaultCompanionModel)

	stubCompanionBuilder(t, func(config.Config, *slog.Logger) (Transcriber, error) {
		return &Mock{Text: "not a stream"}, nil
	})

	got, err := LoadPreview(t.Context(), cfg, slog.Default())
	if err != nil {
		t.Fatalf("LoadPreview: %v", err)
	}
	if got.Mode != PreviewPhrases {
		t.Fatalf("mode = %q, want %q", got.Mode, PreviewPhrases)
	}
}

// The fatal case survives LoadPreview: it is the one preview failure that
// stops the daemon.
func TestLoadPreviewPropagatesTheFatalNamedModel(t *testing.T) {
	cfg := previewConfig(t, "whisper-base.en")
	installWhisperModel(t, cfg, "whisper-base.en")
	cfg.Preview.Source = "zipformer-streaming-20m"

	stubCompanionBuilder(t, func(config.Config, *slog.Logger) (Transcriber, error) {
		t.Fatal("a missing named model must fail before anything is built")
		return nil, nil
	})

	if _, err := LoadPreview(t.Context(), cfg, slog.Default()); err == nil {
		t.Fatal("LoadPreview must fail for a named model that is not installed")
	}
}
