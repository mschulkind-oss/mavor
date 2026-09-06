package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mschulkind-oss/mavor/internal/config"
	"github.com/mschulkind-oss/mavor/internal/models"
	"github.com/mschulkind-oss/mavor/internal/speech"
)

// setupConfig is a config whose model cache is an empty directory this test
// owns.
func setupConfig(t *testing.T, model string) config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.Model = model
	cfg.Paths.Models = filepath.Join(t.TempDir(), "models")
	if err := os.MkdirAll(cfg.Paths.Models, 0o755); err != nil {
		t.Fatalf("mkdir models: %v", err)
	}
	return cfg
}

// fakePull records what setup asked for and puts the model where setup will
// find it next time, which is what a real `mavor models pull` leaves behind.
func fakePull(t *testing.T, cfg config.Config, pulled *[]string) func(string) error {
	t.Helper()
	return func(name string) error {
		*pulled = append(*pulled, name)
		path := installedModelPath(cfg, name)
		if models.RuntimeFor(name) == models.RuntimeWhisper {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			return os.WriteFile(path, []byte("ggml"), 0o644)
		}
		if err := os.MkdirAll(path, 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(path, "encoder.onnx"), []byte("x"), 0o644)
	}
}

// `mavor setup` pulls every model the config names — the main model AND the
// preview companion.
func TestConfiguredModelsIncludesThePreviewCompanion(t *testing.T) {
	cfg := setupConfig(t, "whisper-base.en")

	got := configuredModels(cfg)
	if len(got) != 2 || got[0] != "whisper-base.en" || got[1] != speech.DefaultCompanionModel {
		t.Fatalf("configuredModels = %v, want the main model then %q", got, speech.DefaultCompanionModel)
	}
}

// A main model that already decodes incrementally needs no companion, so
// setup has nothing extra to download.
func TestConfiguredModelsSkipsTheCompanionForAStreamingModel(t *testing.T) {
	cfg := setupConfig(t, "zipformer-streaming")

	got := configuredModels(cfg)
	if len(got) != 1 || got[0] != "zipformer-streaming" {
		t.Fatalf("configuredModels = %v, want just the main model", got)
	}
}

// The contract: after `setup` exits zero the config is fully runnable, and
// running it again downloads nothing.
func TestSetupIsIdempotent(t *testing.T) {
	cfg := setupConfig(t, "whisper-base.en")

	var first []string
	if _, err := ensureModels(cfg, configuredModels(cfg), false, fakePull(t, cfg, &first)); err != nil {
		t.Fatalf("first setup: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("first run pulled %v, want the main model and the companion", first)
	}

	var second []string
	if _, err := ensureModels(cfg, configuredModels(cfg), false, fakePull(t, cfg, &second)); err != nil {
		t.Fatalf("second setup: %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("second run downloaded %v, want nothing", second)
	}
}

// Re-run after an edit to preview.source and setup fetches exactly the model
// that edit named.
func TestSetupPullsAModelNamedByAnEditedPreviewSource(t *testing.T) {
	cfg := setupConfig(t, "whisper-base.en")

	var initial []string
	if _, err := ensureModels(cfg, configuredModels(cfg), false, fakePull(t, cfg, &initial)); err != nil {
		t.Fatalf("first setup: %v", err)
	}

	cfg.Preview.Source = "zipformer-streaming"
	var afterEdit []string
	if _, err := ensureModels(cfg, configuredModels(cfg), false, fakePull(t, cfg, &afterEdit)); err != nil {
		t.Fatalf("setup after edit: %v", err)
	}
	if len(afterEdit) != 1 || afterEdit[0] != "zipformer-streaming" {
		t.Fatalf("setup pulled %v, want just the newly named model", afterEdit)
	}
}

// A half-unpacked model directory is not an installed model: setup fetches it
// again rather than leaving a config that cannot run.
func TestSetupRefetchesAnEmptyModelDirectory(t *testing.T) {
	cfg := setupConfig(t, "whisper-base.en")
	if err := os.MkdirAll(installedModelPath(cfg, speech.DefaultCompanionModel), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(installedModelPath(cfg, cfg.Model), []byte("ggml"), 0o644); err != nil {
		t.Fatalf("write model: %v", err)
	}

	var pulled []string
	if _, err := ensureModels(cfg, configuredModels(cfg), false, fakePull(t, cfg, &pulled)); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if len(pulled) != 1 || pulled[0] != speech.DefaultCompanionModel {
		t.Fatalf("setup pulled %v, want just the empty companion directory refetched", pulled)
	}
}
