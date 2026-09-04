package speech

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mschulkind-oss/mavor/internal/config"
)

func TestFactoryCLIValidModel(t *testing.T) {
	modelDir := t.TempDir()
	modelFile := filepath.Join(modelDir, "ggml-base.en.bin")
	if err := os.WriteFile(modelFile, []byte("fake-model"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{
		Engine:    "cli",
		Model:     "base.en",
		ModelDir:  modelDir,
		GPULayers: 16,
		Threads:   4,
	}

	transcriber, err := Factory(cfg, slog.Default())
	if err != nil {
		t.Fatalf("Factory failed: %v", err)
	}
	cli, ok := transcriber.(*WhisperCli)
	if !ok {
		t.Fatalf("expected *WhisperCli, got %T", transcriber)
	}
	if cli.ModelPath != modelFile {
		t.Errorf("ModelPath = %q, want %q", cli.ModelPath, modelFile)
	}
	if cli.GPULayers != 16 {
		t.Errorf("GPULayers = %d, want 16", cli.GPULayers)
	}
	if cli.Threads != 4 {
		t.Errorf("Threads = %d, want 4", cli.Threads)
	}
}

func TestFactoryCLIMissingModel(t *testing.T) {
	cfg := config.Config{
		Engine:   "cli",
		Model:    "absent.en",
		ModelDir: t.TempDir(),
	}
	_, err := Factory(cfg, slog.Default())
	if err == nil {
		t.Fatal("expected error on missing model")
	}
	if !strings.Contains(err.Error(), "absent.en") {
		t.Fatalf("error %q should mention missing model name", err)
	}
}

func TestFactoryServerUnixSocket(t *testing.T) {
	modelDir := t.TempDir()
	modelFile := filepath.Join(modelDir, "ggml-base.en.bin")
	_ = os.WriteFile(modelFile, []byte("fake-model"), 0o644)

	sockPath := filepath.Join(t.TempDir(), "server.sock")
	cfg := config.Config{
		Engine:       "server",
		Model:        "base.en",
		ModelDir:     modelDir,
		ServerSocket: sockPath,
		GPULayers:    8,
		Threads:      2,
	}

	transcriber, err := Factory(cfg, slog.Default())
	if err != nil {
		t.Fatalf("Factory failed: %v", err)
	}
	st, ok := transcriber.(*ServerTranscriber)
	if !ok {
		t.Fatalf("expected *ServerTranscriber, got %T", transcriber)
	}
	if st.Endpoint != sockPath {
		t.Errorf("Endpoint = %q, want %q", st.Endpoint, sockPath)
	}
	if st.Supervisor == nil {
		t.Fatal("expected Supervisor to be configured for unix socket server")
	}
	if st.Supervisor.cfg.ModelPath != modelFile {
		t.Errorf("Supervisor ModelPath = %q, want %q", st.Supervisor.cfg.ModelPath, modelFile)
	}
}

func TestFactoryServerHTTP(t *testing.T) {
	cfg := config.Config{
		Engine:       "server",
		Model:        "remote-model",
		ServerSocket: "http://127.0.0.1:8080",
	}

	transcriber, err := Factory(cfg, slog.Default())
	if err != nil {
		t.Fatalf("Factory failed: %v", err)
	}
	st, ok := transcriber.(*ServerTranscriber)
	if !ok {
		t.Fatalf("expected *ServerTranscriber, got %T", transcriber)
	}
	if st.Endpoint != "http://127.0.0.1:8080" {
		t.Errorf("Endpoint = %q, want http://127.0.0.1:8080", st.Endpoint)
	}
	if st.Supervisor != nil {
		t.Errorf("expected no supervisor for remote HTTP endpoint")
	}
}

func TestFactorySherpa(t *testing.T) {
	cfg := config.Config{
		Engine: "sherpa",
	}
	_, err := Factory(cfg, slog.Default())
	if err == nil {
		t.Fatal("expected error for sherpa engine in default build")
	}
	if !strings.Contains(err.Error(), "sherpa") {
		t.Fatalf("error %q should mention sherpa", err)
	}
}

func TestFactoryUnknownEngine(t *testing.T) {
	cfg := config.Config{
		Engine: "unsupported-engine",
	}
	_, err := Factory(cfg, slog.Default())
	if err == nil {
		t.Fatal("expected error on unknown engine")
	}
	if !strings.Contains(err.Error(), "unknown engine") {
		t.Fatalf("error %q should mention unknown engine", err)
	}
}

// An error that names a command is an instruction the user will follow, so it
// has to name the binary that actually exists. Every "model not found" path in
// this package points at `mavor models pull`; naming anything else sends the
// reader to a command their shell cannot run.
func TestModelNotFoundErrorNamesTheRealBinary(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	_, err := Factory(
		config.Config{Engine: "cli", Model: "base.en", ModelDir: t.TempDir()},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err == nil {
		t.Fatal("Factory() = nil error for a model that is not on disk, want an error")
	}
	if got := err.Error(); !strings.Contains(got, "mavor models pull") {
		t.Errorf("error = %q, want it to tell the user to run `mavor models pull`", got)
	}
}

// ResolveSherpaModelDir carries the same instruction, and unlike the sherpa
// engine itself it is compiled into every build, so it is reachable here.
func TestSherpaModelNotFoundErrorNamesTheRealBinary(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	_, err := ResolveSherpaModelDir(config.Config{
		SherpaModel: "parakeet-tdt-0.6b",
		ModelDir:    t.TempDir(),
	})
	if err == nil {
		t.Fatal("ResolveSherpaModelDir() = nil error for a model that is not on disk, want an error")
	}
	if got := err.Error(); !strings.Contains(got, "mavor models pull") {
		t.Errorf("error = %q, want it to tell the user to run `mavor models pull`", got)
	}
}

// The sherpa search order must only name directories this project actually
// writes to. `mavor models pull` puts models under the configured model dir
// (internal/config.DefaultModelDir), so a candidate path under any other
// project directory can never hit, and reports a path the user cannot act on.
func TestSherpaCandidatePathsAreAllMavorOwned(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)

	_, err := ResolveSherpaModelDir(config.Config{
		SherpaModel: "parakeet-tdt-0.6b",
		ModelDir:    filepath.Join(t.TempDir(), "models"),
	})
	if err == nil {
		t.Fatal("want an error listing the candidate paths")
	}
	for _, candidate := range strings.Fields(err.Error()) {
		if strings.HasPrefix(candidate, dataHome) && !strings.Contains(candidate, "/mavor/") {
			t.Errorf("candidate %q is under XDG_DATA_HOME but not under mavor/", candidate)
		}
	}
}
